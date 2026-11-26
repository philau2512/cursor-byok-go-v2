package backend

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"cursor/internal/backend/server"
)

// TestAgentServiceFallbackActionMatchesProtocol chứng minh fallback trả thân
// đúng theo từng kiểu giao thức Connect, vì client sẽ unmarshal thân đó thành
// message response của chính procedure: unary proto thì body rỗng (= message zero
// value), unary JSON thì "{}", còn streaming thì envelope kết thúc luồng.
func TestAgentServiceFallbackActionMatchesProtocol(t *testing.T) {
	handler := agentServiceFallbackAction()

	tests := []struct {
		name            string
		requestedType   string
		wantContentType string
		wantBody        []byte
	}{
		{
			name:            "unary proto",
			requestedType:   "application/proto",
			wantContentType: "application/proto",
			wantBody:        nil,
		},
		{
			name:            "unary json",
			requestedType:   "application/json",
			wantContentType: "application/json",
			wantBody:        []byte("{}"),
		},
		{
			name:            "connect stream proto",
			requestedType:   "application/connect+proto",
			wantContentType: "application/connect+proto",
			wantBody:        []byte{0x02, 0x00, 0x00, 0x00, 0x02, '{', '}'},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/agent.v1.AgentService/WarmUp", nil)
			req.Header.Set("content-type", tc.requestedType)
			if err := handler(&server.Context{Request: req, Writer: rec}); err != nil {
				t.Fatalf("fallback handler error: %v", err)
			}

			res := rec.Result()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, muốn 200 (không được 404 nữa)", res.StatusCode)
			}
			if got := res.Header.Get("content-type"); got != tc.wantContentType {
				t.Errorf("content-type = %q, muốn %q", got, tc.wantContentType)
			}
			if got := res.Header.Get("connect-protocol-version"); got != "1" {
				t.Errorf("connect-protocol-version = %q, muốn 1", got)
			}
			if !bytes.Equal(rec.Body.Bytes(), tc.wantBody) {
				t.Errorf("body = %v, muốn %v", rec.Body.Bytes(), tc.wantBody)
			}
		})
	}
}

// TestAgentServiceFallbackRoutePrecedence giữ lại điều kiện sống còn của wildcard:
// nó chỉ được bắt procedure chưa mount, tuyệt đối không được chặn đường các
// procedure đã có handler (Run / RunSSE / BidiAppend...).
func TestAgentServiceFallbackRoutePrecedence(t *testing.T) {
	mounted := false
	mux := server.New(
		server.POST("/agent.v1.AgentService/NameAgent",
			server.Name("agent_name_agent"),
			server.HTTP(),
			server.Local(func(ctx *server.Context) error {
				mounted = true
				ctx.Writer.WriteHeader(http.StatusTeapot)
				return nil
			}),
		),
		server.Any("/agent.v1.AgentService/*",
			server.Name("agent_service_fallback"),
			server.HTTP(),
			server.Local(agentServiceFallbackAction()),
		),
	)

	t.Run("procedure đã mount không bị fallback nuốt", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/agent.v1.AgentService/NameAgent", nil))
		if !mounted {
			t.Fatalf("wildcard đã che mất route đã mount (status=%d)", rec.Code)
		}
		if rec.Code != http.StatusTeapot {
			t.Fatalf("status = %d, muốn %d", rec.Code, http.StatusTeapot)
		}
	})

	t.Run("procedure lạ được fallback trả 200", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/agent.v1.AgentService/GetUsableModels", nil)
		req.Header.Set("content-type", "application/proto")
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, muốn 200", rec.Code)
		}
		if version := rec.Header().Get("connect-protocol-version"); version != "1" {
			t.Fatalf("connect-protocol-version = %q, muốn 1", version)
		}
	})
}
