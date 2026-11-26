package forwarder

import (
	"testing"

	"cursor/gen/agentv1"
)

// TestRequestedModelUsesVariantString chốt phạm vi suy luận variant string sau
// khi client 3.32 xóa flag is_variant_string_representation: chỉ nhận token effort
// canonical, không nhận alias, để tên model BYOK có dấu hai chấm không bị cắt nhầm.
func TestRequestedModelUsesVariantString(t *testing.T) {
	tests := []struct {
		name    string
		modelID string
		want    bool
	}{
		{name: "canonical effort colon", modelID: "claude-sonnet:high", want: true},
		{name: "canonical lowercase xhigh", modelID: "gpt-5:xhigh", want: true},
		{name: "canonical disabled", modelID: "gpt-5:MAX", want: true},
		{name: "bracket with content and closing", modelID: "custom[context=356k,reasoning=low]", want: true},
		{name: "alias off is not variant", modelID: "proxy:off", want: false},
		{name: "alias none is not variant", modelID: "vertex:none", want: false},
		{name: "numeric suffix is not variant", modelID: "proxy:0", want: false},
		{name: "namespace suffix is not variant", modelID: "openai:gpt-4o-mini", want: false},
		{name: "unclosed bracket is not variant", modelID: "model[reasoning=high", want: false},
		{name: "leading bracket is plain id", modelID: "[context=356k]", want: false},
		{name: "trailing colon is plain id", modelID: "model:", want: false},
		{name: "no colon no bracket", modelID: "claude-sonnet", want: false},
		{name: "empty", modelID: "", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestedModelUsesVariantString(tc.modelID); got != tc.want {
				t.Errorf("requestedModelUsesVariantString(%q) = %v, muốn %v", tc.modelID, got, tc.want)
			}
		})
	}
}

// TestExtractRequestedModelIDKeepsNonVariantColons là bằng chứng tác động thật:
// những id trên phải đi nguyên vẹn tới provider, nếu không BYOK sẽ gọi sai model.
func TestExtractRequestedModelIDKeepsNonVariantColons(t *testing.T) {
	tests := []struct {
		name       string
		modelID    string
		wantModel  string
		wantEffort string
	}{
		{
			name:      "canonical effort is split",
			modelID:   "claude-sonnet:high",
			wantModel: "claude-sonnet", wantEffort: "high",
		},
		{
			name:      "bracket variant keeps context and effort",
			modelID:   "custom[context=356k,reasoning=low]",
			wantModel: "custom", wantEffort: "low",
		},
		{
			name:      "numeric suffix survives",
			modelID:   "proxy:0",
			wantModel: "proxy:0", wantEffort: "",
		},
		{
			name:      "none suffix survives",
			modelID:   "vertex:none",
			wantModel: "vertex:none", wantEffort: "",
		},
		{
			name:      "namespaced id survives",
			modelID:   "openai:gpt-4o-mini",
			wantModel: "openai:gpt-4o-mini", wantEffort: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			model := &agentv1.RequestedModel{ModelId: tc.modelID}
			if got := extractRequestedModelIDFromRequestedModel(model); got != tc.wantModel {
				t.Errorf("model id = %q, muốn %q", got, tc.wantModel)
			}
			if got := extractRuntimeThinkingEffortFromRequestedModel(model); got != tc.wantEffort {
				t.Errorf("thinking effort = %q, muốn %q", got, tc.wantEffort)
			}
		})
	}
}
