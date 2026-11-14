package agy

import (
	"encoding/json"
	"testing"
)

func TestClassifyTool(t *testing.T) {
	testCases := []struct {
		name     string
		expected ToolKind
	}{
		{"read_file", ToolKindRead},
		{"readFile", ToolKindRead},
		{"view_file", ToolKindRead},
		{"edit_file", ToolKindEdit},
		{"replace_file_content", ToolKindEdit},
		{"patch_edit", ToolKindEdit},
		{"grep_search", ToolKindGrep},
		{"find_by_name", ToolKindSearch},
		{"run_command", ToolKindCommand},
		{"bash", ToolKindCommand},
		{"click_browser", ToolKindBrowser},
		{"mcp_postgres_query", ToolKindMCP},
		{"call_mcp_tool", ToolKindMCP},
		{"unknown_custom_tool", ToolKindOther},
	}

	for _, tc := range testCases {
		kind := ClassifyTool(tc.name)
		if kind != tc.expected {
			t.Errorf("Tool %s: kỳ vọng %s, thực tế %s", tc.name, tc.expected, kind)
		}
	}
}

func TestBuildDetailedToolCall(t *testing.T) {
	// 1. Read tool call
	readParams, _ := json.Marshal(map[string]any{"path": "lib/app.dart"})
	call := BuildDetailedToolCall("read_file", json.RawMessage(readParams))
	if call.GetReadToolCall() == nil || call.GetReadToolCall().GetArgs().GetPath() != "lib/app.dart" {
		t.Errorf("BuildDetailedToolCall read_file mismatch: %v", call)
	}

	// 2. Edit tool call
	editParams, _ := json.Marshal(map[string]any{"path": "lib/app.dart"})
	call = BuildDetailedToolCall("edit_file", json.RawMessage(editParams))
	if call.GetEditToolCall() == nil || call.GetEditToolCall().GetArgs().GetPath() != "lib/app.dart" {
		t.Errorf("BuildDetailedToolCall edit_file mismatch: %v", call)
	}

	// 3. Shell tool call
	cmdParams, _ := json.Marshal(map[string]any{"command": "go version"})
	call = BuildDetailedToolCall("run_command", json.RawMessage(cmdParams))
	if call.GetShellToolCall() == nil || call.GetShellToolCall().GetArgs().GetCommand() != "go version" {
		t.Errorf("BuildDetailedToolCall run_command mismatch: %v", call)
	}

	// 4. Grep tool call
	grepParams, _ := json.Marshal(map[string]any{"pattern": "func main", "path": "."})
	call = BuildDetailedToolCall("grep_search", json.RawMessage(grepParams))
	if call.GetGrepToolCall() == nil || call.GetGrepToolCall().GetArgs().GetPattern() != "func main" {
		t.Errorf("BuildDetailedToolCall grep_search mismatch: %v", call)
	}
}

func TestParseInteractionAndFormatAnswer(t *testing.T) {
	payload := []byte(`{
		"interaction": {
			"id": "act-123",
			"type": "question",
			"prompt": "Chọn môi trường triển khai",
			"options": ["staging", "production"]
		}
	}`)

	req := ParseInteraction(json.RawMessage(payload))
	if req == nil {
		t.Fatalf("ParseInteraction trả về nil")
	}
	if req.ID != "act-123" {
		t.Errorf("Kỳ vọng ID = act-123, thực tế = %s", req.ID)
	}
	if len(req.Options) != 2 || req.Options[0] != "staging" {
		t.Errorf("Options mismatch: %v", req.Options)
	}

	answerText := FormatInteractionAnswer("act-123", "staging")
	expected := "Answer interaction act-123: staging"
	if answerText != expected {
		t.Errorf("FormatInteractionAnswer: kỳ vọng %s, thực tế %s", expected, answerText)
	}
}