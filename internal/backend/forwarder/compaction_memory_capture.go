// compaction_memory_capture.go chuyển compaction handoff (cấu trúc section cố
// định theo prompt/compaction/prompt.md) thành memory entries origin=auto bằng
// parser deterministic — không phát sinh thêm provider request.
package forwarder

import (
	"context"
	"sort"
	"strings"

	"cursor/internal/memory"
)

const (
	compactionCaptureMaxEntries = 8
	compactionCaptureMaxContent = 700
	compactionCaptureMinRunes   = 25
)

// captureMemoryMapping ánh xạ một nhóm bullet nguồn sang category + title prefix.
type captureMemoryMapping struct {
	priority int
	prefix   string
	category memory.Category
	scenario string
}

var (
	mappingDirective = captureMemoryMapping{0, "Directive: ", memory.CategoryUserBehavior,
		"When planning next steps in this project, honor this directive."}
	mappingConstraint = captureMemoryMapping{1, "Constraint: ", memory.CategoryDecision,
		"When resuming or extending work in this project, recall this captured decision."}
	mappingDeadEnd = captureMemoryMapping{2, "Dead end: ", memory.CategoryPitfall,
		"When resuming or extending work in this project, recall this captured decision."}
	mappingTarget = captureMemoryMapping{3, "Target: ", memory.CategoryTaskExperience,
		"When planning next steps in this project, recall the captured objective."}
)

// captureTemplateNoise là các dòng hướng dẫn mẫu trong prompt compaction;
// model thường giữ lại nguyên văn — không phải nội dung để nhớ.
var captureTemplateNoise = []string{
	"requirement-level checklist",
	"enforced rules, safety boundaries, invariants, and latest user corrections",
	"explicit checklist tracking requirement completion",
	"step-level view of the current requirement only",
	"use standard status tags",
	"single imperative instruction telling the next agent",
	"endpoints, urls, active ports, environment variables, background processes, and reproducible steps",
	"exact file paths, symbols, configs, endpoints, ports, running processes, and test commands",
	"preserve causal chains",
	"files created/modified/deleted with concise summary of changes",
	"key functions/modules changed and any in-flight",
	"validation status: report only test or linter commands",
}

type capturedBullet struct {
	mapping captureMemoryMapping
	text    string
}

// extractMemoriesFromHandoff là pure function theo FR1-FR6 của plan.
func extractMemoriesFromHandoff(summary string, projectSlug string) []memory.Entry {
	if strings.TrimSpace(projectSlug) == "" {
		return nil
	}
	bullets := collectCaptureBullets(summary)
	if len(bullets) == 0 {
		return nil
	}
	sort.SliceStable(bullets, func(i, j int) bool {
		return bullets[i].mapping.priority < bullets[j].mapping.priority
	})
	seenText := make(map[string]struct{}, len(bullets))
	entries := make([]memory.Entry, 0, compactionCaptureMaxEntries)
	for _, bullet := range bullets {
		if len(entries) >= compactionCaptureMaxEntries {
			break
		}
		normalized := normalizeMemoryContent(bullet.text)
		if _, dup := seenText[normalized]; dup {
			continue
		}
		seenText[normalized] = struct{}{}
		entries = append(entries, buildCapturedEntry(bullet))
	}
	return entries
}

// collectCaptureBullets parse markdown "# SECTION" + bullet "- " (kể cả sub-bullet
// indent) và gán mapping theo FR2; sub-bullet nối tiếp lead "Rejected approaches"
// hoặc "user preferences & latest explicit directives" được gộp vào lead đó.
func collectCaptureBullets(summary string) []capturedBullet {
	lines := strings.Split(strings.ReplaceAll(summary, "\r\n", "\n"), "\n")
	var (
		section       string
		current       *capturedBullet
		currentIndent int
		collected     []capturedBullet
	)
	flush := func() {
		if current != nil {
			collected = append(collected, *current)
			current = nil
		}
	}
	for _, rawLine := range lines {
		line := strings.TrimRight(rawLine, " \t")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "# ") {
			flush()
			section = strings.ToUpper(strings.TrimSpace(trimmed[2:]))
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		bulletText, isBullet := parseCaptureBulletLine(trimmed)
		if !isBullet {
			continue
		}
		// Sub-bullet thuộc nhóm lead (Rejected approaches / directives): gộp
		// thẳng vào bullet cha đang mở, không phân loại lại theo section.
		if current != nil && indent > currentIndent && isLeadGroupMapping(current.mapping) {
			current.text += "; " + bulletText
			continue
		}
		mapping, lead, ok := classifyCaptureBullet(section, bulletText)
		if !ok {
			continue
		}
		if lead && indent > 0 && current != nil && current.mapping == mapping {
			// lead lặp ở indent sâu (hiếm): nối vào lead cùng nhóm.
			remainder := stripCaptureLeadFragment(mapping, bulletText)
			if remainder != "" {
				current.text += "; " + remainder
			}
			continue
		}
		flush()
		content := stripCaptureLeadFragment(mapping, bulletText)
		if content == "" || !passesCaptureContent(content) {
			continue
		}
		current = &capturedBullet{mapping: mapping, text: content}
		currentIndent = indent
	}
	flush()
	filtered := collected[:0]
	for _, bullet := range collected {
		if passesCaptureContent(bullet.text) {
			filtered = append(filtered, bullet)
		}
	}
	return filtered
}

// parseCaptureBulletLine nhận "- text", "* text", "- [x] text", "- Label: text".
func parseCaptureBulletLine(trimmed string) (string, bool) {
	body := trimmed
	switch {
	case strings.HasPrefix(body, "- "):
		body = body[2:]
	case strings.HasPrefix(body, "* "):
		body = body[2:]
	default:
		return "", false
	}
	// Bỏ checkbox "[x] " / "[ ] " đầu bullet.
	if strings.HasPrefix(body, "[") {
		if closeIdx := strings.Index(body, "]"); closeIdx >= 0 && closeIdx <= 3 {
			rest := body[closeIdx+1:]
			body = strings.TrimSpace(rest)
		}
	}
	return body, body != ""
}

// isLeadGroupMapping báo mapping là nhóm có lead mang sub-list (dead ends,
// directives) — sub-bullet indent sâu của nó được gộp vào cha.
func isLeadGroupMapping(mapping captureMemoryMapping) bool {
	return mapping.prefix == mappingDeadEnd.prefix || mapping.prefix == mappingDirective.prefix
}

// classifyCaptureBullet quyết định mapping theo section + nội dung lead.
func classifyCaptureBullet(section string, text string) (captureMemoryMapping, bool, bool) {
	lowered := strings.ToLower(text)
	switch section {
	case "HARD CONSTRAINTS & ARCHITECTURAL DECISIONS":
		if strings.HasPrefix(lowered, "rejected approaches") {
			return mappingDeadEnd, true, true
		}
		return mappingConstraint, false, true
	case "GOALS & ACCEPTANCE CRITERIA STATUS":
		if strings.HasPrefix(lowered, "specific user preferences") {
			return mappingDirective, true, true
		}
		return captureMemoryMapping{}, false, false
	case "SESSION METADATA & STATUS":
		if strings.HasPrefix(lowered, "primary target") {
			return mappingTarget, false, true
		}
		return captureMemoryMapping{}, false, false
	default:
		return captureMemoryMapping{}, false, false
	}
}

// stripCaptureLeadFragment bỏ phần lead "Rejected approaches & dead ends:" và
// tương tự, trả phần nội dung phía sau dấu ": " (hoặc toàn bullet nếu không có lead).
func stripCaptureLeadFragment(mapping captureMemoryMapping, text string) string {
	lowered := strings.ToLower(text)
	var leadPrefixes []string
	switch mapping.prefix {
	case "Dead end: ":
		leadPrefixes = []string{"rejected approaches & dead ends:", "rejected approaches and dead ends:", "rejected approaches:"}
	case "Directive: ":
		leadPrefixes = []string{"specific user preferences & latest explicit directives:", "specific user preferences and latest explicit directives:", "user preferences & latest explicit directives:"}
	}
	for _, prefix := range leadPrefixes {
		if strings.HasPrefix(lowered, prefix) {
			return strings.TrimSpace(text[len(prefix):])
		}
	}
	if mapping.prefix == "Target: " {
		if colon := strings.Index(text, ":"); colon >= 0 && strings.ToLower(text[:colon]) == "primary target" {
			return strings.TrimSpace(text[colon+1:])
		}
	}
	return text
}

func passesCaptureContent(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}
	lowered := strings.ToLower(trimmed)
	for _, noise := range captureTemplateNoise {
		if strings.HasPrefix(lowered, noise) {
			return false
		}
	}
	if strings.Contains(trimmed, "[Unobserved]") || strings.Contains(trimmed, "[Unverified]") {
		return false
	}
	runes := []rune(trimmed)
	return len(runes) >= compactionCaptureMinRunes
}

func buildCapturedEntry(bullet capturedBullet) memory.Entry {
	text := strings.TrimSpace(bullet.text)
	runes := []rune(text)
	if len(runes) > compactionCaptureMaxContent {
		text = string(runes[:compactionCaptureMaxContent]) + "…"
	}
	return memory.Entry{
		Title:         bullet.mapping.prefix + captureTitleFromText(text),
		Category:      bullet.mapping.category,
		Keywords:      autoMemoryKeywords(text),
		UsageScenario: []string{bullet.mapping.scenario},
		Origin:        memory.OriginAuto,
		Content:       text,
	}
}

// captureTitleFromText: tối đa 8 từ đầu, viết hoa chữ cái đầu.
func captureTitleFromText(text string) string {
	words := strings.Fields(text)
	if len(words) > 8 {
		words = words[:8]
	}
	title := strings.Join(words, " ")
	title = strings.TrimRight(title, " ,;:.")
	if title != "" {
		runes := []rune(title)
		title = strings.ToUpper(string(runes[0])) + string(runes[1:])
	}
	return title
}

var captureStopwords = map[string]struct{}{
	"the": {}, "and": {}, "with": {}, "for": {}, "must": {}, "never": {},
	"when": {}, "that": {}, "this": {}, "from": {}, "into": {}, "because": {},
	"have": {}, "been": {}, "were": {}, "they": {}, "them": {}, "then": {},
}

// autoMemoryKeywords: 3 token đầu dài >=4 ký tự, không phải stopword.
func autoMemoryKeywords(text string) []string {
	tokens := tokenizeLower(text)
	keywords := make([]string, 0, 3)
	for _, token := range tokens {
		if len(token) < 4 {
			continue
		}
		if _, stop := captureStopwords[token]; stop {
			continue
		}
		seen := false
		for _, existing := range keywords {
			if existing == token {
				seen = true
				break
			}
		}
		if seen {
			continue
		}
		keywords = append(keywords, token)
		if len(keywords) == 3 {
			break
		}
	}
	return keywords
}

func tokenizeLower(text string) []string {
	lowered := strings.ToLower(text)
	fields := strings.FieldsFunc(lowered, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_')
	})
	return fields
}

func normalizeMemoryContent(s string) string {
	trimmed := strings.ToLower(strings.TrimSpace(s))
	return strings.Join(strings.Fields(trimmed), " ")
}

// memoryAutoCaptureEnabled gate cấu hình memoryAutoCapture (nil = bật).
func (service *Service) memoryAutoCaptureEnabled() bool {
	if service == nil || service.memoryStore == nil {
		return false
	}
	if !service.memoryEnabledFor() {
		return false
	}
	if service.memoryConfig == nil {
		return true
	}
	return service.memoryConfig.MemoryAutoCapture()
}

// captureMemoriesFromCompaction parse handoff và ghi entries origin=auto vào
// store. Best-effort: lỗi IO chỉ log; trả (captured, skipped) cho test/debug.
func (service *Service) captureMemoriesFromCompaction(stream *ActiveStream, summaryText string) (int, int) {
	if !service.memoryAutoCaptureEnabled() {
		return 0, 0
	}
	project := projectMemorySlug(stream)
	if project == "" {
		return 0, 0
	}
	entries := extractMemoriesFromHandoff(summaryText, project)
	if len(entries) == 0 {
		return 0, 0
	}
	existing, _, err := service.memoryStore.List(project)
	if err != nil {
		service.logMemoryEvent(stream, "memory_auto_capture", map[string]any{"error": err.Error()})
		return 0, 0
	}
	known := make(map[string]struct{}, len(existing))
	for _, e := range existing {
		known[string(e.Category)+"\x00"+normalizeMemoryContent(e.Content)] = struct{}{}
	}
	captured, skipped := 0, 0
	for _, entry := range entries {
		key := string(entry.Category) + "\x00" + normalizeMemoryContent(entry.Content)
		if _, exists := known[key]; exists {
			skipped++
			continue
		}
		if containsMemorySecret(entry.Content) {
			skipped++
			continue
		}
		if _, err := service.memoryStore.Save(project, entry); err != nil {
			skipped++
			continue
		}
		known[key] = struct{}{}
		captured++
	}
	fields := map[string]any{"project": project, "captured": captured, "skipped": skipped}
	if requestID, conversationID := streamIDs(stream); requestID != "" && service.debug != nil {
		service.debug.LogRuntime(context.Background(), requestID, conversationID, "memory_auto_capture", fields)
	}
	return captured, skipped
}

func streamIDs(stream *ActiveStream) (string, string) {
	if stream == nil {
		return "", ""
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return strings.TrimSpace(stream.RequestID), strings.TrimSpace(stream.ConversationID)
}
