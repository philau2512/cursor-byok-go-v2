package agy

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"cursor/gen/agentv1"
)

// Consumer là hàm nhận các bản tin đã dịch để đưa vào stream broker của Cursor.
type Consumer func(*agentv1.AgentServerMessage) error

// Translator chuyển đổi các event stream-json từ AGY sang Cursor AgentServerMessage.
type Translator struct {
	requestID         string
	conversationID    string
	modelCallID       string
	emit              Consumer
	mu                sync.Mutex
	thinkingStartedAt time.Time
	thinkingActive    bool
	activeTools       map[string]*agentv1.ToolCall
}

// NewTranslator tạo một bộ dịch mới gắn với requestID và modelCallID cụ thể.
func NewTranslator(requestID string, conversationID string, modelCallID string, emit Consumer) *Translator {
	return &Translator{
		requestID:      requestID,
		conversationID: conversationID,
		modelCallID:    modelCallID,
		emit:           emit,
		activeTools:    make(map[string]*agentv1.ToolCall),
	}
}

// Translate nhận một event map từ Transport và phát các AgentServerMessage tương ứng.
func (t *Translator) Translate(raw map[string]any) error {
	data, err := json.Marshal(raw)
	if err != nil {
		return err
	}

	var event AgyEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return err
	}

	switch event.Event {
	case "init":
		t.mu.Lock()
		if event.ConversationID != "" {
			t.conversationID = event.ConversationID
		}
		t.mu.Unlock()
		return nil

	case "step_update":
		if event.StepUpdate != nil {
			return t.handleStepUpdate(event.StepUpdate)
		}
		return nil

	case "result":
		return t.handleResult(event.Result)

	default:
		return nil
	}
}

func (t *Translator) handleStepUpdate(step *AgyStepUpdate) error {
	kind := strings.ToLower(firstNonEmpty(step.StepType, step.Type))

	// 1. Xử lý Thinking (Suy nghĩ)
	thinkingText := firstNonEmpty(step.ThinkingDelta, step.DeltaThinking)
	if thinkingText != "" {
		return t.emitThinkingDelta(thinkingText)
	}
	if kind == "thinking" || kind == "thought" {
		thought := firstNonEmpty(step.TextDelta, step.Thinking)
		if thought != "" {
			return t.emitThinkingDelta(thought)
		}
	}

	// 2. Xử lý Agent Response (Nội dung văn bản trả lời)
	if kind == "agent_response" {
		t.completeThinkingIfNeeded()
		if step.TextDelta != "" {
			return t.emit(buildTextDeltaMessage(step.TextDelta))
		}
		return nil
	}

	// 3. Xử lý Tool Execution (Thực thi công cụ)
	if kind == "tool" {
		t.completeThinkingIfNeeded()
		return t.handleToolStep(step)
	}

	return nil
}

func (t *Translator) emitThinkingDelta(delta string) error {
	t.mu.Lock()
	if !t.thinkingActive {
		t.thinkingActive = true
		t.thinkingStartedAt = time.Now().UTC()
	}
	t.mu.Unlock()

	style := agentv1.ThinkingStyle_THINKING_STYLE_DEFAULT
	return t.emit(buildThinkingDeltaMessage(delta, style))
}

func (t *Translator) completeThinkingIfNeeded() {
	t.mu.Lock()
	if !t.thinkingActive {
		t.mu.Unlock()
		return
	}
	duration := time.Since(t.thinkingStartedAt)
	t.thinkingActive = false
	t.mu.Unlock()

	_ = t.emit(buildThinkingCompletedMessage(int32(duration.Milliseconds())))
}

func (t *Translator) handleToolStep(step *AgyStepUpdate) error {
	toolID := step.ToolID
	if toolID == "" {
		toolID = fmt.Sprintf("agy-tool-%d", time.Now().UnixNano())
	}
	status := strings.ToLower(step.Status)

	t.mu.Lock()
	existingTool, exists := t.activeTools[toolID]
	t.mu.Unlock()

	if !exists || status == "running" || status == "start" {
		toolCall := t.buildToolCall(step.ToolName, step.Parameters)
		t.mu.Lock()
		t.activeTools[toolID] = toolCall
		t.mu.Unlock()

		return t.emit(buildToolCallStartedMessage(toolID, t.modelCallID, toolCall))
	}

	if status == "success" || status == "completed" || status == "error" || status == "failed" {
		t.mu.Lock()
		delete(t.activeTools, toolID)
		t.mu.Unlock()

		if existingTool == nil {
			existingTool = t.buildToolCall(step.ToolName, step.Parameters)
		}
		return t.emit(buildToolCallCompletedMessage(toolID, t.modelCallID, existingTool))
	}

	return nil
}

func (t *Translator) buildToolCall(toolName string, rawParams json.RawMessage) *agentv1.ToolCall {
	return BuildDetailedToolCall(toolName, rawParams)
}

func (t *Translator) handleResult(result *AgyResult) error {
	t.completeThinkingIfNeeded()

	var inTokens, outTokens, cacheRead, cacheWrite int64
	if result != nil && result.Usage != nil {
		inTokens = result.Usage.InputTokens
		outTokens = result.Usage.OutputTokens
		cacheRead = result.Usage.CacheReadTokens
		cacheWrite = result.Usage.CacheWriteTokens
	}

	return t.emit(buildTurnEndedMessage(inTokens, outTokens, cacheRead, cacheWrite))
}

// Protobuf builders
func buildTextDeltaMessage(text string) *agentv1.AgentServerMessage {
	return &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_InteractionUpdate{
			InteractionUpdate: &agentv1.InteractionUpdate{
				Message: &agentv1.InteractionUpdate_TextDelta{
					TextDelta: &agentv1.TextDeltaUpdate{Text: text},
				},
			},
		},
	}
}

func buildThinkingDeltaMessage(text string, style agentv1.ThinkingStyle) *agentv1.AgentServerMessage {
	styleCopy := style
	return &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_InteractionUpdate{
			InteractionUpdate: &agentv1.InteractionUpdate{
				Message: &agentv1.InteractionUpdate_ThinkingDelta{
					ThinkingDelta: &agentv1.ThinkingDeltaUpdate{
						Text:          text,
						ThinkingStyle: &styleCopy,
					},
				},
			},
		},
	}
}

func buildThinkingCompletedMessage(durationMS int32) *agentv1.AgentServerMessage {
	return &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_InteractionUpdate{
			InteractionUpdate: &agentv1.InteractionUpdate{
				Message: &agentv1.InteractionUpdate_ThinkingCompleted{
					ThinkingCompleted: &agentv1.ThinkingCompletedUpdate{
						ThinkingDurationMs: durationMS,
					},
				},
			},
		},
	}
}

func buildToolCallStartedMessage(callID string, modelCallID string, toolCall *agentv1.ToolCall) *agentv1.AgentServerMessage {
	return &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_InteractionUpdate{
			InteractionUpdate: &agentv1.InteractionUpdate{
				Message: &agentv1.InteractionUpdate_ToolCallStarted{
					ToolCallStarted: &agentv1.ToolCallStartedUpdate{
						CallId:      callID,
						ToolCall:    toolCall,
						ModelCallId: modelCallID,
					},
				},
			},
		},
	}
}

func buildToolCallCompletedMessage(callID string, modelCallID string, toolCall *agentv1.ToolCall) *agentv1.AgentServerMessage {
	return &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_InteractionUpdate{
			InteractionUpdate: &agentv1.InteractionUpdate{
				Message: &agentv1.InteractionUpdate_ToolCallCompleted{
					ToolCallCompleted: &agentv1.ToolCallCompletedUpdate{
						CallId:      callID,
						ToolCall:    toolCall,
						ModelCallId: modelCallID,
					},
				},
			},
		},
	}
}

func buildTurnEndedMessage(inputTokens int64, outputTokens int64, cacheReadTokens int64, cacheWriteTokens int64) *agentv1.AgentServerMessage {
	inputTokensValue := inputTokens
	outputTokensValue := outputTokens
	cacheReadTokensValue := cacheReadTokens
	cacheWriteTokensValue := cacheWriteTokens
	return &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_InteractionUpdate{
			InteractionUpdate: &agentv1.InteractionUpdate{
				Message: &agentv1.InteractionUpdate_TurnEnded{
					TurnEnded: &agentv1.TurnEndedUpdate{
						InputTokens:      &inputTokensValue,
						OutputTokens:     &outputTokensValue,
						CacheReadTokens:  &cacheReadTokensValue,
						CacheWriteTokens: &cacheWriteTokensValue,
					},
				},
			},
		},
	}
}

// Helpers
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func stringValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}