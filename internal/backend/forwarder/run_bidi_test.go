package forwarder

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"cursor/gen/agentv1"
	"cursor/gen/agentv1/agentv1connect"
	legacyruntime "cursor/internal/runtime"
)

// failingChannelResolver giả lập môi trường chưa cấu hình model channel,
// để đường bidi đi tới nhánh terminal lỗi thay vì gọi provider thật.
type failingChannelResolver struct{}

func (failingChannelResolver) SelectChannelForModel(context.Context, string) (*legacyruntime.ResolvedChannel, error) {
	return nil, errors.New("no model channel configured")
}

func (failingChannelResolver) ProviderStreamIdleTimeout(context.Context) time.Duration {
	return 200 * time.Millisecond
}

// newBidiRunTestService dùng temp dir tự quản lý: actor của provider pass
// ghi history bất đồng bộ, nên t.TempDir() sẽ fail trên Windows khi dọn
// thư mục "not empty".
func newBidiRunTestService(t *testing.T) *Service {
	t.Helper()
	historyRoot, err := os.MkdirTemp("", "forwarder-agent-run-")
	if err != nil {
		t.Fatalf("create history root failed: %v", err)
	}
	service := NewService(historyRoot, failingChannelResolver{})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = service.Close(ctx)
		_ = os.RemoveAll(historyRoot)
	})
	return service
}

// newBidiRunTestClient dựng server h2c thật và connect client bidi,
// để test đi đúng đường frame trên wire thay vì gọi hàm nội bộ.
func newBidiRunTestClient(t *testing.T, service *Service) agentv1connect.AgentServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(agentv1connect.AgentServiceRunProcedure, NewAgentRunHandler(service))
	server := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(server.Close)

	httpClient := &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(dialCtx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(dialCtx, network, addr)
		},
	}}
	return agentv1connect.NewAgentServiceClient(httpClient, server.URL)
}

func bidiRunFrame(conversationID string, text string) *agentv1.AgentClientMessage {
	identity := conversationID
	return &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: &agentv1.AgentRunRequest{
				ConversationId: &identity,
				Action: &agentv1.ConversationAction{
					Action: &agentv1.ConversationAction_UserMessageAction{
						UserMessageAction: &agentv1.UserMessageAction{
							UserMessage: &agentv1.UserMessage{
								MessageId: "msg-bidi-1",
								Text:      text,
							},
						},
					},
				},
			},
		},
	}
}

func isHeartbeatFrame(message *agentv1.AgentServerMessage) bool {
	if message == nil {
		return false
	}
	update := message.GetInteractionUpdate()
	if update == nil {
		return false
	}
	_, ok := update.GetMessage().(*agentv1.InteractionUpdate_Heartbeat)
	return ok
}

// TestAgentRunBidiHandlesEmptyStreamOnlyClose chứng minh vòng lặp Run không
// deadlock khi client chỉ mở stream rồi half-close: handler phải trả về và
// client nhận io.EOF.
func TestAgentRunBidiHandlesEmptyStreamOnlyClose(t *testing.T) {
	service := newBidiRunTestService(t)
	client := newBidiRunTestClient(t, service)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream := client.Run(ctx)
	if err := stream.CloseRequest(); err != nil {
		t.Fatalf("CloseRequest failed: %v", err)
	}
	message, err := stream.Receive()
	if err == nil {
		t.Fatalf("expected stream to end, got message %#v", message)
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF after client half-close, got %v", err)
	}
}

// TestAgentRunBidiDispatchesRunRequest gửi một run_request thật qua stream.
// Kỳ vọng: actor nhận intent, provider fail vì thiếu channel, và client nhận
// được tín hiệu *không phải heartbeat* (message hoặc terminal error).
// Nếu plumbing gãy, test sẽ treo tới timeout thay vì fail sớm.
func TestAgentRunBidiDispatchesRunRequest(t *testing.T) {
	service := newBidiRunTestService(t)
	client := newBidiRunTestClient(t, service)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	stream := client.Run(ctx)
	if err := stream.Send(bidiRunFrame("conv-bidi-run", "hello via agent run")); err != nil {
		t.Fatalf("Send run_request failed: %v", err)
	}
	if err := stream.CloseRequest(); err != nil {
		t.Fatalf("CloseRequest failed: %v", err)
	}

	sawNonHeartbeat := false
	for {
		message, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var connectErr *connect.Error
			if !errors.As(err, &connectErr) {
				t.Fatalf("Receive returned non-connect error: %v", err)
			}
			sawNonHeartbeat = true
			break
		}
		if !isHeartbeatFrame(message) {
			sawNonHeartbeat = true
		}
	}
	if !sawNonHeartbeat {
		t.Fatal("agent run stream produced only heartbeats; run_request was not dispatched end-to-end")
	}
}

// TestHandleBidiRunFrameCursorContract khớp yêu cầu nghiệp vụ:
// OpenStream reset backlog nên chỉ intent bắt đầu vòng mới được trả resetCursor.
func TestHandleBidiRunFrameCursorContract(t *testing.T) {
	service := newBidiRunTestService(t)
	ctx := context.Background()

	t.Run("run request resets cursor", func(t *testing.T) {
		resetCursor, err := service.handleBidiRunFrame(ctx, "req-run", bidiRunFrame("conv-cursor-contract", "please reset"))
		if err != nil {
			t.Fatalf("handleBidiRunFrame returned error: %v", err)
		}
		if !resetCursor {
			t.Fatal("expected cursor reset for a run_request frame")
		}
	})

	t.Run("heartbeat does not reset cursor", func(t *testing.T) {
		heartbeat := &agentv1.AgentClientMessage{
			Message: &agentv1.AgentClientMessage_ClientHeartbeat{
				ClientHeartbeat: &agentv1.ClientHeartbeat{},
			},
		}
		resetCursor, err := service.handleBidiRunFrame(ctx, "req-heartbeat", heartbeat)
		if err != nil {
			t.Fatalf("heartbeat frame must be tolerated, got error: %v", err)
		}
		if resetCursor {
			t.Fatal("expected no cursor reset for a client_heartbeat frame")
		}
	})

	t.Run("run request without conversation id is fatal", func(t *testing.T) {
		_, err := service.handleBidiRunFrame(ctx, "req-invalid", bidiRunFrame("", "missing conversation"))
		if err == nil {
			t.Fatal("expected invalid run_request to abort the stream")
		}
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("expected invalid argument, got %v", err)
		}
	})
}

// TestBidiRunStreamEndedClassification định nghĩa "kết thúc bình thường"
// để handler không báo lỗi oan khi client đổi tab hoặc đóng window.
func TestBidiRunStreamEndedClassification(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		expected bool
	}{
		{name: "nil", err: nil, expected: true},
		{name: "eof", err: io.EOF, expected: true},
		{name: "wrapped eof", err: connect.NewError(connect.CodeUnknown, io.EOF), expected: true},
		{name: "canceled", err: connect.NewError(connect.CodeCanceled, errors.New("gone")), expected: true},
		{name: "deadline", err: connect.NewError(connect.CodeDeadlineExceeded, errors.New("late")), expected: true},
		{name: "invalid argument", err: connect.NewError(connect.CodeInvalidArgument, errors.New("bad")), expected: false},
		{name: "plain error", err: errors.New("boom"), expected: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := bidiRunStreamEnded(testCase.err); got != testCase.expected {
				t.Fatalf("bidiRunStreamEnded(%v) = %v, want %v", testCase.err, got, testCase.expected)
			}
		})
	}
}
