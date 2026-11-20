package forwarder

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPruneToolResultToolCallJSONStripsFullFileContents(t *testing.T) {
	rawInput := []byte(`{
		"editToolCall": {
			"args": {"path": "main.go"},
			"result": {
				"success": {
					"path": "main.go",
					"linesAdded": 2,
					"linesRemoved": 1,
					"diffString": "@@ -1,3 +1,4 @@\n-old\n+new1\n+new2\n",
					"beforeFullFileContent": "package main\n\nfunc main() {\n\told()\n}\n",
					"afterFullFileContent": "package main\n\nfunc main() {\n\tnew1()\n\tnew2()\n}\n",
					"message": "File edited successfully"
				}
			}
		}
	}`)

	pruned := pruneToolResultToolCallJSON("PatchEdit", rawInput)
	prunedStr := string(pruned)

	if strings.Contains(prunedStr, "beforeFullFileContent") {
		t.Fatalf("pruned payload still contains beforeFullFileContent: %s", prunedStr)
	}
	if strings.Contains(prunedStr, "afterFullFileContent") {
		t.Fatalf("pruned payload still contains afterFullFileContent: %s", prunedStr)
	}
	if !strings.Contains(prunedStr, "diffString") {
		t.Fatalf("pruned payload lost diffString: %s", prunedStr)
	}
	if !strings.Contains(prunedStr, "File edited successfully") {
		t.Fatalf("pruned payload lost message: %s", prunedStr)
	}

	var parsed map[string]any
	if err := json.Unmarshal(pruned, &parsed); err != nil {
		t.Fatalf("failed to parse pruned json: %v", err)
	}
	editCall, _ := parsed["editToolCall"].(map[string]any)
	result, _ := editCall["result"].(map[string]any)
	success, _ := result["success"].(map[string]any)

	if _, exists := success["beforeFullFileContent"]; exists {
		t.Errorf("success object should not have beforeFullFileContent")
	}
	if _, exists := success["afterFullFileContent"]; exists {
		t.Errorf("success object should not have afterFullFileContent")
	}
	if diff, ok := success["diffString"].(string); !ok || !strings.Contains(diff, "@@") {
		t.Errorf("diffString was not preserved properly: %v", success["diffString"])
	}
}

func TestPruneToolResultToolCallJSONComputesDiffIfMissing(t *testing.T) {
	rawInput := []byte(`{
		"editToolCall": {
			"result": {
				"success": {
					"path": "test.txt",
					"beforeFullFileContent": "line 1\nline 2\n",
					"afterFullFileContent": "line 1\nline 2 modified\n"
				}
			}
		}
	}`)

	pruned := pruneToolResultToolCallJSON("Edit", rawInput)
	prunedStr := string(pruned)

	if strings.Contains(prunedStr, "beforeFullFileContent") || strings.Contains(prunedStr, "afterFullFileContent") {
		t.Fatalf("full file content fields not stripped: %s", prunedStr)
	}
	if !strings.Contains(prunedStr, "diffString") {
		t.Fatalf("diffString was not computed when missing: %s", prunedStr)
	}
}

func TestPruneToolResultToolCallJSONLeavesNonEditToolsUntouched(t *testing.T) {
	rawInput := []byte(`{"readResult": {"content": "some content"}}`)
	pruned := pruneToolResultToolCallJSON("Read", rawInput)
	if string(pruned) != string(rawInput) {
		t.Fatalf("non-edit tool payload unexpectedly modified: %s", string(pruned))
	}
}

func TestPruneToolResultToolCallJSONTruncatesLargeShellOutput(t *testing.T) {
	hugeStdout := strings.Repeat("a", 50*1024)
	rawInput, _ := json.Marshal(map[string]any{
		"shellToolCall": map[string]any{
			"result": map[string]any{
				"stdout": hugeStdout,
			},
		},
	})
	pruned := pruneToolResultToolCallJSON("Shell", rawInput)
	if len(pruned) >= len(rawInput) {
		t.Fatalf("expected shell output to be truncated: before=%d, after=%d", len(rawInput), len(pruned))
	}
	var parsed map[string]any
	if err := json.Unmarshal(pruned, &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	shellCall, _ := parsed["shellToolCall"].(map[string]any)
	result, _ := shellCall["result"].(map[string]any)
	stdout, _ := result["stdout"].(string)
	if !strings.Contains(stdout, "omitted") {
		t.Fatalf("expected middle truncation notice in stdout, got: %s", stdout[:100])
	}
}

func TestLimitProjectedToolResultReplayTruncatesAndPrunesLsOutput(t *testing.T) {
	hugeTree := strings.Repeat("{\"path\": \"/some/deep/file.go\"},\n", 1000) // ~32KB
	truncated := limitProjectedToolResultReplay("Ls", hugeTree, "", false, false)
	if len(truncated) > projectedLsReplayLimit+500 {
		t.Fatalf("expected Ls output truncated to ~%d bytes, got %d", projectedLsReplayLimit, len(truncated))
	}
	if !strings.Contains(truncated, "omitted") {
		t.Fatalf("expected middle truncation notice in Ls output, got: %s", truncated[:200])
	}

	prunedHistorical := limitProjectedToolResultReplay("Ls", hugeTree, "", false, true)
	expectedPlaceholder := "[Previous directory listing truncated to save context]"
	if prunedHistorical != expectedPlaceholder {
		t.Fatalf("expected historical Ls output pruned to %q, got %q", expectedPlaceholder, prunedHistorical)
	}
}

func TestLimitProjectedToolResultReplayPreservesGrepAtNextTurnAndPrunesAtDistance2(t *testing.T) {
	grepMatches := strings.Repeat("{\"line\": 10, \"match\": \"some code matching pattern here\"},\n", 300) // ~17KB
	// Active turn (historical = false): should preserve full matches up to 32KB so AI never misses code search results
	active := limitProjectedToolResultReplay("Grep", grepMatches, "", false, false)
	if len(active) < 15000 {
		t.Fatalf("expected active Grep to retain full matches, got len=%d", len(active))
	}

	rawToolCall, _ := json.Marshal(map[string]any{
		"tool_call_id": "call-grep-1",
		"tool_name":    "Grep",
		"tool_call": map[string]any{
			"grepToolCall": map[string]any{
				"args": map[string]any{
					"pattern": "test",
				},
			},
		},
	})
	rawToolResult, _ := json.Marshal(map[string]any{
		"tool_name":    "Grep",
		"tool_call_id": "call-grep-1",
		"result_text":  grepMatches,
		"arguments":    `{"pattern":"test"}`,
	})
	entryCall := HistoryEntry{Seq: 1, TurnSeq: 1, Role: "assistant", Kind: "tool_call", Payload: rawToolCall}
	entryResult := HistoryEntry{Seq: 2, TurnSeq: 1, Role: "tool", Kind: "tool_result", Payload: rawToolResult}

	// Scenario A: Currently at Turn 2 (NextTurnSeq = 3, currentTurnSeq = 2).
	// Turn 1's Grep has distance = 2 - 1 = 1 (< 2). It should be PRESERVED so Turn 2 can use Turn 1's search!
	convTurn2 := &ConversationFile{
		NextTurnSeq: 3,
		Entries: []HistoryEntry{
			entryCall,
			entryResult,
		},
	}
	projector := NewHistoryProjector()
	msgsTurn2, err := projector.ProjectPromptReplay(convTurn2)
	if err != nil {
		t.Fatalf("ProjectPromptReplay failed: %v", err)
	}
	if len(msgsTurn2) < 2 || strings.Contains(msgsTurn2[1].Content, "Previous grep output") {
		t.Fatalf("Turn 1 Grep should be preserved at Turn 2, got: %+v", msgsTurn2)
	}

	// Scenario B: Currently at Turn 3 (NextTurnSeq = 4, currentTurnSeq = 3).
	// Turn 1's Grep has distance = 3 - 1 = 2 (>= 2). It should now be PRUNED to save context!
	convTurn3 := &ConversationFile{
		NextTurnSeq: 4,
		Entries: []HistoryEntry{
			entryCall,
			entryResult,
		},
	}
	msgsTurn3, err := projector.ProjectPromptReplay(convTurn3)
	if err != nil {
		t.Fatalf("ProjectPromptReplay failed: %v", err)
	}
	if len(msgsTurn3) < 2 || !strings.Contains(msgsTurn3[1].Content, "Previous grep output truncated to save context") {
		t.Fatalf("Turn 1 Grep should be pruned at Turn 3, got: %+v", msgsTurn3)
	}
}

func TestLimitProjectedToolResultReplayPrunesHistoricalSkillRead(t *testing.T) {
	skillContent := "---\nname: ak-debug\ndescription: Debug misbehaving code\n---\n" + strings.Repeat("# Step details and rules\n", 200) // ~5KB
	// Active (current turn): should retain full content
	active := limitProjectedToolResultReplay("Read", skillContent, "", false, false)
	if !strings.Contains(active, "ak-debug") {
		t.Fatalf("active skill read should retain content")
	}

	// Historical (prior turn): should be pruned to lightweight notice
	prunedHistorical := limitProjectedToolResultReplay("Read", skillContent, "", false, true)
	expectedPlaceholder := "[Previous skill instructions omitted to save context]"
	if prunedHistorical != expectedPlaceholder {
		t.Fatalf("expected historical skill read pruned to %q, got %q", expectedPlaceholder, prunedHistorical)
	}
}
