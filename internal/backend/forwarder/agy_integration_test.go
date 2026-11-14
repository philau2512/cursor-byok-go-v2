package forwarder

import (
	"testing"

	"cursor/gen/agentv1"
	"cursor/internal/backend/agy"
)

func TestAgyForwarderEndToEndStream(t *testing.T) {
	broker := NewStreamBroker()
	service := &Service{
		broker: broker,
	}

	requestID := "req-agy-e2e-test"
	conversationID := "conv-agy-e2e-test"

	stream, err := service.broker.OpenStream(
		requestID,
		conversationID,
		1,
		"agy/claude-3-7-sonnet",
		"Antigravity Claude 3.7",
		agentv1.AgentMode_AGENT_MODE_AGENT,
		"Hello AGY",
	)
	if err != nil {
		t.Fatalf("OpenStream thất bại: %v", err)
	}

	// Đăng ký nhận stream events
	subscriberID, _, err := service.broker.Subscribe(requestID)
	if err != nil {
		t.Fatalf("Subscribe stream thất bại: %v", err)
	}
	defer service.broker.Unsubscribe(requestID, subscriberID)

	// Kích hoạt isAgyRequest
	if !service.isAgyRequest(stream.ModelID) {
		t.Fatalf("isAgyRequest phải trả về true với model %s", stream.ModelID)
	}

	// Giả lập phát sự kiện qua Translator vào broker
	var capturedMessages []*agentv1.AgentServerMessage
	translator := agy.NewTranslator(requestID, conversationID, "call-test", func(msg *agentv1.AgentServerMessage) error {
		capturedMessages = append(capturedMessages, msg)
		return service.broker.Publish(requestID, StreamEvent{Message: msg})
	})

	// Mô phỏng 3 event stream-json
	_ = translator.Translate(map[string]any{
		"event": "step_update",
		"step_update": map[string]any{
			"step_type":      "thinking",
			"thinking_delta": "Thinking about the task...",
		},
	})

	_ = translator.Translate(map[string]any{
		"event": "step_update",
		"step_update": map[string]any{
			"step_type":  "agent_response",
			"text_delta": "Here is the response text.",
		},
	})

	_ = translator.Translate(map[string]any{
		"event": "result",
		"result": map[string]any{
			"status": "success",
			"usage": map[string]any{
				"input_tokens":  float64(100),
				"output_tokens": float64(50),
			},
		},
	})

	if len(capturedMessages) < 3 {
		t.Fatalf("Kỳ vọng nhận ít nhất 3 message, thực tế: %d", len(capturedMessages))
	}

	// Xác nhận kiểu message
	hasThinking := false
	hasText := false
	hasTurnEnded := false
	for _, m := range capturedMessages {
		update := m.GetInteractionUpdate()
		if update == nil {
			continue
		}
		if update.GetThinkingDelta() != nil {
			hasThinking = true
		}
		if update.GetTextDelta() != nil {
			hasText = true
		}
		if update.GetTurnEnded() != nil {
			hasTurnEnded = true
		}
	}

	if !hasThinking || !hasText || !hasTurnEnded {
		t.Errorf("Stream thiếu message cần thiết (hasThinking=%v, hasText=%v, hasTurnEnded=%v)", hasThinking, hasText, hasTurnEnded)
	}

	// Đọc từ broker backlog để kiểm chứng pipeline
	backlog, err := service.broker.ReadFromCursor(requestID, 0)
	if err != nil {
		t.Fatalf("ReadFromCursor thất bại: %v", err)
	}

	if len(backlog) != len(capturedMessages) {
		t.Errorf("Kỳ vọng nhận %d message từ broker, thực tế: %d", len(capturedMessages), len(backlog))
	}
}