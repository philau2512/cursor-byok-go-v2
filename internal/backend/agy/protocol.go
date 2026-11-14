package agy

import (
	"encoding/json"
)

// AgyEvent đại diện cho một gói tin JSON nhận từ stdout của AGY CLI.
type AgyEvent struct {
	Event          string         `json:"event"`
	ConversationID string         `json:"conversation_id,omitempty"`
	StepUpdate     *AgyStepUpdate `json:"step_update,omitempty"`
	Result         *AgyResult     `json:"result,omitempty"`
}

// AgyStepUpdate chứa thông tin chi tiết của một bước thực thi từ AGY.
type AgyStepUpdate struct {
	StepType        string          `json:"step_type"`
	Type            string          `json:"type,omitempty"`
	StepIndex       any             `json:"step_index,omitempty"`
	ThinkingDelta   string          `json:"thinking_delta,omitempty"`
	DeltaThinking   string          `json:"delta_thinking,omitempty"`
	Thinking        string          `json:"thinking,omitempty"`
	TextDelta       string          `json:"text_delta,omitempty"`
	DurationSeconds float64         `json:"duration_seconds,omitempty"`
	ToolID          string          `json:"tool_id,omitempty"`
	ToolName        string          `json:"tool_name,omitempty"`
	Status          string          `json:"status,omitempty"`
	Parameters      json.RawMessage `json:"parameters,omitempty"`
	Output          string          `json:"output,omitempty"`
	Error           string          `json:"error,omitempty"`
	Usage           *AgyUsage       `json:"usage,omitempty"`
}

// AgyUsage thống kê lượng token sử dụng trong bước hoặc toàn bộ turn.
type AgyUsage struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	ThinkingTokens   int64 `json:"thinking_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

// AgyResult sự kiện kết thúc turn từ AGY CLI.
type AgyResult struct {
	Status       string    `json:"status"`
	Usage        *AgyUsage `json:"usage,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
}