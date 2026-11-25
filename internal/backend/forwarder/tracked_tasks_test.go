package forwarder

import (
	"encoding/json"
	"testing"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func TestTrackedTaskBoardLifecycle(t *testing.T) {
	service := &Service{}
	board := service.getTrackedTaskBoard("conv-test-1")

	// Verify started tool call builder for CreateTrackedTasks
	createArgs, _ := json.Marshal(map[string]any{
		"tasks": []map[string]any{
			{"client_ref": "t1", "title": "Design schema", "status": "todo"},
			{"client_ref": "t2", "title": "Implement handler", "status": "in_progress", "parent": "t1"},
		},
	})
	started := buildTrackedTaskStartedToolCall(runtimecore.ToolInvocation{
		CallID:   "call-1",
		ToolName: "CreateTrackedTasks",
		ArgsJSON: createArgs,
	})
	if started == nil || started.GetCreateTrackedTasksToolCall() == nil {
		t.Fatalf("expected CreateTrackedTasksToolCall in started tool call")
	}
	if len(started.GetCreateTrackedTasksToolCall().GetArgs().GetTasks()) != 2 {
		t.Fatalf("expected 2 tasks in started args")
	}

	// Execute create directly via board + verify state
	stream := &ActiveStream{
		RequestID:      "req-1",
		ConversationID: "conv-test-1",
	}
	_ = stream

	board.mu.Lock()
	board.nextID++
	k1 := "TASK-1"
	board.tasks[k1] = &agentv1.TrackedTaskView{
		Key:         k1,
		Title:       "Design schema",
		StatusId:    "todo",
		StatusLabel: "Todo",
		Version:     1,
	}
	board.order = append(board.order, k1)
	statuses, tags := board.recomputeCountsLocked()
	board.mu.Unlock()

	if len(statuses) != 4 {
		t.Fatalf("expected 4 default statuses, got %d", len(statuses))
	}
	if statuses[0].GetTaskCount() != 1 {
		t.Fatalf("expected todo count=1, got %d", statuses[0].GetTaskCount())
	}
	if len(tags) != 0 {
		t.Fatalf("expected 0 default tags, got %d", len(tags))
	}

	// Verify resolveStatusLocked aliases
	board.mu.Lock()
	id, label := board.resolveStatusLocked("completed")
	board.mu.Unlock()
	if id != "done" || label != "Done" {
		t.Fatalf("got (%q, %q), want (done, Done)", id, label)
	}

	// Verify CreateTrackedTasks fallback when model omits title (uses internal_notes / client_ref)
	fallbackRaw := []byte(`{"tasks":[{"client_ref":"dummy-1","internal_notes":"Init project","status":"todo"}]}`)
	fallbackArgs := &agentv1.CreateTrackedTasksArgs{}
	if err := unmarshalTrackedTaskArgs(fallbackRaw, fallbackArgs); err != nil {
		t.Fatalf("unmarshal fallback args failed: %v", err)
	}
	if len(fallbackArgs.GetTasks()) != 1 || fallbackArgs.GetTasks()[0].GetInternalNotes() != "Init project" {
		t.Fatalf("unexpected fallback args: %+v", fallbackArgs)
	}

	// Verify TrackedTasks tools are exposed in Agent mode tool catalog
	_, names, err := NewToolCatalog().Load(agentv1.AgentMode_AGENT_MODE_AGENT, "")
	if err != nil {
		t.Fatalf("ToolCatalog.Load(agent) failed: %v", err)
	}
	nameSet := make(map[string]bool, len(names))
	for _, n := range names {
		nameSet[n] = true
	}
	for _, want := range []string{"ListTrackedTasks", "CreateTrackedTasks", "UpdateTrackedTasks", "DeleteTrackedTasks"} {
		if !nameSet[want] {
			t.Fatalf("expected %s in Agent mode tool catalog, got %v", want, names)
		}
	}
}
