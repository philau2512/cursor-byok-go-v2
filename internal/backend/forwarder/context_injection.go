// context_injection.go xử lý agent.v1.InjectContextAction: client chèn thêm
// context vào GIỮA một run đang chạy (ô expected_run_id trỏ tới run hiện tại).
//
// Lịch sử mà model nhìn thấy chỉ được phép append để giữ prefix cache, nên cách
// port an toàn là biến payload injection thành prompt-context entry — đúng cơ
// chế mà mode_change giữa run đang dùng — chứ không sửa nội dung history đã gửi.
package forwarder

import (
	"context"
	"strings"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
)

const (
	injectContextUserSource   = "injected_user_context"
	injectContextSystemSource = "injected_system_context"
)

// conversationActionInjectContext lấy payload inject_context từ một
// ConversationAction, trả nil nếu action không phải injection.
func conversationActionInjectContext(action *agentv1.ConversationAction) *agentv1.InjectContextAction {
	if action == nil {
		return nil
	}
	item, ok := action.GetAction().(*agentv1.ConversationAction_InjectContextAction)
	if !ok {
		return nil
	}
	return item.InjectContextAction
}

// injectContextMetadataEntries sinh phần lịch sử cần append cho một injection.
// Trả nil khi payload không có nội dung dùng được, để caller phân biệt "không có
// gì để ghi" với "có injection nhưng rỗng".
func injectContextMetadataEntries(turnSeq int64, requestID string, injection *agentv1.InjectContextAction) []HistoryEntry {
	if injection == nil {
		return nil
	}
	var entries []HistoryEntry
	if content := formatInjectedSystemContext(injection.GetSystemContext()); content != "" {
		entries = append(entries, newInjectContextEntry(turnSeq, requestID, injectContextSystemSource, content))
	}
	if text := userMessageText(injection.GetUserContext().GetUserMessage()); text != "" {
		entries = append(entries, newInjectContextEntry(turnSeq, requestID, injectContextUserSource, text))
	}
	return entries
}

// newInjectContextEntry bọc context được inject thành system reminder, để model
// xem nó là thông tin phiên chứ không phải lời user mới.
func newInjectContextEntry(turnSeq int64, requestID string, source string, content string) HistoryEntry {
	return newPromptContextEntry(turnSeq, requestID, newPromptContextMessage(
		source,
		modeladapter.Message{Role: "user", Content: wrapSystemReminder(sourceHeading(source) + "\n" + content)},
		true,
	))
}

func sourceHeading(source string) string {
	if source == injectContextSystemSource {
		return "Context injected into the running turn:"
	}
	return "Additional context from the user for the running turn:"
}

// formatInjectedSystemContext dựng nội dung từ SystemContextInjection (producer +
// content), trả chuỗi trống khi không có gì để nói với model.
func formatInjectedSystemContext(system *agentv1.SystemContextInjection) string {
	if system == nil {
		return ""
	}
	content := strings.TrimSpace(system.GetContent())
	if content == "" {
		return ""
	}
	if producer := strings.TrimSpace(system.GetProducer()); producer != "" {
		return "from " + producer + "\n" + content
	}
	return content
}

// injectContextDebugFields mô tả một injection cho debug log, bao gồm cả những
// phần bản này chưa áp dụng (request_context của user_context) để lần điều tra
// sau không phải đoán.
func injectContextDebugFields(injection *agentv1.InjectContextAction) map[string]any {
	if injection == nil {
		return nil
	}
	fields := map[string]any{
		"injection_id":    strings.TrimSpace(injection.GetInjectionId()),
		"expected_run_id": strings.TrimSpace(injection.GetExpectedRunId()),
	}
	if user := injection.GetUserContext(); user != nil {
		text := userMessageText(user.GetUserMessage())
		fields["payload"] = "user_context"
		fields["user_message_bytes"] = len(text)
		fields["user_message_text_present"] = text != ""
		fields["request_context_present"] = user.GetRequestContext() != nil
	} else if system := injection.GetSystemContext(); system != nil {
		fields["payload"] = "system_context"
		fields["producer"] = strings.TrimSpace(system.GetProducer())
		fields["content_bytes"] = len(strings.TrimSpace(system.GetContent()))
	} else {
		fields["payload"] = "empty"
	}
	return fields
}

// logInjectContextDropped ghi lại một injection bị bỏ qua vì không còn run đang
// hoạt động để nhận nó. Im lặng ở đây nghĩa là model không bao giờ thấy phần
// context user vừa thêm, nên lần điều tra sau chỉ cần đọc log là đủ.
func (service *Service) logInjectContextDropped(intent InboundIntent, reason string) {
	if service == nil || intent.InjectContext == nil {
		return
	}
	fields := injectContextDebugFields(intent.InjectContext)
	fields["reason"] = strings.TrimSpace(reason)
	service.debug.LogRuntime(context.Background(), intent.RequestID, intent.ConversationID, "inject_context_dropped", fields)
}
