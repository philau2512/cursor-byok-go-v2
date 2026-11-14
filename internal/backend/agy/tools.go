package agy

import (
	"encoding/json"
	"strings"

	"cursor/gen/agentv1"
	"google.golang.org/protobuf/types/known/structpb"
)

// ToolKind định danh các nhóm công cụ được hỗ trợ.
type ToolKind string

const (
	ToolKindRead    ToolKind = "read"
	ToolKindEdit    ToolKind = "edit"
	ToolKindGrep    ToolKind = "grep"
	ToolKindSearch  ToolKind = "search"
	ToolKindCommand ToolKind = "command"
	ToolKindBrowser ToolKind = "browser"
	ToolKindMCP     ToolKind = "mcp"
	ToolKindOther   ToolKind = "other"
)

// ClassifyTool nhận diện nhóm công cụ từ tên tool của AGY (tương tự AgyToolClassifier trong BaoCode).
func ClassifyTool(name string) ToolKind {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch {
	case containsAny(lower, "read_file", "readfile", "view_file", "open_file"):
		return ToolKindRead
	case containsAny(lower, "edit_file", "write_file", "replace_file_content", "patch_edit", "delete_file"):
		return ToolKindEdit
	case containsAny(lower, "grep_search", "grep", "rg", "search_files"):
		return ToolKindGrep
	case containsAny(lower, "find_by_name", "glob", "search_files_by_name"):
		return ToolKindSearch
	case containsAny(lower, "run_command", "execute_command", "send_command_input", "bash"):
		return ToolKindCommand
	case containsAny(lower, "browser_", "click_browser", "capture_browser_screenshot"):
		return ToolKindBrowser
	case strings.HasPrefix(lower, "mcp_") || containsAny(lower, "call_mcp_tool", "mcp_tool"):
		return ToolKindMCP
	default:
		return ToolKindOther
	}
}

// BuildDetailedToolCall chuyển đổi tên và tham số của AGY thành agentv1.ToolCall đầy đủ thuộc tính.
func BuildDetailedToolCall(toolName string, rawParams json.RawMessage) *agentv1.ToolCall {
	var paramMap map[string]any
	if len(rawParams) > 0 {
		_ = json.Unmarshal(rawParams, &paramMap)
	}
	if paramMap == nil {
		paramMap = make(map[string]any)
	}

	kind := ClassifyTool(toolName)
	switch kind {
	case ToolKindRead:
		return &agentv1.ToolCall{
			Tool: &agentv1.ToolCall_ReadToolCall{
				ReadToolCall: &agentv1.ReadToolCall{
					Args: &agentv1.ReadToolArgs{
						Path: getString(paramMap, "path", "file_path", "target"),
					},
				},
			},
		}

	case ToolKindEdit:
		return &agentv1.ToolCall{
			Tool: &agentv1.ToolCall_EditToolCall{
				EditToolCall: &agentv1.EditToolCall{
					Args: &agentv1.EditArgs{
						Path: getString(paramMap, "path", "file_path", "target"),
					},
				},
			},
		}

	case ToolKindCommand:
		return &agentv1.ToolCall{
			Tool: &agentv1.ToolCall_ShellToolCall{
				ShellToolCall: &agentv1.ShellToolCall{
					Args: &agentv1.ShellArgs{
						Command: getString(paramMap, "command", "cmd", "line"),
					},
				},
			},
		}

	case ToolKindGrep:
		pattern := getString(paramMap, "pattern", "query")
		path := getString(paramMap, "path", "dir")
		var pathPtr *string
		if path != "" {
			pathPtr = &path
		}
		return &agentv1.ToolCall{
			Tool: &agentv1.ToolCall_GrepToolCall{
				GrepToolCall: &agentv1.GrepToolCall{
					Args: &agentv1.GrepArgs{
						Pattern: pattern,
						Path:    pathPtr,
					},
				},
			},
		}

	case ToolKindSearch:
		pattern := getString(paramMap, "pattern", "glob", "glob_pattern")
		return &agentv1.ToolCall{
			Tool: &agentv1.ToolCall_GlobToolCall{
				GlobToolCall: &agentv1.GlobToolCall{
					Args: &agentv1.GlobToolArgs{
						GlobPattern: pattern,
					},
				},
			},
		}

	default:
		pbArgs, _ := structpb.NewStruct(paramMap)
		var argsMap map[string]*structpb.Value
		if pbArgs != nil {
			argsMap = pbArgs.GetFields()
		}
		return &agentv1.ToolCall{
			Tool: &agentv1.ToolCall_McpToolCall{
				McpToolCall: &agentv1.McpToolCall{
					Args: &agentv1.McpArgs{
						Name:     toolName,
						ToolName: toolName,
						Args:     argsMap,
					},
				},
			},
		}
	}
}

func containsAny(s string, patterns ...string) bool {
	for _, p := range patterns {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

func getString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if val, ok := m[k]; ok {
			if str, ok := val.(string); ok && str != "" {
				return str
			}
		}
	}
	return ""
}