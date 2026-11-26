// compact.go parse kiểu descriptor nén mà Cursor >= 3.30 dùng trong bundle.
//
// Bundle không còn khai descriptor bằng object literal {no,name,kind,T} mà
// extractor cũ đọc; thay vào đó là chuỗi nén:
//
//	class $v extends fq{… static $(){return["AgentRunRequest|1 conversation_state #0|…",Tv,rb,…]}}
//	var pq=(0,s.QT)(r.proto3,dq,"AgentMode",[[0,"UNSPECIFIED"],[1,"AGENT"],…],1)
//	const zE={typeName:"agent.v1.AgentService",methods:{run:{name:"Run",I:DT,O:LT,kind:r.MethodKind.BiDiStreaming},…}}
//	const dq="agent.v1."  +  class fq extends …{static $p(){return dq}}
//
// Grammar field: "<number> <name> <typeExpr>[ <oneofName>]", trong đó typeExpr
// là "9", "9?", "#3*", "9,#0" (map key,value)…; số là FieldNumberType, "#N"
// là index vào mảng refs đứng sau chuỗi schema.
//
// Kết quả nạp vào đúng struct cũ (Message/Enum/Service + Field) để tái dùng
// TypeResolver và generateProtos, giữ nguyên format output của pipeline.
package main

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// compactScalarTypes ánh xạ FieldNumberType sang tên scalar proto.
var compactScalarTypes = map[int]string{
	1:  "double",
	2:  "float",
	3:  "int64",
	4:  "uint64",
	5:  "int32",
	6:  "fixed64",
	7:  "sfixed64",
	8:  "bool",
	9:  "string",
	12: "bytes",
	13: "uint32",
	15: "fixed32",
	16: "sfixed32",
	17: "sint32",
	18: "sint64",
}

// compactWellKnownTypes map định danh JS dạng "r.Value" sang tên qualified.
var compactWellKnownTypes = map[string]string{
	"Value": "google.protobuf.Value", "Struct": "google.protobuf.Struct",
	"ListValue": "google.protobuf.ListValue", "NullValue": "google.protobuf.NullValue",
	"Timestamp": "google.protobuf.Timestamp", "Duration": "google.protobuf.Duration",
	"Empty": "google.protobuf.Empty", "Any": "google.protobuf.Any",
	"Int32Value": "google.protobuf.Int32Value", "Int64Value": "google.protobuf.Int64Value",
	"UInt32Value": "google.protobuf.UInt32Value", "UInt64Value": "google.protobuf.UInt64Value",
	"FloatValue": "google.protobuf.FloatValue", "DoubleValue": "google.protobuf.DoubleValue",
	"BoolValue": "google.protobuf.BoolValue", "StringValue": "google.protobuf.StringValue",
	"BytesValue": "google.protobuf.BytesValue", "FieldMask": "google.protobuf.FieldMask",
}

var (
	compactBlockRe  = regexp.MustCompile(`static \$\(\)\{return\["((?:[^"\\]|\\.)*)"((?:,[^\]]+)?)\]\}`)
	compactClassRe  = regexp.MustCompile(`class ([A-Za-z0-9_$]+) extends ([A-Za-z0-9_$.]+)`)
	compactPrefixRe = regexp.MustCompile(`(?:var|let|const) ([A-Za-z0-9_$]+)="([A-Za-z0-9_.]+)\."`)
	compactPkgRe    = regexp.MustCompile(`static \$p\(\)\{return ([A-Za-z0-9_$]+)\}\}`)
	compactEnumRe   = regexp.MustCompile(`([A-Za-z0-9_$]+)=\(0,[A-Za-z0-9_$.]+\)\([A-Za-z0-9_$.]+,([A-Za-z0-9_$]+),"([A-Za-z0-9_.]+)",\[\[`)
	compactValueRe  = regexp.MustCompile(`\[(\d+),"([^"]+)"\]`)
	compactSvcRe    = regexp.MustCompile(`([A-Za-z0-9_$]+)=\{typeName:"([A-Za-z0-9_.]+)",methods:\{`)
	compactMthRe    = regexp.MustCompile(`\{name:"([^"]+)",I:([A-Za-z0-9_$.]+),O:([A-Za-z0-9_$.]+),kind:[A-Za-z0-9_$.]*MethodKind\.([A-Za-z]+)`)
	compactRefRe    = regexp.MustCompile(`[A-Za-z0-9_$.]+`)
)

// compactMessage là bản parse thô của một message class.
type compactMessage struct {
	ident  string
	base   string
	pkg    string
	name   string
	specs  []string
	refs   []string
	pos    int
	module int
}

// compactEnum là bản parse thô của một enum.
type compactEnum struct {
	ident  string
	pkg    string
	name   string
	values []EnumValue
	pos    int
	module int
}

// compactTypeDef là một định nghĩa tra được từ định danh JS.
type compactTypeDef struct {
	Name string
	Kind string // "message" | "enum"
}

// compactIndex tra định danh JS ra (tên qualified, kind) sau khi parse xong.
//
// Định danh minify (yr, vr, B9...) được mỗi webpack module dùng lại, nên tra
// toàn cục kiểu "định nghĩa cuối thắng" sẽ gắn nhầm type cho field. byModule
// giữ ánh xạ trong từng module và được ưu tiên; byIdent chỉ là fallback.
type compactIndex struct {
	byIdent      map[string]compactTypeDef
	byModule     map[int]map[string]compactTypeDef
	exportsByMod map[int]map[string]string
	importsByMod map[int]map[string]int
	exportGlobal map[string]string
	unresolved   map[string]int
}

func newCompactIndex() *compactIndex {
	return &compactIndex{
		byIdent:    make(map[string]compactTypeDef, 8192),
		byModule:   make(map[int]map[string]compactTypeDef, 512),
		unresolved: make(map[string]int),
	}
}

// compactModuleStartByID map module id ("3917") lấy vị trí start của nó, gộp cả
// hai shape factory (webpack 5 shorthand và "id:function(){}").
func compactModuleStartByID(text string) map[string]int {
	byID := make(map[string]int, 4096)
	for _, match := range compactModuleFactoryRe.FindAllStringSubmatchIndex(text, -1) {
		byID[text[match[2]:match[3]]] = match[0]
	}
	for _, match := range moduleStartRe.FindAllStringSubmatchIndex(text, -1) {
		if _, exists := byID[text[match[2]:match[3]]]; !exists {
			byID[text[match[2]:match[3]]] = match[0]
		}
	}
	return byID
}

// compactLoadRefs dựng hai bảng quan hệ của webpack: bảng export
// (n.d(t,{KS:()=>T}) alias -> class gốc) và bảng import (var a = n(3917)
// namespace -> module). Không có chúng thì ref dạng "a.DX" chỉ đoán được bằng
// bảng alias toàn cục, vốn trùng tên giữa hàng trăm module.
func (index *compactIndex) compactLoadRefs(text string, moduleStarts []int) {
	index.exportsByMod = make(map[int]map[string]string)
	index.importsByMod = make(map[int]map[string]int)
	index.exportGlobal = make(map[string]string)

	for _, blockMatch := range webpackExportBlockRe.FindAllStringIndex(text, -1) {
		module := 0
		if len(moduleStarts) > 0 {
			module = moduleStartForPos(moduleStarts, blockMatch[0])
		}
		blockStart := blockMatch[1] - 1
		blockEnd := findMatchingBrace(text, blockStart)
		if blockEnd == -1 {
			continue
		}
		for _, entry := range webpackExportEntryRe.FindAllStringSubmatch(text[blockStart:blockEnd], -1) {
			alias, root := entry[1], entry[2]
			if alias == "" || root == "" || alias == root {
				continue
			}
			if index.exportsByMod[module] == nil {
				index.exportsByMod[module] = make(map[string]string)
			}
			index.exportsByMod[module][alias] = root
			if _, ok := index.exportGlobal[alias]; !ok {
				index.exportGlobal[alias] = root
			}
		}
	}

	byID := compactModuleStartByID(text)
	for moduleStart, moduleEnd := range compactModuleRanges(moduleStarts) {
		if moduleEnd < 0 || moduleEnd > len(text) {
			moduleEnd = len(text)
		}
		for _, match := range moduleImportRe.FindAllStringSubmatch(text[moduleStart:moduleEnd], -1) {
			targetStart, ok := byID[match[2]]
			if !ok {
				continue
			}
			if index.importsByMod[moduleStart] == nil {
				index.importsByMod[moduleStart] = make(map[string]int)
			}
			index.importsByMod[moduleStart][match[1]] = targetStart
		}
	}
}

// compactModuleRanges trả khoảng [start, nextStart) của từng module.
func compactModuleRanges(moduleStarts []int) map[int]int {
	ranges := make(map[int]int, len(moduleStarts))
	for index, start := range moduleStarts {
		end := -1
		if index+1 < len(moduleStarts) {
			end = moduleStarts[index+1]
		}
		ranges[start] = end
	}
	return ranges
}

func (index *compactIndex) add(ident string, module int, name, kind string) {
	def := compactTypeDef{Name: name, Kind: kind}
	if existing, ok := index.byIdent[ident]; !ok || existing.Kind == kind {
		index.byIdent[ident] = def
	}
	bucket := index.byModule[module]
	if bucket == nil {
		bucket = make(map[string]compactTypeDef)
		index.byModule[module] = bucket
	}
	bucket[ident] = def
}

// resolveDottedRef phân tích ref "a.DX": namespace "a" của module đang tham
// chiếu trỏ tới module đích, alias "DX" của module đích trỏ tới class gốc.
func (index *compactIndex) resolveDottedRef(ref string, module int) (compactTypeDef, bool) {
	split := strings.LastIndex(ref, ".")
	if split <= 0 || split+1 >= len(ref) {
		return compactTypeDef{}, false
	}
	namespace, leaf := ref[:split], ref[split+1:]
	targets := make([]int, 0, 2)
	if imports := index.importsByMod[module]; imports != nil {
		if target, ok := imports[namespace]; ok {
			targets = append(targets, target)
		}
	}
	// "a.DX" cũng có thể là tham chiếu trực tiếp tới class định nghĩa trong
	// module khác mà không qua namespace: thử cả module hiện tại.
	targets = append(targets, module)
	for _, target := range targets {
		root := ""
		if exports := index.exportsByMod[target]; exports != nil {
			root = exports[leaf]
		}
		if root == "" {
			root = index.exportGlobal[leaf]
		}
		if root == "" {
			continue
		}
		if def, ok := index.byModule[target][root]; ok {
			return def, true
		}
		if def, ok := index.byIdent[root]; ok {
			return def, true
		}
	}
	return compactTypeDef{}, false
}

// lookup ưu tiên định nghĩa cùng module với nơi đang tham chiếu.
func (index *compactIndex) lookup(ident string, module int) (compactTypeDef, bool) {
	trimmed := strings.TrimSpace(ident)
	if bucket := index.byModule[module]; bucket != nil {
		if def, ok := bucket[trimmed]; ok {
			return def, true
		}
	}
	if def, ok := index.byIdent[trimmed]; ok {
		return def, true
	}
	if strings.Contains(trimmed, ".") {
		return index.resolveDottedRef(trimmed, module)
	}
	return compactTypeDef{}, false
}

func looksLikeCompactBundle(text string) bool {
	if !compactBlockRe.MatchString(text) {
		return false
	}
	return len(compactClassRe.FindAllStringIndex(text, 40)) > 0
}

// extractCompactProtos trả message/enum/service parse từ descriptor nén.
func extractCompactProtos(text string) ([]Message, []Enum, []Service) {
	scopes := newCompactScopes(text)

	rawMessages := compactParseMessages(text, scopes)
	rawEnums := compactParseEnums(text, scopes)
	services := compactParseServices(text, scopes)

	// Registry là ground truth: nếu nó nói "agent.v1.Foo" mà suy chain $p() lại
	// ra package khác, tin registry và ghi đếm để báo cáo.
	registry, registryAmbiguous := compactNameRegistry(text)
	scopes.registryOverrides = applyCompactNameRegistry(registry, rawMessages, rawEnums)
	scopes.registryAmbiguous = registryAmbiguous
	fmt.Fprintln(os.Stderr, scopes.stats())

	index := newCompactIndex()
	index.compactLoadRefs(text, scopes.moduleStarts)
	for i := range rawMessages {
		rawMessages[i].module = scopes.moduleFor(rawMessages[i].pos)
		index.add(rawMessages[i].ident, rawMessages[i].module, rawMessages[i].pkg+"."+rawMessages[i].name, "message")
	}
	for i := range rawEnums {
		rawEnums[i].module = scopes.moduleFor(rawEnums[i].pos)
		index.add(rawEnums[i].ident, rawEnums[i].module, rawEnums[i].pkg+"."+rawEnums[i].name, "enum")
	}

	messages := make([]Message, 0, len(rawMessages))
	for _, message := range rawMessages {
		qualified := message.pkg + "." + message.name
		fields := make([]Field, 0, len(message.specs))
		for _, spec := range message.specs {
			if field, ok := index.buildField(spec, message); ok {
				fields = append(fields, field)
			}
		}
		messages = append(messages, Message{
			TypeName:     qualified,
			VarName:      message.ident,
			InternalName: message.ident,
			Fields:       fields,
			Package:      message.pkg,
			ShortName:    message.name,
			Pos:          message.pos,
			// ModuleStart là module thật của class: TypeResolver dùng nó để lọc
			// ứng viên cùng module trước khi so khoảng cách vị trí.
			ModuleStart: message.module,
		})
	}

	enums := make([]Enum, 0, len(rawEnums))
	for _, enum := range rawEnums {
		enums = append(enums, Enum{
			TypeName:    enum.pkg + "." + enum.name,
			VarName:     enum.ident,
			Values:      compactNormalizeEnumValues(enum.name, enum.values),
			Package:     enum.pkg,
			ShortName:   enum.name,
			Pos:         enum.pos,
			ModuleStart: enum.module,
		})
	}

	services = index.compactNormalizeServiceIO(services, scopes)

	index.reportUnresolved(len(messages), len(enums), len(services))
	return messages, enums, services
}

var (
	// Bundle webpack 5 minified khai báo module factory dạng shorthand
	// "3917(t,e,r){...}", khác shape "id:function(...){}" mà buildModuleStarts
	// của nhánh legacy nhìn thấy. Không mở rộng buildModuleStarts vì nó còn
	// dùng cho bundle cũ; compact mode cần detector riêng.
	compactModuleFactoryRe = regexp.MustCompile(`(?:^|[,{])(\d{2,7})\s*\(\s*[A-Za-z$_][\w$]*(?:\s*,\s*[A-Za-z$_][\w$]*)*\s*\)\s*\{`)
	// Bundle nhúng bảng tên qualified (registry field-mask), ví dụ
	// "agent.v1.RequestContextSubagentsPart":{custom_subagents:"CODE"}; đây là
	// ground truth về package, đáng tin hơn chuỗi suy từ static $p().
	compactRegistryRe = regexp.MustCompile(`"([a-z_]+\.v[0-9]+)\.([A-Za-z][A-Za-z0-9_]*)"`)
)

// compactModuleStarts gộp cả hai shape module factory để xác định ranh giới
// module trong bundle minified.
func compactModuleStarts(text string) []int {
	starts := buildModuleStarts(text)
	for _, match := range compactModuleFactoryRe.FindAllStringIndex(text, -1) {
		starts = append(starts, match[0])
	}
	sort.Ints(starts)
	deduped := starts[:0]
	for index, value := range starts {
		if index > 0 && value == deduped[len(deduped)-1] {
			continue
		}
		deduped = append(deduped, value)
	}
	return deduped
}

// compactNameRegistry ánh xạ tên type -> package dựa trên bảng tên qualified
// trong bundle. Tên xuất hiện ở hai package khác nhau bị loại vì không đủ căn
// cứ quyết định bên nào đúng.
func compactNameRegistry(text string) (map[string]string, int) {
	registry := make(map[string]string, 256)
	ambiguous := make(map[string]bool)
	for _, match := range compactRegistryRe.FindAllStringSubmatch(text, -1) {
		pkg, name := match[1], match[2]
		if previous, ok := registry[name]; ok && previous != pkg {
			ambiguous[name] = true
			continue
		}
		registry[name] = pkg
	}
	for name := range ambiguous {
		delete(registry, name)
	}
	return registry, len(ambiguous)
}

// applyCompactNameRegistry hiệu chỉnh package theo registry, trả số hiệu chỉnh.
func applyCompactNameRegistry(registry map[string]string, messages []compactMessage, enums []compactEnum) int {
	overrides := 0
	for index := range messages {
		if pkg, ok := registry[messages[index].name]; ok && pkg != messages[index].pkg {
			messages[index].pkg = pkg
			overrides++
		}
	}
	for index := range enums {
		if pkg, ok := registry[enums[index].name]; ok && pkg != enums[index].pkg {
			enums[index].pkg = pkg
			overrides++
		}
	}
	return overrides
}

// compactClassRange là một khai báo class kèm phạm vi thân của nó.
type compactClassRange struct {
	name  string
	start int
	end   int
}

// compactCollectClassRanges đo phạm vi từng class bằng brace matching, để gán
// "static $p()" cho đúng class CHỨA nó. Heuristic "class gần nhất phía trước"
// trước đây nhận sai khi hai class đứng sát nhau, và hậu quả là hai message
// cùng tên nhưng khác package (agent.v1.EditResult và aiserver.v1.EditResult)
// bị gộp vào một package.
func compactCollectClassRanges(text string) []compactClassRange {
	matches := compactClassRe.FindAllStringSubmatchIndex(text, -1)
	ranges := make([]compactClassRange, 0, len(matches))
	for _, match := range matches {
		bodyStart := match[1]
		if bodyStart <= 0 || bodyStart >= len(text) || text[bodyStart] != '{' {
			// Chỉ nhận khai báo mà thân class mở ngay tại đó; nếu không thì
			// brace tiếp theo thuộc về code khác và phạm vi sẽ sai.
			continue
		}
		bodyEnd := findClassEnd(text, bodyStart)
		if bodyEnd == -1 {
			continue
		}
		ranges = append(ranges, compactClassRange{
			name:  text[match[2]:match[3]],
			start: match[0],
			end:   bodyEnd,
		})
	}
	return ranges
}

// compactInnermostClass trả class nhỏ nhất chứa offset.
func compactInnermostClass(classRanges []compactClassRange, offset int) string {
	found := ""
	bestSize := 0
	for _, classRange := range classRanges {
		if offset < classRange.start || offset >= classRange.end {
			continue
		}
		size := classRange.end - classRange.start
		if found == "" || size < bestSize {
			found = classRange.name
			bestSize = size
		}
	}
	return found
}

// compactPrefixMap và compactClassPackages cũ tra định danh JS bằng map toàn
// cục. Bundle lớn khai báo biến minify trùng tên ở nhiều webpack module
// (const a="agent.v1." ở module này, const a="aiserver.v1." ở module khác), nên
// tên nào xuất hiện sau cùng thắng, và message của agent.v1 bị ghi nhầm sang
// file aiserver.v1. compactScopes thay thế cả hai bằng tra cứu theo module.
type compactScopes struct {
	moduleStarts   []int
	prefixByModule map[int]map[string]string
	prefixGlobal   map[string]string
	pkgByModule    map[int]map[string]string
	pkgGlobal      map[string]string
	parentByModule map[int]map[string]string
	parentGlobal   map[string]string
	ambiguousNames int
	prefixMisses   int
	classPkgMisses int
	// Số lần cùng một tên class được hai module gán hai package khác nhau:
	// chính là bằng chứng tra cứu toàn cục ghi nhầm package.
	classPkgConflicts int
	// Số package phải sửa theo registry tên qualified, và số tên registry ghi
	// cho hai package khác nhau (đã loại bỏ).
	registryOverrides int
	registryAmbiguous int
}

func newCompactScopes(text string) *compactScopes {
	scopes := &compactScopes{
		moduleStarts:   compactModuleStarts(text),
		prefixByModule: make(map[int]map[string]string),
		prefixGlobal:   make(map[string]string),
		pkgByModule:    make(map[int]map[string]string),
		pkgGlobal:      make(map[string]string),
		parentByModule: make(map[int]map[string]string),
		parentGlobal:   make(map[string]string),
	}

	for _, match := range compactPrefixRe.FindAllStringSubmatchIndex(text, -1) {
		name := text[match[2]:match[3]]
		pkg := text[match[4]:match[5]]
		if !strings.Contains(pkg, ".v") {
			continue
		}
		if previous, ok := scopes.prefixGlobal[name]; ok && previous != pkg {
			scopes.ambiguousNames++
		}
		module := scopes.moduleFor(match[0])
		setScopedEntry(scopes.prefixByModule, module, name, pkg)
		scopes.prefixGlobal[name] = pkg
	}

	for _, match := range compactClassRe.FindAllStringSubmatchIndex(text, -1) {
		name := text[match[2]:match[3]]
		base := text[match[4]:match[5]]
		module := scopes.moduleFor(match[0])
		setScopedEntry(scopes.parentByModule, module, name, base)
		scopes.parentGlobal[name] = base
	}

	// static $p(){return VAR} khai báo package của chính class chứa nó, nên phải
	// quy về class innermost thay vì class gần nhất phía trước.
	classRanges := compactCollectClassRanges(text)
	for _, match := range compactPkgRe.FindAllStringSubmatchIndex(text, -1) {
		prefixVar := text[match[2]:match[3]]
		ownerPos := match[0]
		pkg := scopes.prefixAt(prefixVar, ownerPos)
		if pkg == "" {
			scopes.prefixMisses++
			continue
		}
		owner := compactInnermostClass(classRanges, ownerPos)
		if owner == "" {
			continue
		}
		if previous, ok := scopes.pkgGlobal[owner]; ok && previous != pkg {
			scopes.classPkgConflicts++
		}
		setScopedEntry(scopes.pkgByModule, scopes.moduleFor(ownerPos), owner, pkg)
		scopes.pkgGlobal[owner] = pkg
	}

	return scopes
}

func (scopes *compactScopes) moduleFor(pos int) int {
	if len(scopes.moduleStarts) == 0 {
		return 0
	}
	return moduleStartForPos(scopes.moduleStarts, pos)
}

// prefixAt ưu tiên định nghĩa trong cùng module, rồi mới tới map toàn cục.
func (scopes *compactScopes) prefixAt(varName string, pos int) string {
	if len(scopes.moduleStarts) > 0 {
		if pkg := scopes.prefixByModule[scopes.moduleFor(pos)][varName]; pkg != "" {
			return pkg
		}
	}
	return scopes.prefixGlobal[varName]
}

// classPackageAt đi theo chuỗi kế thừa để tìm package của một message class.
func (scopes *compactScopes) classPackageAt(name string, pos int) string {
	seen := make(map[string]bool, 8)
	module := scopes.moduleFor(pos)
	current := strings.TrimSpace(name)
	for current != "" && !seen[current] && len(seen) < 8 {
		seen[current] = true
		if len(scopes.moduleStarts) > 0 {
			if pkg := scopes.pkgByModule[module][current]; pkg != "" {
				return pkg
			}
		}
		if pkg := scopes.pkgGlobal[current]; pkg != "" {
			return pkg
		}
		base := ""
		if len(scopes.moduleStarts) > 0 {
			base = scopes.parentByModule[module][current]
		}
		if base == "" {
			base = scopes.parentGlobal[current]
		}
		current = base
	}
	return ""
}

func (scopes *compactScopes) stats() string {
	return fmt.Sprintf("compact scopes: %d webpack module, %d biến prefix đa nghĩa, %d class đa package đã tách, %d $p không tra được prefix, %d message không suy được package, %d package sửa theo registry (%d tên registry đa nghĩa)",
		len(scopes.moduleStarts), scopes.ambiguousNames, scopes.classPkgConflicts, scopes.prefixMisses, scopes.classPkgMisses,
		scopes.registryOverrides, scopes.registryAmbiguous)
}

func setScopedEntry(index map[int]map[string]string, module int, key, value string) {
	bucket := index[module]
	if bucket == nil {
		bucket = make(map[string]string)
		index[module] = bucket
	}
	bucket[key] = value
}

// compactNearestClass tìm khai báo class gần nhất phía trước offset.
func compactNearestClass(text string, offset int) string {
	start := offset - 6000
	if start < 0 {
		start = 0
	}
	matches := compactClassRe.FindAllStringSubmatch(text[start:offset], -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1][1]
}

// compactParseMessages trích mọi message class kèm spec field thô.
func compactParseMessages(text string, scopes *compactScopes) []compactMessage {
	classMatches := compactClassRe.FindAllStringSubmatchIndex(text, -1)
	blockMatches := compactBlockRe.FindAllStringSubmatchIndex(text, -1)
	messages := make([]compactMessage, 0, len(blockMatches))

	classCursor := 0
	for _, block := range blockMatches {
		for classCursor < len(classMatches) && classMatches[classCursor][0] < block[0] {
			classCursor++
		}
		if classCursor == 0 {
			continue
		}
		owner := classMatches[classCursor-1]
		ident := text[owner[2]:owner[3]]
		base := text[owner[4]:owner[5]]

		pkg := scopes.classPackageAt(ident, block[0])
		if pkg == "" {
			// Không suy được package: giữ agent.v1 để không mất definition,
			// diagnostics sẽ vẫn cho thấy số lượng rơi vào nhánh này.
			scopes.classPkgMisses++
			pkg = "agent.v1"
		}

		schema := compactUnescape(text[block[2]:block[3]])
		name, specs := compactSplitSchema(schema)
		if name == "" {
			continue
		}
		messages = append(messages, compactMessage{
			ident: ident,
			base:  base,
			pkg:   pkg,
			name:  name,
			specs: specs,
			refs:  compactSplitRefs(text[block[4]:block[5]]),
			pos:   owner[0],
		})
	}
	return messages
}

// compactParseEnums trích mọi enum do factory QT sinh ra.
func compactParseEnums(text string, scopes *compactScopes) []compactEnum {
	enums := make([]compactEnum, 0, 256)
	for _, match := range compactEnumRe.FindAllStringSubmatchIndex(text, -1) {
		ident := text[match[2]:match[3]]
		prefixVar := text[match[4]:match[5]]
		name := text[match[6]:match[7]]
		pkg := scopes.prefixAt(prefixVar, match[0])
		if pkg == "" {
			pkg = "agent.v1"
		}
		// Values array mở ngay trước vị trí kết thúc match (chuỗi "[[").
		open := match[1] - 2
		end := compactSkipBalanced(text, open)
		if open < 0 || end <= open {
			continue
		}
		values := make([]EnumValue, 0, 8)
		for _, valueMatch := range compactValueRe.FindAllStringSubmatch(text[open:end], -1) {
			number, err := strconv.Atoi(valueMatch[1])
			if err != nil {
				continue
			}
			values = append(values, EnumValue{No: number, Name: valueMatch[2]})
		}
		if len(values) == 0 {
			continue
		}
		enums = append(enums, compactEnum{ident: ident, pkg: pkg, name: name, values: values, pos: match[0]})
	}
	return enums
}

// compactParseServices trích service + method (I/O là định danh class).
func compactParseServices(text string, scopes *compactScopes) []Service {
	services := make([]Service, 0, 32)
	for _, match := range compactSvcRe.FindAllStringSubmatchIndex(text, -1) {
		ident := text[match[2]:match[3]]
		typeName := text[match[4]:match[5]]
		pkg, short := parseTypeName(typeName)
		if pkg == "" {
			pkg = scopes.prefixAt(ident, match[0])
		}
		open := match[1] - 1 // vị trí dấu "{" mở của methods
		end := compactSkipBalanced(text, open)
		if end <= open {
			continue
		}
		methods := make([]Method, 0, 16)
		for _, methodMatch := range compactMthRe.FindAllStringSubmatch(text[open:end], -1) {
			methods = append(methods, Method{
				Name:       methodMatch[1],
				InputType:  methodMatch[2],
				OutputType: methodMatch[3],
				Kind:       methodMatch[4],
			})
		}
		services = append(services, Service{
			TypeName:    typeName,
			VarName:     ident,
			Methods:     methods,
			Package:     pkg,
			ShortName:   short,
			Pos:         match[0],
			ModuleStart: scopes.moduleFor(match[0]),
		})
	}
	return services
}

// resolveTypeRef đổi một định danh JS (hoặc "ns.Alias") thành tên type qualified
// theo module đang tham chiếu. Trả ok=false khi không định danh được.
func (index *compactIndex) resolveTypeRef(ref string, module int) (string, bool) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return "", false
	}
	if looksLikeFullTypeName(trimmed) {
		return trimmed, true
	}
	if name, ok := compactWellKnownName(trimmed); ok {
		return name, true
	}
	if def, ok := index.lookup(trimmed, module); ok && def.Name != "" {
		return def.Name, true
	}
	return "", false
}

// compactNormalizeServiceIO phân loại I/O type của method theo module chứa
// service. Service descriptor tham chiếu bằng định danh cục bộ kiểu "n.KS"; để
// resolver đoán tiếp thì các module trùng tên sẽ gắn nhầm message, và kết quả
// là "rpc Run(stream UpdateTeamMemberInviteSettingRequest)" vô nghĩa.
func (index *compactIndex) compactNormalizeServiceIO(services []Service, scopes *compactScopes) []Service {
	normalized := make([]Service, 0, len(services))
	for _, service := range services {
		module := scopes.moduleFor(service.Pos)
		methods := make([]Method, 0, len(service.Methods))
		for _, method := range service.Methods {
			input, inputOK := index.resolveTypeRef(method.InputType, module)
			output, outputOK := index.resolveTypeRef(method.OutputType, module)
			if !inputOK || !outputOK {
				// Thà bỏ method và báo unresolved còn hơn emit tên type sai,
				// vì tên sai compile được nhưng decode nhầm message.
				index.unresolved["service-io:"+service.ShortName+"."+method.Name]++
				continue
			}
			method.InputType = input
			method.OutputType = output
			methods = append(methods, method)
		}
		service.Methods = methods
		normalized = append(normalized, service)
	}
	return normalized
}

// compactSplitSchema tách "Name|spec|spec" thành tên + list spec.
func compactSplitSchema(schema string) (string, []string) {
	parts := strings.Split(schema, "|")
	name := strings.TrimSpace(parts[0])
	if name == "" {
		return "", nil
	}
	specs := make([]string, 0, len(parts)-1)
	for _, spec := range parts[1:] {
		spec = strings.TrimSpace(spec)
		if spec != "" {
			specs = append(specs, spec)
		}
	}
	return name, specs
}

// compactSplitRefs lấy danh sách định danh refs sau chuỗi schema.
func compactSplitRefs(refsText string) []string {
	refsText = strings.TrimSpace(refsText)
	refsText = strings.TrimPrefix(refsText, ",")
	if refsText == "" {
		return nil
	}
	return compactRefRe.FindAllString(refsText, -1)
}

// buildField dựng Field từ một spec; trả ok=false khi không định danh được.
func (index *compactIndex) buildField(spec string, message compactMessage) (Field, bool) {
	tokens := strings.Fields(spec)
	if len(tokens) < 3 {
		index.unresolved["short-spec:"+spec]++
		return Field{}, false
	}
	number, err := strconv.Atoi(tokens[0])
	if err != nil {
		index.unresolved["bad-number:"+spec]++
		return Field{}, false
	}
	field := Field{No: number, Name: tokens[1]}
	typeExpr := tokens[2]
	if len(tokens) >= 4 {
		field.Oneof = tokens[3]
	}

	keyExpr, valueExpr, isMap := compactSplitTypeExpr(typeExpr)
	if isMap {
		keyNumber, keyRef, _, keyOK := index.classify(keyExpr, message, "map-key")
		if !keyOK || keyRef != "" {
			// map key bắt buộc scalar; ref làm key là tín hiệu parse sai.
			index.unresolved["map-key:"+keyExpr]++
			return Field{}, false
		}
		field.Kind = "map"
		field.MapKey = keyNumber
		valueNumber, valueRef, valueKind, valueOK := index.classify(valueExpr, message, "map-value")
		if !valueOK {
			return Field{}, false
		}
		field.Repeated = strings.Contains(valueExpr, "*")
		if valueRef != "" {
			field.MapValueKind = valueKind
			field.MapValueT = valueRef
		} else {
			field.MapValueKind = "scalar"
			field.MapValueT = valueNumber
		}
		return field, true
	}

	typeNumber, typeRef, typeKind, ok := index.classify(typeExpr, message, "field")
	if !ok {
		return Field{}, false
	}
	field.Repeated = strings.Contains(typeExpr, "*")
	field.Opt = strings.Contains(typeExpr, "?")
	if typeRef != "" {
		field.Kind = typeKind
		field.T = typeRef
	} else {
		field.Kind = "scalar"
		field.T = typeNumber
	}
	return field, true
}

// classify phân loại type token thành (scalar number, tên type, kind).
// Với ref "#N", tên type được tra theo module của message đang khai báo field để
// định danh minify trùng tên giữa các module không gắn nhầm type.
func (index *compactIndex) classify(token string, message compactMessage, role string) (int, string, string, bool) {
	clean := strings.TrimRight(token, "*?")
	if clean == "" {
		index.unresolved[role+":empty"]++
		return 0, "", "", false
	}
	if strings.HasPrefix(clean, "#") {
		refIndex, err := strconv.Atoi(strings.TrimPrefix(clean, "#"))
		if err != nil {
			index.unresolved[role+":bad-ref-index:"+clean]++
			return 0, "", "", false
		}
		if refIndex < 0 || refIndex >= len(message.refs) {
			index.unresolved[role+":ref-index-out-of-range:"+strconv.Itoa(refIndex)]++
			return 0, "", "", false
		}
		ref := message.refs[refIndex]
		if name, ok := compactWellKnownName(ref); ok {
			kind := "message"
			if name == "google.protobuf.NullValue" {
				kind = "enum"
			}
			return 0, name, kind, true
		}
		if def, ok := index.lookup(ref, message.module); ok && def.Name != "" {
			return 0, def.Name, def.Kind, true
		}
		index.unresolved[role+":ref-target:"+ref]++
		return 0, "", "", false
	}
	number, err := strconv.Atoi(clean)
	if err != nil {
		index.unresolved[role+":type-token:"+token]++
		return 0, "", "", false
	}
	if _, ok := compactScalarTypes[number]; !ok {
		index.unresolved[role+":unsupported-type:"+strconv.Itoa(number)]++
		return 0, "", "", false
	}
	return number, "", "", true
}

func compactWellKnownName(ref string) (string, bool) {
	short := ref
	if idx := strings.LastIndex(ref, "."); idx >= 0 {
		short = ref[idx+1:]
	}
	name, ok := compactWellKnownTypes[short]
	return name, ok
}

// compactSplitTypeExpr tách "9,#0" thành key/value cho map field.
func compactSplitTypeExpr(expr string) (string, string, bool) {
	if idx := strings.Index(expr, ","); idx >= 0 {
		return expr[:idx], expr[idx+1:], true
	}
	return "", expr, false
}

// compactSkipBalanced trả index sau ngoặc đóng tương ứng với vị trí open.
func compactSkipBalanced(text string, open int) int {
	if open < 0 || open >= len(text) {
		return -1
	}
	openChar, closeChar := byte('['), byte(']')
	if text[open] == '{' {
		openChar, closeChar = '{', '}'
	}
	depth, inString, escape := 0, false, false
	var quote byte
	for i := open; i < len(text); i++ {
		current := text[i]
		if inString {
			if escape {
				escape = false
				continue
			}
			switch current {
			case '\\':
				escape = true
			case quote:
				inString = false
			}
			continue
		}
		switch current {
		case '"', '\'':
			inString = true
			quote = current
		case openChar:
			depth++
		case closeChar:
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

func compactUnescape(value string) string {
	value = strings.ReplaceAll(value, `\"`, `"`)
	value = strings.ReplaceAll(value, `\\`, `\`)
	return value
}

// reportUnresolved in thống kê ref không định danh được ra stderr.
func (index *compactIndex) reportUnresolved(messageCount, enumCount, serviceCount int) {
	if len(index.unresolved) == 0 {
		return
	}
	keys := make([]string, 0, len(index.unresolved))
	for key := range index.unresolved {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	limit := len(keys)
	if limit > 15 {
		limit = 15
	}
	fmt.Fprintf(os.Stderr, "compact mode: %d/%d/%d definitions, %d nhóm ref chưa định danh:\n",
		messageCount, enumCount, serviceCount, len(keys))
	for _, key := range keys[:limit] {
		fmt.Fprintf(os.Stderr, "  - %s (%d)\n", key, index.unresolved[key])
	}
}

// compactNormalizeEnumValues khôi phục prefix chuẩn của tên giá trị enum.
//
// protobuf-es cắt prefix khỏi descriptor nén (["UNSPECIFIED","AGENT"] thay vì
// ["AGENT_MODE_UNSPECIFIED","AGENT_MODE_AGENT"]) để tiết kiệm byte. Proto3 lại
// áp C++ scoping: giá trị enum là sibling của enum trong cùng scope, nên nếu
// không có prefix thì nhiều enum cùng dùng "UNSPECIFIED" sẽ va nhau.
//
// Quy ước khớp snapshot cũ: enum lồng (name dạng "ErrorDetails.Error") lưu tên
// đầy đủ ERROR_DETAILS_ERROR_CUSTOM_MESSAGE trong bundle, nhưng pipeline cũ
// phát hành ERROR_CUSTOM_MESSAGE — tức chỉ cắt prefix đường dẫn của message
// cha, giữ prefix tên enum. protoc-gen-go nối theo scope nên tên Go giữa hai
// bên phải khớp từng ký tự, nếu không internal/ mất symbol khi sync.
func compactNormalizeEnumValues(enumName string, values []EnumValue) []EnumValue {
	leaf := strings.TrimSpace(enumName)
	parentPrefix := ""
	if dot := strings.LastIndex(leaf, "."); dot >= 0 {
		parent := compactEnumValuePrefix(leaf[:dot])
		if parent == "" {
			return values
		}
		parentPrefix = parent + "_"
		leaf = leaf[dot+1:]
	}
	prefix := compactEnumValuePrefix(leaf)
	if prefix == "" {
		return values
	}
	normalized := make([]EnumValue, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		name := strings.TrimSpace(value.Name)
		if name == "" {
			continue
		}
		if parentPrefix != "" && strings.HasPrefix(name, parentPrefix) {
			name = strings.TrimPrefix(name, parentPrefix)
		}
		if !strings.HasPrefix(name, prefix) {
			name = prefix + "_" + name
		}
		if seen[name] {
			// Hai giá trị cùng tên sau khi thêm prefix: tách bằng số để không
			// phá definition (rất hiếm, chỉ xảy ra khi bundle alias giá trị).
			name = fmt.Sprintf("%s_%d", name, value.No)
		}
		seen[name] = true
		normalized = append(normalized, EnumValue{No: value.No, Name: name})
	}
	return normalized
}

// compactEnumValuePrefix đổi "SandboxPolicy.Type" thành "SANDBOX_POLICY_TYPE".
func compactEnumValuePrefix(enumName string) string {
	path := strings.ReplaceAll(strings.TrimSpace(enumName), ".", "_")
	if path == "" {
		return ""
	}
	var builder strings.Builder
	for index := 0; index < len(path); index++ {
		current := path[index]
		previous := byte(0)
		if index > 0 {
			previous = path[index-1]
		}
		isUpper := current >= 'A' && current <= 'Z'
		previousIsLowerOrDigit := (previous >= 'a' && previous <= 'z') || (previous >= '0' && previous <= '9')
		startOfAcronym := isUpper && previous >= 'A' && previous <= 'Z' &&
			index+1 < len(path) && path[index+1] >= 'a' && path[index+1] <= 'z'
		// Chỉ chèn "_" tại ranh giới giới từ (chữ thường/số -> chữ HOA), nếu
		// không "TodoStatus" sẽ bị vỡ thành "TO_D_O_ST_A_T_U_S".
		if index > 0 && isUpper && current != '_' && previous != '_' && (previousIsLowerOrDigit || startOfAcronym) {
			builder.WriteByte('_')
		}
		switch {
		case current >= 'a' && current <= 'z':
			builder.WriteByte(current - 32)
		default:
			builder.WriteByte(current)
		}
	}
	return builder.String()
}
