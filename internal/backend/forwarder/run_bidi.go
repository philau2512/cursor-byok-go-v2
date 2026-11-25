// run_bidi.go chứa đường vào bidi của client protocol mới:
// AgentClientMessage chiều lên và AgentServerMessage chiều xuống dùng chung
// một stream /agent.v1.AgentService/Run, thay cho kênh hai đoạn legacy là
// BidiAppend (lên) + RunSSE (xuống).
package forwarder

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	"cursor/gen/agentv1/agentv1connect"
)

// bidiRunHeartbeatInterval giữ nhịp heartbeat giống legacy RunSSE để client
// vẫn phát hiện được kết nối sống khi không có delta mới.
const bidiRunHeartbeatInterval = 5 * time.Second

// NewAgentRunHandler lắp HTTP handler cho luồng bidi Run.
func NewAgentRunHandler(service *Service, options ...connect.HandlerOption) http.Handler {
	return connect.NewBidiStreamHandler(
		agentv1connect.AgentServiceRunProcedure,
		service.Run,
		options...,
	)
}

// bidiRunEvent là nguồn sự kiện duy nhất mà vòng lặp bidi chờ.
type bidiRunEvent struct {
	Kind    string // "frame" | "read_end"
	Frame   *agentv1.AgentClientMessage
	ReadErr error
}

// bidiRunFlush mô tả kết quả một lần đẩy backlog xuống stream.
type bidiRunFlush struct {
	Sent        int   // số message đã gửi trong lần đẩy này
	Ended       bool  // vòng chạy đã phát sinh terminal, phải kết thúc stream
	TerminalErr error // lỗi tương ứng terminal event, có thể nil
	SendErr     error // gửi thất bại, cần ngắt ngay
}

// Run xử lý /agent.v1.AgentService/Run.
//
// Ràng buộc ngữ nghĩa:
//   - request_id do server sinh cho từng stream; toàn bộ hai chiều của một vòng
//     dùng chung một key nên kết quả exec / interaction / kv tự gắn với
//     run_request, không còn cần append_seqno để chống trùng như legacy.
//   - chiều xuống chỉ gửi trong goroutine này, chiều lên chỉ nhận trong
//     goroutine reader, tránh đọc và ghi đồng thời trên cùng connect stream.
func (service *Service) Run(ctx context.Context, stream *connect.BidiStream[agentv1.AgentClientMessage, agentv1.AgentServerMessage]) error {
	if service == nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("forwarder service is nil"))
	}
	requestID := "agent-run-" + uuid.NewString()
	subscriberID, signal, err := service.broker.Subscribe(requestID)
	if err != nil {
		service.debug.LogRunSSE(ctx, requestID, "", "agent_run_subscribe_error", map[string]any{
			"error": err.Error(),
		})
		return connect.NewError(connect.CodeInternal, err)
	}
	service.debug.LogRunSSE(ctx, requestID, "", "agent_run_open", map[string]any{
		"subscriber_id": subscriberID,
		"procedure":     agentv1connect.AgentServiceRunProcedure,
	})
	defer func() {
		remaining := service.broker.Unsubscribe(requestID, subscriberID)
		service.debug.LogRunSSE(context.Background(), requestID, "", "agent_run_close", map[string]any{
			"subscriber_id":         subscriberID,
			"remaining_subscribers": remaining,
		})
		if remaining == 0 {
			// Giống legacy RunSSE: khi stream đứt transient, cho provider một
			// khoảng ân hạn để reconnect thay vì cancel ngay.
			if !service.scheduleOrphanCancelActor(requestID, "[canceled] Agent Run stream disconnected") {
				service.broker.RemoveIfIdle(requestID)
			}
		}
	}()

	events := make(chan bidiRunEvent, 8)
	go service.readBidiRunFrames(stream, events)

	cursor := 0
	ticker := time.NewTicker(bidiRunHeartbeatInterval)
	defer ticker.Stop()

	for {
		flush := service.flushBidiRunBacklog(ctx, stream, requestID, subscriberID, &cursor)
		if flush.SendErr != nil {
			return flush.SendErr
		}
		if flush.Ended {
			return flush.TerminalErr
		}

		select {
		case <-ctx.Done():
			service.debug.LogRunSSE(ctx, requestID, "", "agent_run_context_done", map[string]any{
				"cursor": cursor,
			})
			_ = service.flushBidiRunBacklog(context.Background(), stream, requestID, subscriberID, &cursor)
			return nil
		case event := <-events:
			switch event.Kind {
			case "frame":
				if resetCursor, frameErr := service.handleBidiRunFrame(ctx, requestID, event.Frame); frameErr != nil {
					return frameErr
				} else if resetCursor {
					cursor = 0
				}
			case "read_end":
				if bidiRunStreamEnded(event.ReadErr) {
					return nil
				}
				service.debug.LogRunSSE(ctx, requestID, "", "agent_run_read_error", map[string]any{
					"error": event.ReadErr.Error(),
				})
				return event.ReadErr
			}
		case <-signal:
			// actor vừa sinh backlog mới, quay lại đầu vòng lặp để đẩy xuống.
		case <-ticker.C:
			if pending, pendingErr := service.broker.ReadFromCursor(requestID, cursor); pendingErr == nil && len(pending) > 0 {
				continue
			}
			heartbeat := buildHeartbeatMessage()
			if err := stream.Send(heartbeat); err != nil {
				service.debug.LogRunSSE(ctx, requestID, "", "agent_run_heartbeat_error", map[string]any{
					"cursor":       cursor,
					"message_case": agentServerMessageCase(heartbeat),
					"error":        err.Error(),
				})
				return err
			}
			service.debug.LogRunSSE(ctx, requestID, "", "agent_run_heartbeat", map[string]any{
				"cursor":       cursor,
				"message_case": agentServerMessageCase(heartbeat),
			})
		}
	}
}

// readBidiRunFrames chuyển frame chiều lên cho vòng lặp chính; báo một lần khi kết thúc.
func (service *Service) readBidiRunFrames(stream *connect.BidiStream[agentv1.AgentClientMessage, agentv1.AgentServerMessage], events chan<- bidiRunEvent) {
	defer close(events)
	for {
		frame, err := stream.Receive()
		if err != nil {
			events <- bidiRunEvent{Kind: "read_end", ReadErr: err}
			return
		}
		if frame == nil {
			continue
		}
		events <- bidiRunEvent{Kind: "frame", Frame: frame}
	}
}

// handleBidiRunFrame chuẩn hoá vàphân phát một frame chiều lên, trả về việc có phải reset cursor.
func (service *Service) handleBidiRunFrame(ctx context.Context, requestID string, frame *agentv1.AgentClientMessage) (bool, error) {
	clientKind := agentClientMessageCase(frame)
	rawHex := ""
	if payload, err := proto.Marshal(frame); err == nil {
		rawHex = hex.EncodeToString(payload)
	}
	intent, err := service.decodeInboundIntent(requestID, frame, clientKind)
	if err != nil {
		service.debug.LogBidiRaw(ctx, requestID, "", 0, rawHex, "decode_error", map[string]any{
			"procedure":   agentv1connect.AgentServiceRunProcedure,
			"client_kind": strings.TrimSpace(clientKind),
			"error":       err.Error(),
		})
		// Không giải được run_request thì cả vòng không bắt đầu được, phải ngắt
		// stream để client biết; các frame khác (heartbeat, kết quả trả về) thì
		// bỏ qua có dung sai để không phá provider loop đang chạy.
		if strings.TrimSpace(clientKind) == "run_request" || strings.TrimSpace(clientKind) == "prewarm_request" {
			return false, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return false, nil
	}
	service.debug.LogBidiRaw(ctx, requestID, intent.ConversationID, 0, rawHex, "accepted", map[string]any{
		"procedure":   agentv1connect.AgentServiceRunProcedure,
		"client_kind": strings.TrimSpace(clientKind),
	})
	service.debug.LogBidiDecoded(ctx, requestID, intent.ConversationID, 0, clientKind, frame, intent, nil)
	if queueMessageFields, queued := queuedUserMessageDebugFields(clientKind, intent); queued {
		service.debug.LogRuntime(ctx, requestID, intent.ConversationID, "queued_user_message_received", queueMessageFields)
	}
	if err := service.dispatchInboundIntent(intent); err != nil {
		if shouldAcknowledgeInterruptedInboundIntent(intent, err) {
			service.debug.LogRuntime(ctx, requestID, intent.ConversationID, "dispatch_interrupted_ignored", map[string]any{
				"channel": "agent_run",
				"kind":    strings.TrimSpace(intent.Kind),
				"error":   err.Error(),
			})
			return intent.StartsRun, nil
		}
		service.debug.LogRuntime(ctx, requestID, intent.ConversationID, "dispatch_error", map[string]any{
			"channel": "agent_run",
			"kind":    strings.TrimSpace(intent.Kind),
			"error":   err.Error(),
		})
		if strings.TrimSpace(intent.Kind) == "run" {
			return false, connect.NewError(connect.CodeInternal, err)
		}
		return false, nil
	}
	service.debug.LogRuntime(ctx, requestID, intent.ConversationID, "inbound_intent_dispatched", map[string]any{
		"channel":         "agent_run",
		"kind":            strings.TrimSpace(intent.Kind),
		"thinking_effort": strings.TrimSpace(intent.ThinkingEffort),
		"prewarm":         intent.Prewarm,
		"ignored_reason":  strings.TrimSpace(intent.IgnoredReason),
	})
	if queueMessageFields, queued := queuedUserMessageDebugFields(clientKind, intent); queued {
		service.debug.LogRuntime(ctx, requestID, intent.ConversationID, "queued_user_message_dispatched", queueMessageFields)
	}
	// OpenStream xoá sạch backlog và đặt lại subscriber cursor về 0, nên cursor
	// cục bộ phải reset theo; nếu không sẽ bỏ qua vài message đầu của vòng mới.
	return intent.StartsRun, nil
}

// flushBidiRunBacklog đẩy các message đã sinh trong backlog xuống stream hiện tại.
func (service *Service) flushBidiRunBacklog(
	ctx context.Context,
	stream *connect.BidiStream[agentv1.AgentClientMessage, agentv1.AgentServerMessage],
	requestID string,
	subscriberID string,
	cursor *int,
) bidiRunFlush {
	events, err := service.broker.ReadFromCursor(requestID, *cursor)
	if err != nil {
		service.debug.LogRunSSE(ctx, requestID, "", "agent_run_read_backlog_error", map[string]any{
			"cursor": *cursor,
			"error":  err.Error(),
		})
		return bidiRunFlush{}
	}
	flush := bidiRunFlush{}
	for _, event := range events {
		if event.Message != nil {
			if err := stream.Send(event.Message); err != nil {
				service.logRunSSEMessage(ctx, requestID, "send_error", *cursor, event.Message, err)
				flush.SendErr = err
				return flush
			}
			flush.Sent++
			service.logRunSSEMessage(ctx, requestID, "send_message", *cursor, event.Message, nil)
		}
		*cursor++
		_ = service.broker.AcknowledgeCursor(requestID, subscriberID, *cursor)
		if event.End {
			service.debug.LogRunSSE(ctx, requestID, "", "agent_run_terminal", map[string]any{
				"cursor":                 *cursor,
				"terminal_error_code":    strings.TrimSpace(event.TerminalErrorCode),
				"terminal_error_message": strings.TrimSpace(event.TerminalErrorMessage),
			})
			flush.Ended = true
			flush.TerminalErr = buildTerminalStreamError(event)
			return flush
		}
	}
	return flush
}

// bidiRunStreamEnded xác định việc kết thúc chiều lên có phải đóng bình thường.
func bidiRunStreamEnded(err error) bool {
	if err == nil || errors.Is(err, io.EOF) {
		return true
	}
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		switch connectErr.Code() {
		case connect.CodeCanceled, connect.CodeDeadlineExceeded:
			return true
		}
	}
	return false
}
