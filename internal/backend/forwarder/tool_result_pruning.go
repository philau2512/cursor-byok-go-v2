package forwarder

import (
	"encoding/json"
	"strings"
)

// pruneToolResultToolCallJSON 清理 tool_result payload 中的 beforeFullFileContent 与 afterFullFileContent，
// 并对超长 Shell stdout/stderr 进行 middle-truncate，从源头上切断历史 context.jsonl 随编辑与终端输出急剧膨胀的根源。
func pruneToolResultToolCallJSON(toolName string, raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return raw
	}
	changed := stripEditFullFileContents(payload, patchEditReplayLimit(toolName))
	if compactProjectedShellFields(payload) {
		changed = true
	}
	if !changed {
		return raw
	}
	clean, err := json.Marshal(payload)
	if err != nil {
		return raw
	}
	return clean
}

func stripEditFullFileContents(val any, diffLimit int) bool {
	changed := false
	switch item := val.(type) {
	case map[string]any:
		hasBefore := false
		hasAfter := false
		beforeContent := ""
		afterContent := ""
		diffString := ""
		diffKey := ""

		for k, v := range item {
			strVal, isStr := v.(string)
			switch k {
			case "beforeFullFileContent", "before_full_file_content":
				hasBefore = true
				if isStr {
					beforeContent = strVal
				}
			case "afterFullFileContent", "after_full_file_content":
				hasAfter = true
				if isStr {
					afterContent = strVal
				}
			case "diffString", "diff_string":
				if isStr {
					diffString = strings.TrimSpace(strVal)
					diffKey = k
				}
			}
		}

		if hasBefore || hasAfter {
			if diffString == "" && (beforeContent != "" || afterContent != "") {
				diff, _, _ := computeEditDiff(beforeContent, afterContent)
				diffString = strings.TrimSpace(diff)
			}
			if diffString != "" {
				if diffLimit > 0 && len(diffString) > diffLimit {
					diffString = truncateProjectedReplayText("Edit", diffString, diffLimit)
				}
				item["diffString"] = diffString
				if diffKey == "diff_string" {
					delete(item, "diff_string")
				}
			}
			delete(item, "beforeFullFileContent")
			delete(item, "before_full_file_content")
			delete(item, "afterFullFileContent")
			delete(item, "after_full_file_content")
			changed = true
		} else if diffKey != "" && diffLimit > 0 && len(diffString) > diffLimit {
			item[diffKey] = truncateProjectedReplayText("Edit", diffString, diffLimit)
			changed = true
		}

		for _, child := range item {
			if stripEditFullFileContents(child, diffLimit) {
				changed = true
			}
		}
	case []any:
		for _, child := range item {
			if stripEditFullFileContents(child, diffLimit) {
				changed = true
			}
		}
	}
	return changed
}
