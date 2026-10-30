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
