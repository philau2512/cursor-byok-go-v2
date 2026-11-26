package forwarder

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
)

// appendBidiFrame gửi một frame AgentClientMessage vào đường legacy BidiAppend,
// chọn carrier theo yêu cầu để tái hiện đúng cách client 3.32 đóng gói payload
// (ô data hex hay ô data_binary nhị phân).
func appendBidiFrame(t *testing.T, service *Service, requestID string, message *agentv1.AgentClientMessage, useBinary bool) (*connect.Response[aiserverv1.BidiAppendResponse], error) {
	t.Helper()
	payload, err := proto.Marshal(message)
	if err != nil {
		t.Fatalf("marshal client message failed: %v", err)
	}
	return appendBidiFramePayload(t, service, requestID, payload, useBinary)
}

func appendBidiFramePayload(t *testing.T, service *Service, requestID string, payload []byte, useBinary bool) (*connect.Response[aiserverv1.BidiAppendResponse], error) {
	t.Helper()
	req := &aiserverv1.BidiAppendRequest{
		RequestId:   &aiserverv1.BidiRequestId{RequestId: requestID},
		AppendSeqno: 1,
	}
	if useBinary {
		req.DataBinary = payload
	} else {
		req.Data = hex.EncodeToString(payload)
	}
	return service.BidiAppend(context.Background(), connect.NewRequest(req))
}

// requireNotRejectedAsUnknownKind kiểm tra đúng lớp lỗi đã làm client 3.32 chết:
// frame bị coi là kind không xác định. Lỗi khác (thiếu model channel, dispatch)
// không liên quan nên bỏ qua.
func requireNotRejectedAsUnknownKind(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), "unsupported client message kind") {
		t.Fatalf("frame bị từ chối vì kind không xác định: %v", err)
	}
}

// TestBidiAppendReadsBinaryPayloadCarrier chứng minh frame chỉ đặt payload trong
// data_binary (client 3.32 làm vậy) vẫn được phân loại, thay vì trả rỗng rồi
// rơi vào "unsupported client message kind: ".
func TestBidiAppendReadsBinaryPayloadCarrier(t *testing.T) {
	service := newBidiRunTestService(t)
	frame := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_ClientHeartbeat{
			ClientHeartbeat: &agentv1.ClientHeartbeat{},
		},
	}

	_, err := appendBidiFrame(t, service, "req-binary-heartbeat", frame, true)
	requireNotRejectedAsUnknownKind(t, err)
}

// TestBidiAppendIgnoresUnrecognizedClientMessageBranch chứng minh frame ở nhánh
// oneof mà bản proto này chưa biết chỉ bị ack rồi bỏ qua: nó là frame quản trị
// phiên, cắt nó sẽ giết luôn run đang chạy.
func TestBidiAppendIgnoresUnrecognizedClientMessageBranch(t *testing.T) {
	service := newBidiRunTestService(t)
	// field 9 (wire type 2) chưa tồn tại trong AgentClientMessage => unknown field.
	unknownBranch := []byte{0x4A, 0x01, 0x08}

	resp, err := appendBidiFramePayload(t, service, "req-unknown-branch", unknownBranch, true)
	if err != nil {
		t.Fatalf("frame không nhận diện phải được ack, nhận được lỗi: %v", err)
	}
	if resp == nil || resp.Msg == nil {
		t.Fatalf("ack thiếu response rỗng")
	}
}

// TestBidiAppendIgnoresEmptyPayload carrier rỗng cả hai ô cũng không được phép
// cắt run: đó là frame flush/keep-alive, không phải frame khởi động.
func TestBidiAppendIgnoresEmptyPayload(t *testing.T) {
	service := newBidiRunTestService(t)
	req := connect.NewRequest(&aiserverv1.BidiAppendRequest{
		RequestId:   &aiserverv1.BidiRequestId{RequestId: "req-empty-payload"},
		AppendSeqno: 1,
	})

	resp, err := service.BidiAppend(context.Background(), req)
	if err != nil {
		t.Fatalf("frame rỗng phải được ack, nhận được lỗi: %v", err)
	}
	if resp == nil || resp.Msg == nil {
		t.Fatalf("ack thiếu response rỗng")
	}
}

// TestBidiAppendStillRejectsBrokenRunRequest giữ lại phía ngược lại của dung sai:
// frame mang run mà không dùng được thì phải báo lỗi thật để client thấy, không
// được âm thầm ack.
func TestBidiAppendStillRejectsBrokenRunRequest(t *testing.T) {
	service := newBidiRunTestService(t)
	frame := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: &agentv1.AgentRunRequest{},
		},
	}

	_, err := appendBidiFrame(t, service, "req-broken-run", frame, true)
	if err == nil {
		t.Fatalf("run_request thiếu conversation_id phải bị từ chối")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		t.Fatalf("lỗi phải là connect.Error, nhận được: %v", err)
	}
	if connectErr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("mong code InvalidArgument, nhận được %v: %v", connectErr.Code(), err)
	}
	if !strings.Contains(err.Error(), "conversation_id") {
		t.Fatalf("lỗi phải nói thiếu conversation_id, nhận được: %v", err)
	}
}
