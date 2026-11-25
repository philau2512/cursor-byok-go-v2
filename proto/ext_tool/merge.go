// merge.go gộp định nghĩa parse từ nhiều bundle Cursor thành một pool trước khi
// emit .proto.
//
// Lý do cần merge: từ 3.30 protocol được chia ra nhiều extension bundle
// (cursor-always-local, cursor-agent-host, cursor-agent-exec...), mỗi bundle chỉ
// khai một phần message/enum/service. Extract một bundle luôn thì thiếu type mà
// internal/ đang dùng, mà thiếu type là gen/ mất symbol => build vỡ.
//
// Chỉ nhận nhánh compact: sau khi parse compact, mọi tham chiếu field/method đã
// là tên qualified, nên các định nghĩa từ bundle khác nhau nối được với nhau mà
// không phụ thuộc vị trí hay module nữa.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ExtractProtosFromFiles parse nhiều bundle, merge pool định nghĩa rồi emit một
// lần. Một file duy nhất thì dùng đường cũ để giữ nguyên hành vi.
func ExtractProtosFromFiles(inputFiles []string, outputDir string) {
	if len(inputFiles) == 0 {
		fmt.Fprintln(os.Stderr, "không có bundle đầu vào nào")
		os.Exit(1)
	}
	if len(inputFiles) == 1 {
		ExtractProtos(inputFiles[0], outputDir)
		return
	}

	activeDiagnostics = newExtractionDiagnostics()
	defer func() { activeDiagnostics = nil }()

	bundles := make([]bundleDefs, 0, len(inputFiles))
	for _, file := range inputFiles {
		content, err := os.ReadFile(file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading file %s: %v\n", file, err)
			os.Exit(1)
		}
		text := string(content)
		if !looksLikeCompactBundle(text) {
			fmt.Fprintf(os.Stderr, "bundle không dùng compact descriptor, merge chưa hỗ trợ trộn hai thế hệ: %s\n", file)
			os.Exit(1)
		}
		messages, enums, services := extractCompactProtos(text)
		for _, message := range messages {
			if len(message.Fields) == 0 {
				activeDiagnostics.emptyMessages = append(activeDiagnostics.emptyMessages, message.TypeName)
			}
		}
		fmt.Fprintf(os.Stderr, "%s: %d message, %d enum, %d service\n",
			filepath.Base(filepath.Dir(filepath.Dir(file))), len(messages), len(enums), len(services))
		bundles = append(bundles, bundleDefs{
			source:   file,
			ordinal:  len(bundles),
			messages: messages,
			enums:    enums,
			services: services,
		})
	}

	pool := mergeBundles(bundles)
	pool.reportConflicts()

	// Mọi tham chiếu đã là tên qualified nên resolver gộp chỉ cần tra tên;
	// alias/module của từng bundle riêng không còn nghĩa khi đứng cạnh nhau.
	resolver := newTypeResolver(pool.messages, pool.enums, nil, nil)
	generateProtos(pool.messages, pool.enums, pool.services, resolver, outputDir)

	validateErr := validateGeneratedProtos(outputDir, activeDiagnostics)
	printDiagnosticsSummary(activeDiagnostics)

	hardConflicts := pool.hardConflicts()
	if strictExtractionValidation && (len(hardConflicts) > 0 || hasValidationFailure(activeDiagnostics, validateErr)) {
		if validateErr != nil {
			fmt.Fprintf(os.Stderr, "Validation failed: %v\n", validateErr)
		}
		if len(hardConflicts) > 0 {
			fmt.Fprintf(os.Stderr, "Validation failed: %d xung đột cứng giữa các bundle\n", len(hardConflicts))
		}
		os.Exit(1)
	}
	if validateErr != nil {
		fmt.Fprintf(os.Stderr, "Validation warning: %v\n", validateErr)
	}

	fmt.Printf("Hoàn tất merge: %d message, %d enum, %d service\n", len(pool.messages), len(pool.enums), len(pool.services))
}

// bundleDefs là kết quả parse của một bundle.
type bundleDefs struct {
	source   string
	ordinal  int
	messages []Message
	enums    []Enum
	services []Service
}

// mergedConflict ghi lại một chỗ hai bundle nói khác nhau về cùng một định nghĩa.
type mergedConflict struct {
	Kind    string
	Subject string
	Detail  string
	// Hard = hai bundle dùng trùng field number cho hai thứ khác nhau, không thể
	// tự quyết định đúng; strict mode phải fail ở đây.
	Hard bool
}

func (conflict mergedConflict) String() string {
	if conflict.Hard {
		return fmt.Sprintf("LỖI %s %s: %s", conflict.Kind, conflict.Subject, conflict.Detail)
	}
	return fmt.Sprintf("chấp nhận %s %s: %s", conflict.Kind, conflict.Subject, conflict.Detail)
}

type mergedPool struct {
	messages  []Message
	enums     []Enum
	services  []Service
	conflicts []mergedConflict
}

// fieldSignature là khoá so sánh nội dung một field giữa hai bundle.
func fieldSignature(field Field) string {
	typeToken := ""
	switch value := field.T.(type) {
	case int:
		typeToken = fmt.Sprintf("int:%d", value)
	case string:
		typeToken = "str:" + value
	}
	return strings.Join([]string{
		field.Name, field.Kind, typeToken,
		fmt.Sprintf("%t", field.Repeated), fmt.Sprintf("%t", field.Opt), field.Oneof,
		fmt.Sprintf("%d", field.MapKey), field.MapValueKind, fmt.Sprintf("%v", field.MapValueT),
	}, "|")
}

// mergeFields hợp nhất field theo số: union các number, trùng number cùng nội
// dung thì bỏ qua. Trùng number khác tên là hard conflict chỉ khi hai bundle
// khác nhau nói khác nhau; trong cùng một bundle nhiều chunk khai báo trùng tên
// type thì đó là xung đột mềm (emitter vốn đã phải chọn một bản).
func mergeFields(base, incoming []Field, subject string, sameBundle bool, conflicts *[]mergedConflict) []Field {
	byNumber := make(map[int]Field, len(base))
	byName := make(map[string]int, len(base))
	order := make([]int, 0, len(base)+len(incoming))
	for _, field := range base {
		if _, seen := byNumber[field.No]; !seen {
			order = append(order, field.No)
		}
		byNumber[field.No] = field
		byName[field.Name] = field.No
	}
	for _, field := range incoming {
		// Proto không cho phép hai field trùng tên trong một message. Trùng tên
		// ở số khác nhau là dấu hiệu hai biến thể cùng tên type (vd
		// CreatePlanResult success/error vs accepted/rejected) bị gộp lại: bỏ
		// định nghĩa sau, giữ nguyên bản đã chọn. Đây là xung đột mềm vì không
		// có cách "đúng" cho cả hai; emitter cũ cũng chỉ phát một biến thể.
		if otherNumber, clash := byName[field.Name]; clash && otherNumber != field.No {
			*conflicts = append(*conflicts, mergedConflict{
				Kind: "field", Subject: fmt.Sprintf("%s %s", subject, field.Name),
				Detail: fmt.Sprintf("#%d trùng tên với #%d (giữ bản đầu)", field.No, otherNumber),
				Hard:   false,
			})
			continue
		}
		existing, seen := byNumber[field.No]
		if !seen {
			order = append(order, field.No)
			byNumber[field.No] = field
			byName[field.Name] = field.No
			continue
		}
		if fieldSignature(existing) == fieldSignature(field) {
			continue
		}
		// Ưu tiên định nghĩa có nhiều thông tin hơn (vd một bundle khai optional
		// rõ ràng), chỉ hard conflict khi tên field cũng khác.
		if existing.Name == field.Name {
			*conflicts = append(*conflicts, mergedConflict{
				Kind: "field", Subject: fmt.Sprintf("%s #%d", subject, field.No),
				Detail: "hai bản mô tả khác nhau (giữ bản đầy đủ hơn)", Hard: !sameBundle,
			})
			if len(field.Oneof) > len(existing.Oneof) || field.MapValueKind != "" && existing.MapValueKind == "" {
				byNumber[field.No] = field
			}
			continue
		}
		*conflicts = append(*conflicts, mergedConflict{
			Kind: "field", Subject: fmt.Sprintf("%s #%d", subject, field.No),
			Detail: fmt.Sprintf("%s xung đột với %s", existing.Name, field.Name),
			Hard:   !sameBundle,
		})
	}
	sort.Ints(order)
	merged := make([]Field, 0, len(order))
	for _, number := range order {
		merged = append(merged, byNumber[number])
	}
	return merged
}

// mergeEnumValues hợp nhất giá trị enum theo số.
func mergeEnumValues(base, incoming []EnumValue, subject string, sameBundle bool, conflicts *[]mergedConflict) []EnumValue {
	byNumber := make(map[int]EnumValue, len(base))
	order := make([]int, 0, len(base)+len(incoming))
	for _, value := range base {
		if _, seen := byNumber[value.No]; !seen {
			order = append(order, value.No)
		}
		byNumber[value.No] = value
	}
	for _, value := range incoming {
		existing, seen := byNumber[value.No]
		if !seen {
			order = append(order, value.No)
			byNumber[value.No] = value
			continue
		}
		if existing.Name == value.Name {
			continue
		}
		*conflicts = append(*conflicts, mergedConflict{
			Kind: "enum value", Subject: fmt.Sprintf("%s #%d", subject, value.No),
			Detail: fmt.Sprintf("%s xung đột với %s", existing.Name, value.Name),
			Hard:   !sameBundle,
		})
	}
	sort.Ints(order)
	merged := make([]EnumValue, 0, len(order))
	for _, number := range order {
		merged = append(merged, byNumber[number])
	}
	return merged
}

// mergeMethods hợp nhất method của service theo tên.
func mergeMethods(base, incoming []Method, subject string, sameBundle bool, conflicts *[]mergedConflict) []Method {
	byName := make(map[string]Method, len(base))
	order := make([]string, 0, len(base)+len(incoming))
	for _, method := range base {
		if _, seen := byName[method.Name]; !seen {
			order = append(order, method.Name)
		}
		byName[method.Name] = method
	}
	for _, method := range incoming {
		existing, seen := byName[method.Name]
		if !seen {
			order = append(order, method.Name)
			byName[method.Name] = method
			continue
		}
		if existing.InputType == method.InputType && existing.OutputType == method.OutputType && existing.Kind == method.Kind {
			continue
		}
		*conflicts = append(*conflicts, mergedConflict{
			Kind: "method", Subject: fmt.Sprintf("%s/%s", subject, method.Name),
			Detail: fmt.Sprintf("%s->%s khác %s->%s", existing.InputType, existing.OutputType, method.InputType, method.OutputType),
			Hard:   !sameBundle,
		})
	}
	sort.Strings(order)
	merged := make([]Method, 0, len(order))
	for _, name := range order {
		merged = append(merged, byName[name])
	}
	return merged
}

// mergeBundles gộp nhiều bundle thành một pool, dedupe theo TypeName.
func mergeBundles(bundles []bundleDefs) mergedPool {
	pool := mergedPool{}
	messageIndex := make(map[string]int)
	enumIndex := make(map[string]int)
	serviceIndex := make(map[string]int)
	messageOrigin := make(map[string]int)
	enumOrigin := make(map[string]int)
	serviceOrigin := make(map[string]int)

	for _, bundle := range bundles {
		for _, message := range bundle.messages {
			if position, ok := messageIndex[message.TypeName]; ok {
				sameBundle := messageOrigin[message.TypeName] == bundle.ordinal
				pool.messages[position].Fields = mergeFields(
					pool.messages[position].Fields, message.Fields, message.TypeName, sameBundle, &pool.conflicts)
				continue
			}
			messageIndex[message.TypeName] = len(pool.messages)
			messageOrigin[message.TypeName] = bundle.ordinal
			pool.messages = append(pool.messages, message)
		}
		for _, enum := range bundle.enums {
			if position, ok := enumIndex[enum.TypeName]; ok {
				sameBundle := enumOrigin[enum.TypeName] == bundle.ordinal
				pool.enums[position].Values = mergeEnumValues(
					pool.enums[position].Values, enum.Values, enum.TypeName, sameBundle, &pool.conflicts)
				continue
			}
			enumIndex[enum.TypeName] = len(pool.enums)
			enumOrigin[enum.TypeName] = bundle.ordinal
			pool.enums = append(pool.enums, enum)
		}
		for _, service := range bundle.services {
			if position, ok := serviceIndex[service.TypeName]; ok {
				sameBundle := serviceOrigin[service.TypeName] == bundle.ordinal
				pool.services[position].Methods = mergeMethods(
					pool.services[position].Methods, service.Methods, service.TypeName, sameBundle, &pool.conflicts)
				continue
			}
			serviceIndex[service.TypeName] = len(pool.services)
			serviceOrigin[service.TypeName] = bundle.ordinal
			pool.services = append(pool.services, service)
		}
	}
	return pool
}

func (pool mergedPool) hardConflicts() []mergedConflict {
	hard := make([]mergedConflict, 0)
	for _, conflict := range pool.conflicts {
		if conflict.Hard {
			hard = append(hard, conflict)
		}
	}
	return hard
}

func (pool mergedPool) reportConflicts() {
	if len(pool.conflicts) == 0 {
		return
	}
	soft := 0
	for _, conflict := range pool.conflicts {
		if conflict.Hard {
			fmt.Fprintf(os.Stderr, "  %s\n", conflict.String())
			continue
		}
		soft++
		if soft <= 10 {
			fmt.Fprintf(os.Stderr, "  %s\n", conflict.String())
		}
	}
	if soft > 10 {
		fmt.Fprintf(os.Stderr, "  ... và %d xung đột mềm khác\n", soft-10)
	}
}
