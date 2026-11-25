package protocol

import (
	"encoding/hex"
	"testing"

	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
)

// mustFrame marshal một AgentClientMessage phục vụ test carrier.
func mustFrame(t *testing.T, message *agentv1.AgentClientMessage) []byte {
	t.Helper()
	payload, err := proto.Marshal(message)
	if err != nil {
		t.Fatalf("marshal frame thất bại: %v", err)
	}
	return payload
}

// TestDecodeAgentClientMessageEnvelopeCarriers chốt hành vi của BidiAppendRequest,
// vì hai ô `data` (hex) và `data_binary` (bytes) cùng chở một frame: client 3.32
// gửi ở data_binary, và khi cả hai cùng có mặt thì data_binary thắng.
func TestDecodeAgentClientMessageEnvelopeCarriers(t *testing.T) {
	heartbeat := mustFrame(t, &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_ClientHeartbeat{ClientHeartbeat: &agentv1.ClientHeartbeat{}},
	})
	runConversationID := "conv-hex"
	runRequest := mustFrame(t, &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: &agentv1.AgentRunRequest{ConversationId: &runConversationID},
		},
	})

	tests := []struct {
		name     string
		hexData  string
		binary   []byte
		wantNil  bool
		wantKind string
		wantErr  bool
	}{
		{
			name:     "chỉ hex như client cũ",
			hexData:  hex.EncodeToString(heartbeat),
			wantKind: "client_heartbeat",
		},
		{
			name:     "chỉ data_binary như client 3.32",
			binary:   heartbeat,
			wantKind: "client_heartbeat",
		},
		{
			name:     "cả hai thì data_binary thắng",
			hexData:  hex.EncodeToString(runRequest),
			binary:   heartbeat,
			wantKind: "client_heartbeat",
		},
		{
			name:    "hai ô đều trống là frame rỗng, không phải lỗi",
			wantNil: true,
		},
		{
			name:    "hex không hợp lệ phải báo lỗi rõ",
			hexData: "xyz-not-hex",
			wantErr: true,
		},
		{
			name:    "binary không parse được phải báo lỗi rõ",
			binary:  []byte{0xFF, 0xFF, 0xFF},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			message, kind, err := DecodeAgentClientMessageEnvelope(tc.hexData, tc.binary)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("mong lỗi decode, nhận được message=%v kind=%q", message, kind)
				}
				return
			}
			if err != nil {
				t.Fatalf("không mong lỗi, nhận được: %v", err)
			}
			if tc.wantNil {
				if message != nil || kind != "" {
					t.Fatalf("frame rỗng phải trả nil/kind trống, nhận được message=%v kind=%q", message, kind)
				}
				return
			}
			if message == nil {
				t.Fatalf("mong message decoded, nhận được nil")
			}
			if kind != tc.wantKind {
				t.Errorf("kind = %q, muốn %q", kind, tc.wantKind)
			}
		})
	}
}

// TestDecodeAgentClientMessageKeepsHexOnlyBehavior bảo đảm hàm cũ vẫn đúng sau
// khi refactor sang envelope (debugger tool vẫn gọi nó).
func TestDecodeAgentClientMessageKeepsHexOnlyBehavior(t *testing.T) {
	heartbeat := mustFrame(t, &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_ClientHeartbeat{ClientHeartbeat: &agentv1.ClientHeartbeat{}},
	})

	message, kind, err := DecodeAgentClientMessage(hex.EncodeToString(heartbeat))
	if err != nil {
		t.Fatalf("decode hex thất bại: %v", err)
	}
	if message == nil || kind != "client_heartbeat" {
		t.Fatalf("message=%v kind=%q, muốn kind client_heartbeat", message, kind)
	}
}
