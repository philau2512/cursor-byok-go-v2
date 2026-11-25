package protocol

import (
	"encoding/hex"
	"testing"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"

	"google.golang.org/protobuf/proto"
)

func TestDecodeAgentClientMessageBytesAndHex(t *testing.T) {
	orig := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_ClientHeartbeat{
			ClientHeartbeat: &agentv1.ClientHeartbeat{},
		},
	}
	raw, err := proto.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	msg, kind, err := DecodeAgentClientMessageBytes(raw)
	if err != nil {
		t.Fatalf("DecodeAgentClientMessageBytes failed: %v", err)
	}
	if kind != "client_heartbeat" || msg == nil {
		t.Fatalf("got kind=%q msg=%v, want client_heartbeat", kind, msg)
	}

	msgHex, kindHex, err := DecodeAgentClientMessage(hex.EncodeToString(raw))
	if err != nil {
		t.Fatalf("DecodeAgentClientMessage failed: %v", err)
	}
	if kindHex != "client_heartbeat" || msgHex == nil {
		t.Fatalf("got kind=%q msg=%v, want client_heartbeat", kindHex, msgHex)
	}
}

func TestMapClientMessageToCommandKindUnknownFieldForwardCompat(t *testing.T) {
	// Field number 99 (wire type 2: (99<<3)|2 = 794 -> varint 0x9a 0x06), len 2, payload 0x08 0x01.
	unknown := []byte{0x9a, 0x06, 0x02, 0x08, 0x01}

	msg, kind, err := DecodeAgentClientMessageBytes(unknown)
	if err != nil {
		t.Fatalf("DecodeAgentClientMessageBytes failed: %v", err)
	}
	if kind != "" {
		t.Fatalf("expected empty kind for unknown branch, got %q", kind)
	}

	cmdKind, err := MapClientMessageToCommandKind(msg, kind)
	if err != nil {
		t.Fatalf("expected forward-compat ack for unknown branch, got error: %v", err)
	}
	if cmdKind != runtimecore.CommandKindClientHeartbeat {
		t.Fatalf("got cmdKind=%q, want %q", cmdKind, runtimecore.CommandKindClientHeartbeat)
	}

	// Empty message without unknown fields must still fail.
	if _, err := MapClientMessageToCommandKind(&agentv1.AgentClientMessage{}, ""); err == nil {
		t.Fatalf("expected error for empty AgentClientMessage")
	}
}
