package proto

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestMsgUnknownReturnsError(t *testing.T) {
	md, err := Msg("ThisMessageDoesNotExist")
	if err == nil {
		t.Fatal("expected error for unknown message name")
	}
	if md != nil {
		t.Fatalf("expected nil descriptor, got %v", md)
	}
	if !strings.Contains(err.Error(), "message not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMsgKnownReturnsDescriptor(t *testing.T) {
	md, err := Msg("AgentClientMessage")
	if err != nil {
		t.Fatalf("Msg: %v", err)
	}
	if md == nil {
		t.Fatal("expected non-nil message descriptor")
	}
	if md.Name() != "AgentClientMessage" {
		t.Fatalf("unexpected name: %s", md.Name())
	}
}

func TestAgentFileDescriptorLoads(t *testing.T) {
	fd, err := AgentFileDescriptor()
	if err != nil {
		t.Fatalf("AgentFileDescriptor: %v", err)
	}
	if fd == nil {
		t.Fatal("expected non-nil file descriptor")
	}
}

func TestEncodeHeartbeatSuccess(t *testing.T) {
	b, err := EncodeHeartbeat()
	if err != nil {
		t.Fatalf("EncodeHeartbeat: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("expected non-empty heartbeat payload")
	}
}

func TestEncodeRunRequestSuccess(t *testing.T) {
	params := &RunRequestParams{
		ModelId:        "gpt-test",
		SystemPrompt:   "you are a helper",
		UserText:       "hello",
		MessageId:      "msg-1",
		ConversationId: "conv-1",
	}
	b, err := EncodeRunRequest(params)
	if err != nil {
		t.Fatalf("EncodeRunRequest: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("expected non-empty run request payload")
	}
	if len(params.BlobStore) == 0 {
		t.Fatal("expected blob store to be populated with system prompt")
	}
}

func TestEncodeExecClientMsgUnknownField(t *testing.T) {
	result := dynamicpb.NewMessage(mustMsg(t, "McpResult"))
	_, err := encodeExecClientMsg(1, "exec-1", "this_result_field_does_not_exist", result)
	if err == nil {
		t.Fatal("expected error for unknown ExecClientMessage result field")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEncoderRejectsFieldTypeMismatch(t *testing.T) {
	msg, err := NewMsg("UserMessage")
	if err != nil {
		t.Fatalf("NewMsg: %v", err)
	}

	var e encoder
	e.setUint32(msg, "text", 1)
	if e.err == nil {
		t.Fatal("expected error for incompatible field type")
	}
	if !strings.Contains(e.err.Error(), "must be singular uint32") {
		t.Fatalf("unexpected error: %v", e.err)
	}
}

func TestEncoderRejectsFieldCardinalityMismatch(t *testing.T) {
	msg, err := NewMsg("UserMessage")
	if err != nil {
		t.Fatalf("NewMsg: %v", err)
	}

	var e encoder
	e.appendBytes(msg, "text", []byte("value"))
	if e.err == nil {
		t.Fatal("expected error for incompatible field cardinality")
	}
	if !strings.Contains(e.err.Error(), "must be repeated bytes") {
		t.Fatalf("unexpected error: %v", e.err)
	}
}

func TestEncodeInteractionQueryResponseSuccess(t *testing.T) {
	b, err := EncodeInteractionQueryResponse(42)
	if err != nil {
		t.Fatalf("EncodeInteractionQueryResponse: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("expected non-empty interaction response payload")
	}
	// Outer wire shape: AgentClientMessage{6: InteractionResponse{...}}
	num, typ, _ := protowire.ConsumeTag(b)
	if num != ACM_InteractionResponse || typ != protowire.BytesType {
		t.Fatalf("expected field %d bytes-type, got num=%d typ=%d", ACM_InteractionResponse, num, typ)
	}
}

func TestEncodeRunRequestRequestedModelParams(t *testing.T) {
	params := &RunRequestParams{
		ModelId:        "grok-4.6",
		SystemPrompt:   "helper",
		UserText:       "hello",
		MessageId:      "msg-1",
		ConversationId: "conv-1",
		ModelParams:    map[string]string{"thinking": "true", "effort": "high"},
	}
	b, err := EncodeRunRequest(params)
	if err != nil {
		t.Fatalf("EncodeRunRequest: %v", err)
	}
	if !wireHasField(b, 1, 9) {
		t.Fatal("expected AgentRunRequest to carry requested_model (field 9) when parameters are set")
	}

	// No parameters: the requested_model field must be omitted entirely.
	params.ModelParams = nil
	b, err = EncodeRunRequest(params)
	if err != nil {
		t.Fatalf("EncodeRunRequest: %v", err)
	}
	runRequest := firstBytesField(b, ACM_RunRequest)
	if runRequest == nil {
		t.Fatal("missing run_request payload")
	}
	if wireHasField(runRequest, ARR_RequestedModel, ARR_RequestedModel) {
		t.Fatal("requested_model must be omitted when no parameters are set")
	}
}

// wireHasField reports whether the message `outer` (a parent message payload)
// contains the bytes field `inner` whose own payload carries field `want`.
// Used to assert nested field presence on the hand-built wire format.
func wireHasField(outer []byte, inner, want int) bool {
	payload := firstBytesField(outer, inner)
	if payload == nil {
		return false
	}
	return firstBytesField(payload, want) != nil
}

func firstBytesField(data []byte, target int) []byte {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return nil
		}
		data = data[n:]
		if typ != protowire.BytesType {
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return nil
			}
			data = data[n:]
			continue
		}
		val, n := protowire.ConsumeBytes(data)
		if n < 0 {
			return nil
		}
		data = data[n:]
		if int(num) == target {
			return val
		}
	}
	return nil
}

func TestEncodeRequestsRejectNilParams(t *testing.T) {
	if _, err := EncodeRunRequest(nil); err == nil {
		t.Fatal("expected nil run request params to return an error")
	}
	if _, err := EncodeResumeRequest(nil); err == nil {
		t.Fatal("expected nil resume request params to return an error")
	}
}

func mustMsg(t *testing.T, name string) protoreflect.MessageDescriptor {
	t.Helper()
	md, err := Msg(name)
	if err != nil {
		t.Fatalf("Msg(%s): %v", name, err)
	}
	return md
}
