package forwarder

import (
	"encoding/json"
	"strings"
	"testing"

	"cursor/gen/agentv1"
)

// newInjectContextFrame dựng một frame AgentClientMessage mang
// conversation_action{inject_context_action} để test đi đúng đường decode thật.
func newInjectContextFrame(injection *agentv1.InjectContextAction) *agentv1.AgentClientMessage {
	return &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_ConversationAction{
			ConversationAction: &agentv1.ConversationAction{
				Action: &agentv1.ConversationAction_InjectContextAction{InjectContextAction: injection},
			},
		},
	}
}

// promptContextPayloadOf đọc lại payload của một entry prompt_context.
func promptContextPayloadOf(t *testing.T, entry HistoryEntry) struct {
	Source  string `json:"source"`
	Role    string `json:"role"`
	Content string `json:"content"`
} {
	t.Helper()
	if entry.Kind != "prompt_context" {
		t.Fatalf("kind = %q, muốn prompt_context", entry.Kind)
	}
	var payload struct {
		Source  string `json:"source"`
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		t.Fatalf("decode payload thất bại: %v", err)
	}
	return payload
}

// TestConversationActionInjectContextOnlyMatchesInjection bảo đảm helper chỉ lấy
// đúng action injection, không bắt nhầm action khác.
func TestConversationActionInjectContextOnlyMatchesInjection(t *testing.T) {
	injection := &agentv1.InjectContextAction{InjectionId: "inj-1"}
	injectFrame := newInjectContextFrame(injection)
	if got := conversationActionInjectContext(injectFrame.GetConversationAction()); got != injection {
		t.Fatalf("không lấy được injection, nhận được %v", got)
	}

	userFrame := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_ConversationAction{
			ConversationAction: &agentv1.ConversationAction{
				Action: &agentv1.ConversationAction_UserMessageAction{
					UserMessageAction: &agentv1.UserMessageAction{},
				},
			},
		},
	}
	if got := conversationActionInjectContext(userFrame.GetConversationAction()); got != nil {
		t.Fatalf("action khác không được thành injection, nhận được %v", got)
	}
	if got := conversationActionInjectContext(nil); got != nil {
		t.Fatalf("action nil phải trả nil, nhận được %v", got)
	}
}

// TestInjectContextMetadataEntriesFromSystemContext chứng minh system_context
// (producer + content) đi tới model dưới dạng reminder được append, không sửa
// history đã gửi.
func TestInjectContextMetadataEntriesFromSystemContext(t *testing.T) {
	injection := &agentv1.InjectContextAction{
		InjectionId:   "inj-sys",
		ExpectedRunId: "req-1",
		Payload: &agentv1.InjectContextAction_SystemContext{
			SystemContext: &agentv1.SystemContextInjection{Producer: "goal-engine", Content: "goal còn 2 bước"},
		},
	}

	entries := injectContextMetadataEntries(7, "req-1", injection)
	if len(entries) != 1 {
		t.Fatalf("entry = %d, muốn 1", len(entries))
	}
	payload := promptContextPayloadOf(t, entries[0])
	if payload.Source != injectContextSystemSource {
		t.Errorf("source = %q, muốn %q", payload.Source, injectContextSystemSource)
	}
	if !strings.Contains(payload.Content, "goal-engine") || !strings.Contains(payload.Content, "goal còn 2 bước") {
		t.Errorf("content thiếu producer hoặc nội dung: %q", payload.Content)
	}
	if !strings.HasPrefix(payload.Content, "<system_reminder>") {
		t.Errorf("content phải được bọc system_reminder: %q", payload.Content)
	}
	if entries[0].TurnSeq != 7 || entries[0].RequestID != "req-1" {
		t.Errorf("turn_seq/request_id = %d/%q, muốn 7/req-1", entries[0].TurnSeq, entries[0].RequestID)
	}
}

// TestInjectContextMetadataEntriesFromUserContext chốt nhánh user_context: phần
// user thêm giữa run thành context chú ý, không thành lời user mới trong history.
func TestInjectContextMetadataEntriesFromUserContext(t *testing.T) {
	injection := &agentv1.InjectContextAction{
		InjectionId: "inj-user",
		Payload: &agentv1.InjectContextAction_UserContext{
			UserContext: &agentv1.UserContextInjection{
				UserMessage: &agentv1.UserMessage{Text: "thêm file đăng nhập vào"},
			},
		},
	}

	entries := injectContextMetadataEntries(3, "req-2", injection)
	if len(entries) != 1 {
		t.Fatalf("entry = %d, muốn 1", len(entries))
	}
	payload := promptContextPayloadOf(t, entries[0])
	if payload.Source != injectContextUserSource {
		t.Errorf("source = %q, muốn %q", payload.Source, injectContextUserSource)
	}
	if !strings.Contains(payload.Content, "thêm file đăng nhập vào") {
		t.Errorf("content thiếu text user: %q", payload.Content)
	}
}

// TestInjectContextMetadataEntriesIgnoresBlankPayloads chứng minh payload rỗng
// không sinh entry rác (log phía actor sẽ báo event inject_context_empty).
func TestInjectContextMetadataEntriesIgnoresBlankPayloads(t *testing.T) {
	tests := []struct {
		name      string
		injection *agentv1.InjectContextAction
	}{
		{name: "nil injection", injection: nil},
		{
			name:      "payload trống",
			injection: &agentv1.InjectContextAction{InjectionId: "inj-empty"},
		},
		{
			name: "system context content trống",
			injection: &agentv1.InjectContextAction{
				Payload: &agentv1.InjectContextAction_SystemContext{
					SystemContext: &agentv1.SystemContextInjection{Producer: "goal-engine", Content: "   "},
				},
			},
		},
		{
			name: "user context text trống",
			injection: &agentv1.InjectContextAction{
				Payload: &agentv1.InjectContextAction_UserContext{
					UserContext: &agentv1.UserContextInjection{UserMessage: &agentv1.UserMessage{Text: ""}},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := injectContextMetadataEntries(1, "req-x", tc.injection); len(got) != 0 {
				t.Fatalf("payload rỗng không được sinh entry, nhận được %d", len(got))
			}
		})
	}
}

// TestDecodeInboundIntentCarriesInjectContext là bằng chứng đầu-cuối của lớp lỗi
// từng im lặng: frame injection phải được decode ra field riêng và KHÔNG được
// khởi động run (nó dành cho run đang chạy).
func TestDecodeInboundIntentCarriesInjectContext(t *testing.T) {
	service := newBidiRunTestService(t)
	injection := &agentv1.InjectContextAction{
		InjectionId:   "inj-decode",
		ExpectedRunId: "req-decode",
		Payload: &agentv1.InjectContextAction_SystemContext{
			SystemContext: &agentv1.SystemContextInjection{Producer: "goal-engine", Content: "continuing goal"},
		},
	}

	intent, err := service.decodeInboundIntent("req-decode", newInjectContextFrame(injection), "conversation_action")
	if err != nil {
		t.Fatalf("decode injection không được lỗi: %v", err)
	}
	if intent.InjectContext != injection {
		t.Fatalf("intent.InjectContext = %v, muốn đúng payload injection", intent.InjectContext)
	}
	if intent.StartsRun {
		t.Errorf("injection không được khởi động run")
	}
	if intent.Kind != "metadata" {
		t.Errorf("kind = %q, muốn metadata", intent.Kind)
	}

	fields := injectContextDebugFields(intent.InjectContext)
	if fields["payload"] != "system_context" || fields["injection_id"] != "inj-decode" {
		t.Errorf("debug fields thiếu dấu vết: %+v", fields)
	}
}
