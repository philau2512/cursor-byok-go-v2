package execbridge

import (
	"strings"
	"testing"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func TestBridgeReadWastedCallDetectionAndInvalidation(t *testing.T) {
	bridge := NewBridge()
	filePath := "test/file.go"
	content := "package main\n\nfunc Hello() string { return \"hello\" }\n"

	makeMessage := func() *agentv1.ExecClientMessage {
		return &agentv1.ExecClientMessage{
			Message: &agentv1.ExecClientMessage_ReadResult{
				ReadResult: &agentv1.ReadResult{
					Result: &agentv1.ReadResult_Success{
						Success: &agentv1.ReadSuccess{
							Path:       filePath,
							TotalLines: 3,
							FileSize:   int64(len(content)),
							Output:     &agentv1.ReadSuccess_Content{Content: content},
						},
					},
				},
			},
		}
	}

	pending := runtimecore.PendingExec{
		ExecKind:   "read",
		ToolCallID: "call-read-1",
		ArgsJSON:   []byte(`{"path":"test/file.go","offset":1,"limit":100}`),
	}

	// First read: should succeed and return normal payload
	res1, err := bridge.ApplyExecClientMessage(makeMessage(), pending)
	if err != nil {
		t.Fatalf("first read failed: %v", err)
	}
	if strings.Contains(res1.ToolResultPayload, "Wasted call") {
		t.Fatalf("first read should not be wasted call, got: %s", res1.ToolResultPayload)
	}

	// Second read with identical args and identical content: should be flagged as Wasted call
	pending2 := runtimecore.PendingExec{
		ExecKind:   "read",
		ToolCallID: "call-read-2",
		ArgsJSON:   []byte(`{"path":"test/file.go","offset":1,"limit":100}`),
	}
	res2, err := bridge.ApplyExecClientMessage(makeMessage(), pending2)
	if err != nil {
		t.Fatalf("second read failed: %v", err)
	}
	if !strings.Contains(res2.ToolResultPayload, "Wasted call") {
		t.Fatalf("second read should be detected as wasted call, got: %s", res2.ToolResultPayload)
	}
	if !strings.Contains(res2.ToolResultPayload, "file.go") {
		t.Fatalf("wasted call notice should mention file name, got: %s", res2.ToolResultPayload)
	}

	// Third read with DIFFERENT offset: should succeed (not wasted)
	pending3 := runtimecore.PendingExec{
		ExecKind:   "read",
		ToolCallID: "call-read-3",
		ArgsJSON:   []byte(`{"path":"test/file.go","offset":101,"limit":100}`),
	}
	res3, err := bridge.ApplyExecClientMessage(makeMessage(), pending3)
	if err != nil {
		t.Fatalf("read with different offset failed: %v", err)
	}
	if strings.Contains(res3.ToolResultPayload, "Wasted call") {
		t.Fatalf("read with different offset should not be wasted, got: %s", res3.ToolResultPayload)
	}

	// Invalidate cache for test/file.go (e.g. after write)
	bridge.InvalidatePathReadCache(filePath)

	// Fourth read with original offset after invalidation: should succeed normally
	res4, err := bridge.ApplyExecClientMessage(makeMessage(), pending)
	if err != nil {
		t.Fatalf("read after invalidation failed: %v", err)
	}
	if strings.Contains(res4.ToolResultPayload, "Wasted call") {
		t.Fatalf("read after invalidation should not be wasted, got: %s", res4.ToolResultPayload)
	}
}
