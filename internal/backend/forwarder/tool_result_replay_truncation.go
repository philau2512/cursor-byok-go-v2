package forwarder

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	projectedReplayKiB = 1024

	projectedReadReplayLimit       = 64 * projectedReplayKiB
	projectedShellReplayLimit      = 10000 // Cắt tỉa Output Shell trần 10.000 ký tự
	projectedShellStreamLimit      = 10000 // 10.000 ký tự cho stdout/stderr
	projectedShellInterleavedLimit = 10000 // 10.000 ký tự cho interleaved output
	projectedShellMaxLines         = 250   // Trần 250 dòng
	projectedShellHeadLines        = 100   // Lấy 100 dòng đầu
	projectedShellTailLines        = 50    // Lấy 50 dòng cuối
	projectedGrepReplayLimit       = 32 * projectedReplayKiB // Giữ nguyên 32KB ở lượt chạy hiện tại để đảm bảo AI KHÔNG bị tìm kiếm thiếu
	projectedLsReplayLimit         = 10000                   // Cắt tỉa Output Ls/DirectoryList trần 10.000 ký tự
	projectedEditReplayLimit       = 32 * projectedReplayKiB
	projectedPatchEditReplayLimit  = 32 * projectedReplayKiB
	projectedWebFetchReplayLimit   = 32 * projectedReplayKiB
	projectedWebSearchReplayLimit  = 16 * projectedReplayKiB
	projectedMcpReplayLimit        = 32 * projectedReplayKiB
)

func limitProjectedToolResultReplay(toolName string, content string, resultText string, fromStoredToolCall bool, historical bool) string {
	if compacted, ok := compactProjectedGenerateImageResultReplay(toolName, content, resultText); ok {
		return compacted
	}
	if historical {
		if compacted, ok := compactHistoricalEditErrorReplay(toolName, content); ok {
			content = compacted
			fromStoredToolCall = false
		}
		if (toolName == "Shell" || strings.EqualFold(toolName, "shell")) && len(content) > 2000 {
			return "[Previous shell output truncated to save context]"
		}
		if (toolName == "Ls" || strings.EqualFold(toolName, "ls") || toolName == "DirectoryList") && len(content) > 2000 {
			return "[Previous directory listing truncated to save context]"
		}
		if (toolName == "Read" || strings.EqualFold(toolName, "read")) && len(content) > 2000 {
			if strings.Contains(content, "name: ak-") || strings.Contains(content, "/skills/") || strings.Contains(content, "\\skills\\") {
				return "[Previous skill instructions omitted to save context]"
			}
		}
	}
	if compacted, ok := compactProjectedEditToolResultReplay(toolName, content); ok {
		content = compacted
		fromStoredToolCall = false
	}
	if compacted, ok := compactProjectedShellToolResultReplay(toolName, content); ok {
		content = compacted
		fromStoredToolCall = false
	}
	if toolName == "Shell" || strings.EqualFold(toolName, "shell") {
		content = truncateShellOutputLinesAndChars(content)
	}
	if toolName == "Ls" || strings.EqualFold(toolName, "ls") || toolName == "DirectoryList" {
		if len(content) > projectedLsReplayLimit {
			content = truncateProjectedReplayTextMiddle("Ls", content, projectedLsReplayLimit)
		}
	}
	limit, ok := projectedToolReplayLimit(toolName)
	if !ok {
		return strings.TrimSpace(content)
	}
	content = strings.TrimSpace(content)
	if len(content) <= limit {
		return content
	}
	if fromStoredToolCall {
		fallback := strings.TrimSpace(resultText)
		notice := fmt.Sprintf("[tool result replay truncated: stored ToolCall result exceeded %d bytes]", limit)
		if fallback == "" {
			fallback = notice
		} else {
			fallback += "\n\n" + notice
		}
		return truncateProjectedReplayText(toolName, fallback, limit)
	}
	return truncateProjectedReplayText(toolName, content, limit)
}

func projectedToolReplayLimit(toolName string) (int, bool) {
	switch strings.TrimSpace(toolName) {
	case "GenerateImage":
		return projectedWebSearchReplayLimit, true
	case "Read":
		return projectedReadReplayLimit, true
	case "Shell":
		return projectedShellReplayLimit, true
	case "Grep":
		return projectedGrepReplayLimit, true
	case "PatchEdit", "PatchEditLines", "PatchEditSpan":
		return projectedPatchEditReplayLimit, true
	case "Edit", "Write":
		return projectedEditReplayLimit, true
	case "WebFetch":
		return projectedWebFetchReplayLimit, true
	case "WebSearch":
		return projectedWebSearchReplayLimit, true
	case "CallMcpTool", "FetchMcpResource", "ListMcpResources":
		return projectedMcpReplayLimit, true
	case "Ls", "DirectoryList":
		return projectedLsReplayLimit, true
	case "Glob":
		return projectedGrepReplayLimit, true
	case "ReadLints":
		return projectedWebSearchReplayLimit, true
	default:
		return 0, false
	}
}

func compactProjectedGenerateImageResultReplay(toolName string, content string, resultText string) (string, bool) {
	if strings.TrimSpace(toolName) != "GenerateImage" {
		return "", false
	}
	fallback := strings.TrimSpace(resultText)
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		if fallback == "" {
			return "", false
		}
		return fallback, true
	}
	var payload any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		if fallback == "" {
			return truncateProjectedReplayText("GenerateImage", trimmed, projectedWebSearchReplayLimit), true
		}
		return fallback, true
	}
	if compactGenerateImagePayload(payload) {
		encoded, err := json.Marshal(payload)
		if err == nil {
			return string(encoded), true
		}
	}
	if fallback != "" {
		return fallback, true
	}
	return trimmed, true
}

func compactGenerateImagePayload(value any) bool {
	changed := false
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			if text, ok := child.(string); ok {
				switch key {
				case "image_data", "imageData":
					item[key] = fmt.Sprintf("[base64 image data omitted from replay; bytes=%d]", len(strings.TrimSpace(text)))
					changed = true
					continue
				}
			}
			if compactGenerateImagePayload(child) {
				changed = true
			}
		}
	case []any:
		for _, child := range item {
			if compactGenerateImagePayload(child) {
				changed = true
			}
		}
	}
	return changed
}

func patchEditReplayLimit(toolName string) int {
	switch strings.TrimSpace(toolName) {
	case "PatchEdit", "PatchEditLines", "PatchEditSpan":
		return projectedPatchEditReplayLimit
	default:
		return projectedEditReplayLimit
	}
}

func compactProjectedShellToolResultReplay(toolName string, content string) (string, bool) {
	if strings.TrimSpace(toolName) != "Shell" {
		return "", false
	}
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return "", false
	}
	var payload any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return "", false
	}
	if !compactProjectedShellFields(payload) {
		return "", false
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

func compactProjectedShellFields(value any) bool {
	changed := false
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			if text, ok := child.(string); ok {
				switch key {
				case "stdout", "stderr", "interleaved_output", "interleavedOutput":
					next := truncateShellOutputLinesAndChars(text)
					if next != text {
						item[key] = next
						changed = true
					}
					continue
				}
			}
			if compactProjectedShellFields(child) {
				changed = true
			}
		}
	case []any:
		for _, child := range item {
			if compactProjectedShellFields(child) {
				changed = true
			}
		}
	}
	return changed
}

// truncateShellOutputLinesAndChars 对 Shell 输出执行双重截断保护：
// 1. Dòng limit: Tối đa 250 dòng (lấy 100 dòng đầu + 50 dòng cuối kèm thông báo)
// 2. Ký tự limit: Tối đa 10.000 ký tự (tránh các dòng cực dài làm phình context)
func truncateShellOutputLinesAndChars(text string) string {
	if text == "" {
		return text
	}

	lines := strings.Split(text, "\n")
	totalLines := len(lines)
	totalChars := len(text)

	if totalChars <= projectedShellReplayLimit && totalLines <= projectedShellMaxLines {
		return text
	}

	// 1. Cắt tỉa theo dòng nếu vượt quá 250 dòng
	if totalLines > projectedShellMaxLines {
		head := lines[:projectedShellHeadLines]
		tail := lines[totalLines-projectedShellTailLines:]
		omitted := totalLines - (projectedShellHeadLines + projectedShellTailLines)
		notice := fmt.Sprintf("\n\n[... Output truncated: Total %d lines (%d bytes); omitted %d lines in middle. Use head/grep/tail to view specific parts ...]\n\n", totalLines, totalChars, omitted)
		truncated := strings.Join(head, "\n") + notice + strings.Join(tail, "\n")
		if len(truncated) <= projectedShellReplayLimit {
			return truncated
		}
		text = truncated
		totalChars = len(text)
	}

	// 2. Cắt tỉa theo ký tự ở giữa nếu chuỗi vẫn vượt quá 10.000 ký tự (ví dụ mỗi dòng quá dài)
	if totalChars > projectedShellReplayLimit {
		headChars := 6000
		tailChars := 3000
		if len(text) > headChars+tailChars {
			notice := fmt.Sprintf("\n\n[... Output truncated: Total %d lines (%d bytes); omitted middle. Use head/grep/tail to view specific parts ...]\n\n", totalLines, totalChars)
			head := truncateProjectedUTF8(text, headChars)
			tail := truncateProjectedUTF8Suffix(text, tailChars)
			return head + notice + tail
		}
	}

	return text
}

func compactProjectedEditToolResultReplay(toolName string, content string) (string, bool) {
	switch strings.TrimSpace(toolName) {
	case "PatchEdit", "PatchEditLines", "PatchEditSpan", "Edit", "Write":
	default:
		return "", false
	}
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return "", false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return "", false
	}
	success, ok := payload["success"].(map[string]any)
	if !ok {
		return "", false
	}
	diffString := firstJSONText(success, "diff_string", "diffString")
	beforeContent := ""
	afterContent := ""
	if diffString == "" {
		beforeContent = firstJSONText(success, "before_full_file_content", "beforeFullFileContent")
		afterContent = firstJSONText(success, "after_full_file_content", "afterFullFileContent")
		if beforeContent != "" {
			diffString, _, _ = computeEditDiff(beforeContent, afterContent)
		}
	}
	if diffString != "" {
		diffString = truncateProjectedReplayText(firstNonEmpty(strings.TrimSpace(toolName), "PatchEdit"), diffString, patchEditReplayLimit(toolName))
		encoded, err := json.Marshal(map[string]any{
			"success": map[string]any{
				"diff_string": diffString,
			},
		})
		if err != nil {
			return "", false
		}
		return string(encoded), true
	}
	if afterContent != "" {
		afterContent = truncateProjectedReplayText(firstNonEmpty(strings.TrimSpace(toolName), "Write"), afterContent, patchEditReplayLimit(toolName))
		encoded, err := json.Marshal(map[string]any{
			"success": map[string]any{
				"after_full_file_content": afterContent,
			},
		})
		if err != nil {
			return "", false
		}
		return string(encoded), true
	}
	return "", false
}

func compactHistoricalEditErrorReplay(toolName string, content string) (string, bool) {
	switch strings.TrimSpace(toolName) {
	case "PatchEdit", "PatchEditLines", "PatchEditSpan", "Edit", "Write":
	default:
		return "", false
	}
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return "", false
	}
	errorPayload, ok := payload["error"].(map[string]any)
	if !ok {
		return "", false
	}
	if firstJSONText(errorPayload, "error", "modelVisibleError") == "" {
		return "", false
	}
	encoded, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"error": "historical edit error omitted from replay; re-read the file before editing",
		},
	})
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

func firstJSONText(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key].(string); ok {
			return value
		}
	}
	return ""
}

func truncateProjectedReplayText(toolName string, text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return strings.TrimSpace(text)
	}
	original := len(text)
	notice := fmt.Sprintf("\n\n[truncated: %s result exceeded %d bytes; showing %d of %d bytes]", toolName, limit, limit, original)
	for {
		keep := limit - len(notice)
		if keep <= 0 {
			return truncateProjectedUTF8(text, limit)
		}
		kept := truncateProjectedUTF8(text, keep)
		nextNotice := fmt.Sprintf("\n\n[truncated: %s result exceeded %d bytes; showing %d of %d bytes]", toolName, limit, len(kept), original)
		output := strings.TrimRight(kept, "\n") + nextNotice
		if len(output) <= limit || nextNotice == notice {
			return output
		}
		notice = nextNotice
	}
}

func truncateProjectedReplayTextMiddle(toolName string, text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	original := len(text)
	notice := fmt.Sprintf("\n\n[truncated: %s result exceeded %d bytes; omitted middle; showing %d of %d bytes]\n\n", toolName, limit, limit, original)
	for {
		keep := limit - len(notice)
		if keep <= 0 {
			return truncateProjectedUTF8(text, limit)
		}
		headLimit := keep / 2
		tailLimit := keep - headLimit
		head := truncateProjectedUTF8(text, headLimit)
		tail := truncateProjectedUTF8Suffix(text, tailLimit)
		kept := len(head) + len(tail)
		nextNotice := fmt.Sprintf("\n\n[truncated: %s result exceeded %d bytes; omitted middle; showing %d of %d bytes]\n\n", toolName, limit, kept, original)
		output := head + nextNotice + tail
		if len(output) <= limit || nextNotice == notice {
			return output
		}
		notice = nextNotice
	}
}

func truncateProjectedUTF8(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	if limit > len(text) {
		limit = len(text)
	}
	truncated := text[:limit]
	for !utf8.ValidString(truncated) && len(truncated) > 0 {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated
}

func truncateProjectedUTF8Suffix(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	start := len(text) - limit
	if start < 0 {
		start = 0
	}
	suffix := text[start:]
	for !utf8.ValidString(suffix) && start < len(text) {
		start++
		suffix = text[start:]
	}
	return suffix
}
