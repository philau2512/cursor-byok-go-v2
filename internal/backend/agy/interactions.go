package agy

import (
	"encoding/json"
	"fmt"
	"strings"
)

// InteractionRequest chứa thông tin câu hỏi hoặc yêu cầu cấp quyền từ AGY.
type InteractionRequest struct {
	ID        string   `json:"id"`
	Type      string   `json:"type"`
	Title     string   `json:"title"`
	Prompt    string   `json:"prompt"`
	Options   []string `json:"options,omitempty"`
	ToolName  string   `json:"tool_name,omitempty"`
	Reason    string   `json:"reason,omitempty"`
}

// ParseInteraction kiểm tra xem step payload có chứa yêu cầu tương tác với người dùng hay không.
func ParseInteraction(rawParams json.RawMessage) *InteractionRequest {
	if len(rawParams) == 0 {
		return nil
	}

	var m map[string]any
	if err := json.Unmarshal(rawParams, &m); err != nil {
		return nil
	}

	raw, ok := m["interaction"]
	if !ok {
		raw = m["request"]
	}
	if raw == nil {
		return nil
	}

	interactionMap, ok := raw.(map[string]any)
	if !ok {
		return nil
	}

	id := getString(interactionMap, "id", "request_id")
	if id == "" {
		return nil
	}

	title := getString(interactionMap, "title")
	if title == "" {
		title = "Antigravity cần phản hồi của bạn"
	}

	kind := getString(interactionMap, "type", "kind")
	prompt := getString(interactionMap, "prompt")

	var options []string
	if optsRaw, ok := interactionMap["options"].([]any); ok {
		for _, o := range optsRaw {
			if s, ok := o.(string); ok && s != "" {
				options = append(options, s)
			}
		}
	}

	return &InteractionRequest{
		ID:       id,
		Type:     kind,
		Title:    title,
		Prompt:   prompt,
		Options:  options,
		ToolName: getString(interactionMap, "tool_name", "tool"),
		Reason:   getString(interactionMap, "reason"),
	}
}

// FormatInteractionAnswer tạo chuỗi văn bản trả lời cho stdin của AGY CLI.
func FormatInteractionAnswer(requestID string, answer string) string {
	return fmt.Sprintf("Answer interaction %s: %s", strings.TrimSpace(requestID), strings.TrimSpace(answer))
}