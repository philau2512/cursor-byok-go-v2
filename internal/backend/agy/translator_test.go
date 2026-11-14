package agy

import (
	"encoding/json"
	"testing"

	"cursor/gen/agentv1"
)

func TestTranslatorEventSequence(t *testing.T) {
	var captured []*agentv1.AgentServerMessage
	translator := NewTranslator("req-123", "conv-123", "call-123", func(msg *agentv1.AgentServerMessage) error {
		captured = append(captured, msg)
		return nil
	})

	// 1. Init event
	err := translator.Translate(map[string]any{
		"event":           "init",
		"conversation_id": "conv-456",
	})
	if err != nil {
		t.Fatalf("Translate init failed: %v", err)
	}
	if translator.conversationID != "conv-456" {
		t.Errorf("Kỳ vọng conversationID = conv-456, thực tế = %s", translator.conversationID)
	}

	// 2. Thinking event
	err = translator.Translate(map[string]any{
		"event": "step_update",
		"step_update": map[string]any{
			"step_type":      "thinking",
			"thinking_delta": "Analyzing the code structure...",
		},
	})
	if err != nil {
		t.Fatalf("Translate thinking failed: %v", err)
	}

	// 3. Agent response text
	err = translator.Translate(map[string]any{
		"event": "step_update",
		"step_update": map[string]any{
			"step_type":  "agent_response",
			"text_delta": "Here is the result.",
		},
	})
	if err != nil {
		t.Fatalf("Translate agent_response failed: %v", err)
	}

	// 4. Tool execution
	toolParams, _ := json.Marshal(map[string]any{
		"path": "src/main.go",
	})
	err = translator.Translate(map[string]any{
		"event": "step_update",
		"step_update": map[string]any{
			"step_type":  "tool",
			"tool_name":  "read_file",
			"tool_id":    "tool-call-1",
			"status":     "running",
			"parameters": json.RawMessage(toolParams),
		},
	})
	if err != nil {
		t.Fatalf("Translate tool start failed: %v", err)
	}

	err = translator.Translate(map[string]any{
		"event": "step_update",
		"step_update": map[string]any{
			"step_type": "tool",
			"tool_name": "read_file",
			"tool_id":   "tool-call-1",
			"status":    "success",
		},
	})
	if err != nil {
		t.Fatalf("Translate tool complete failed: %v", err)
	}

	// 5. Result
	err = translator.Translate(map[string]any{
		"event": "result",
		"result": map[string]any{
			"status": "success",
			"usage": map[string]any{
				"input_tokens":  float64(1200),
				"output_tokens": float64(450),
			},
		},
	})
	if err != nil {
		t.Fatalf("Translate result failed: %v", err)
	}

	// Xác thực các message đã nhận
	var hasThinkingDelta bool
	var hasThinkingCompleted bool
	var hasTextDelta bool
	var hasToolStarted bool
	var hasToolCompleted bool
	var hasTurnEnded bool

	for _, msg := range captured {
		update := msg.GetInteractionUpdate()
		if update == nil {
			continue
		}
		if update.GetThinkingDelta() != nil {
			hasThinkingDelta = true
		}
		if update.GetThinkingCompleted() != nil {
			hasThinkingCompleted = true
		}
		if update.GetTextDelta() != nil {
			hasTextDelta = true
		}
		if update.GetToolCallStarted() != nil {
			hasToolStarted = true
		}
		if update.GetToolCallCompleted() != nil {
			hasToolCompleted = true
		}
		if update.GetTurnEnded() != nil {
			hasTurnEnded = true
		}
	}

	if !hasThinkingDelta {
		t.Errorf("Thiếu message ThinkingDelta")
	}
	if !hasThinkingCompleted {
		t.Errorf("Thiếu message ThinkingCompleted")
	}
	if !hasTextDelta {
		t.Errorf("Thiếu message TextDelta")
	}
	if !hasToolStarted {
		t.Errorf("Thiếu message ToolCallStarted")
	}
	if !hasToolCompleted {
		t.Errorf("Thiếu message ToolCallCompleted")
	}
	if !hasTurnEnded {
		t.Errorf("Thiếu message TurnEnded")
	}
}