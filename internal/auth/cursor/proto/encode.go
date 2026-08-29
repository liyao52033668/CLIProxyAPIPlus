// Package proto provides protobuf encoding for Cursor's gRPC API,
// using dynamicpb with the embedded FileDescriptorProto from agent.proto.
// This mirrors the cursor-auth TS plugin's use of @bufbuild/protobuf create()+toBinary().
package proto

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/structpb"
)

// --- Public types ---

// RunRequestParams holds all data needed to build an AgentRunRequest.
type RunRequestParams struct {
	ModelId        string
	SystemPrompt   string
	UserText       string
	MessageId      string
	ConversationId string
	Images         []ImageData
	Turns          []TurnData
	McpTools       []McpToolDef
	BlobStore      map[string][]byte // hex(sha256) -> data, populated during encoding
	RawCheckpoint  []byte            // if non-nil, use as conversation_state directly (from server checkpoint)
	// ModelParams are sent as RequestedModel.parameters (thinking/effort etc.).
	// Empty means the requested_model field is omitted and upstream defaults apply.
	ModelParams map[string]string
}

type ImageData struct {
	MimeType string
	Data     []byte
	Width    int // decoded from the image header; 0 = unknown (dimension omitted)
	Height   int
}

type TurnData struct {
	UserText      string
	AssistantText string
}

type McpToolDef struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// encoder accumulates the first encoding error so nested message construction
// can avoid threading error returns through every field helper call.
// Field existence and types are still validated; the first failure is kept.
type encoder struct {
	err error
}

func (e *encoder) setErr(err error) {
	if e.err == nil && err != nil {
		e.err = err
	}
}

func (e *encoder) newMsg(name string) *dynamicpb.Message {
	if e.err != nil {
		return nil
	}
	md, err := Msg(name)
	if err != nil {
		e.setErr(err)
		return nil
	}
	return dynamicpb.NewMessage(md)
}

// NewMsg creates a dynamic message by top-level descriptor name.
func NewMsg(name string) (*dynamicpb.Message, error) {
	md, err := Msg(name)
	if err != nil {
		return nil, err
	}
	return dynamicpb.NewMessage(md), nil
}

// requireField returns a field descriptor, recording an error if missing.
func (e *encoder) requireField(msg *dynamicpb.Message, name string) protoreflect.FieldDescriptor {
	if e.err != nil {
		return nil
	}
	if msg == nil {
		e.setErr(fmt.Errorf("cursor proto: cannot access field %q on nil message", name))
		return nil
	}
	fd := msg.Descriptor().Fields().ByName(protoreflect.Name(name))
	if fd == nil {
		e.setErr(fmt.Errorf("cursor proto: field %q not found in %s", name, msg.Descriptor().Name()))
		return nil
	}
	return fd
}

func (e *encoder) requireFieldType(msg *dynamicpb.Message, name string, kind protoreflect.Kind, repeated bool) protoreflect.FieldDescriptor {
	fd := e.requireField(msg, name)
	if fd == nil {
		return nil
	}
	actualRepeated := fd.Cardinality() == protoreflect.Repeated
	if fd.IsMap() || actualRepeated != repeated || fd.Kind() != kind {
		cardinality := "singular"
		if repeated {
			cardinality = "repeated"
		}
		e.setErr(fmt.Errorf("cursor proto: field %q in %s must be %s %s, got %s", name, msg.Descriptor().Name(), cardinality, kind, fd.Kind()))
		return nil
	}
	return fd
}

func (e *encoder) requireMessageField(msg *dynamicpb.Message, name string, sub *dynamicpb.Message, repeated bool) protoreflect.FieldDescriptor {
	if sub == nil {
		e.setErr(fmt.Errorf("cursor proto: field %q has nil message value", name))
		return nil
	}
	fd := e.requireFieldType(msg, name, protoreflect.MessageKind, repeated)
	if fd == nil {
		return nil
	}
	if fd.Message().FullName() != sub.Descriptor().FullName() {
		e.setErr(fmt.Errorf("cursor proto: field %q in %s expects %s, got %s", name, msg.Descriptor().Name(), fd.Message().FullName(), sub.Descriptor().FullName()))
		return nil
	}
	return fd
}

// lookupField returns a field without recording an error (optional / compatibility fields).
func lookupField(msg *dynamicpb.Message, name string) protoreflect.FieldDescriptor {
	if msg == nil {
		return nil
	}
	return msg.Descriptor().Fields().ByName(protoreflect.Name(name))
}

func (e *encoder) setStr(msg *dynamicpb.Message, name, val string) {
	if e.err != nil || val == "" {
		return
	}
	fd := e.requireFieldType(msg, name, protoreflect.StringKind, false)
	if fd == nil {
		return
	}
	msg.Set(fd, protoreflect.ValueOfString(val))
}

// setStrForce sets a string field even when val is empty (e.g. exec_id).
func (e *encoder) setStrForce(msg *dynamicpb.Message, name, val string) {
	if e.err != nil {
		return
	}
	fd := e.requireFieldType(msg, name, protoreflect.StringKind, false)
	if fd == nil {
		return
	}
	msg.Set(fd, protoreflect.ValueOfString(val))
}

func (e *encoder) setBytes(msg *dynamicpb.Message, name string, val []byte) {
	if e.err != nil || len(val) == 0 {
		return
	}
	fd := e.requireFieldType(msg, name, protoreflect.BytesKind, false)
	if fd == nil {
		return
	}
	msg.Set(fd, protoreflect.ValueOfBytes(val))
}

func (e *encoder) setUint32(msg *dynamicpb.Message, name string, val uint32) {
	if e.err != nil {
		return
	}
	fd := e.requireFieldType(msg, name, protoreflect.Uint32Kind, false)
	if fd == nil {
		return
	}
	msg.Set(fd, protoreflect.ValueOfUint32(val))
}

func (e *encoder) setInt32(msg *dynamicpb.Message, name string, val int32) {
	if e.err != nil {
		return
	}
	fd := e.requireFieldType(msg, name, protoreflect.Int32Kind, false)
	if fd == nil {
		return
	}
	msg.Set(fd, protoreflect.ValueOfInt32(val))
}

func (e *encoder) setBool(msg *dynamicpb.Message, name string, val bool) {
	if e.err != nil {
		return
	}
	fd := e.requireFieldType(msg, name, protoreflect.BoolKind, false)
	if fd == nil {
		return
	}
	msg.Set(fd, protoreflect.ValueOfBool(val))
}

func (e *encoder) setMsg(msg *dynamicpb.Message, name string, sub *dynamicpb.Message) {
	if e.err != nil {
		return
	}
	fd := e.requireMessageField(msg, name, sub, false)
	if fd == nil {
		return
	}
	msg.Set(fd, protoreflect.ValueOfMessage(sub.ProtoReflect()))
}

func (e *encoder) appendBytes(msg *dynamicpb.Message, name string, val []byte) {
	if e.err != nil {
		return
	}
	fd := e.requireFieldType(msg, name, protoreflect.BytesKind, true)
	if fd == nil {
		return
	}
	msg.Mutable(fd).List().Append(protoreflect.ValueOfBytes(val))
}

func (e *encoder) appendBytesIfPresent(msg *dynamicpb.Message, name string, val []byte) {
	if e.err != nil || msg == nil {
		return
	}
	if lookupField(msg, name) == nil {
		return
	}
	e.appendBytes(msg, name, val)
}

func (e *encoder) appendMsg(msg *dynamicpb.Message, name string, sub *dynamicpb.Message) {
	if e.err != nil {
		return
	}
	fd := e.requireMessageField(msg, name, sub, true)
	if fd == nil {
		return
	}
	msg.Mutable(fd).List().Append(protoreflect.ValueOfMessage(sub.ProtoReflect()))
}

func (e *encoder) marshal(msg *dynamicpb.Message) []byte {
	if e.err != nil || msg == nil {
		return nil
	}
	b, err := proto.Marshal(msg)
	if err != nil {
		e.setErr(fmt.Errorf("cursor proto marshal: %w", err))
		return nil
	}
	return b
}

// requestedModel encodes RequestedModel{model_id, parameters} for the given
// parameter map (sorted for deterministic wire output); nil when no params.
// Upstream rejects parameter ids a model does not declare, so callers must
// gate values against the account's model catalog.
func (e *encoder) requestedModel(modelId string, params map[string]string) *dynamicpb.Message {
	if e.err != nil || len(params) == 0 {
		return nil
	}
	rm := e.newMsg("RequestedModel")
	e.setStr(rm, "model_id", modelId)
	for _, k := range slices.Sorted(maps.Keys(params)) {
		par := e.newMsg("RequestedModel_ModelParameterbytes")
		e.setStr(par, "id", k)
		e.setStr(par, "value", params[k])
		e.appendMsg(rm, "parameters", par)
	}
	return rm
}

// --- Encode functions mirroring cursor-fetch.ts ---

// EncodeHeartbeat returns an encoded AgentClientMessage with clientHeartbeat.
// Mirrors: create(AgentClientMessageSchema, { message: { case: 'clientHeartbeat', value: create(ClientHeartbeatSchema, {}) } })
func EncodeHeartbeat() ([]byte, error) {
	var e encoder
	hb := e.newMsg("ClientHeartbeat")
	acm := e.newMsg("AgentClientMessage")
	e.setMsg(acm, "client_heartbeat", hb)
	b := e.marshal(acm)
	return b, e.err
}

// EncodeRunRequest builds a full AgentClientMessage wrapping an AgentRunRequest.
// Mirrors buildCursorRequest() in cursor-fetch.ts.
// If p.RawCheckpoint is set, it is used directly as the conversation_state bytes
// (from a previous conversation_checkpoint_update), skipping manual turn construction.
func EncodeRunRequest(p *RunRequestParams) ([]byte, error) {
	if p == nil {
		return nil, fmt.Errorf("cursor proto: run request params are nil")
	}
	if p.RawCheckpoint != nil {
		return encodeRunRequestWithCheckpoint(p)
	}

	if p.BlobStore == nil {
		p.BlobStore = make(map[string][]byte)
	}

	var e encoder

	// --- Conversation turns ---
	// Each turn is serialized as bytes (ConversationTurnStructure → bytes)
	var turnBytes [][]byte
	for _, turn := range p.Turns {
		// UserMessage for this turn
		um := e.newMsg("UserMessage")
		e.setStr(um, "text", turn.UserText)
		e.setStr(um, "message_id", generateId())
		umBytes := e.marshal(um)

		// Steps (assistant response)
		var stepBytes [][]byte
		if turn.AssistantText != "" {
			am := e.newMsg("AssistantMessage")
			e.setStr(am, "text", turn.AssistantText)
			step := e.newMsg("ConversationStep")
			e.setMsg(step, "assistant_message", am)
			stepBytes = append(stepBytes, e.marshal(step))
		}

		// AgentConversationTurnStructure (fields are bytes, not submessages)
		agentTurn := e.newMsg("AgentConversationTurnStructure")
		e.setBytes(agentTurn, "user_message", umBytes)
		for _, sb := range stepBytes {
			e.appendBytes(agentTurn, "steps", sb)
		}

		// ConversationTurnStructure (oneof turn → agentConversationTurn)
		cts := e.newMsg("ConversationTurnStructure")
		e.setMsg(cts, "agent_conversation_turn", agentTurn)
		turnBytes = append(turnBytes, e.marshal(cts))
	}

	// --- System prompt blob ---
	systemJSON, _ := json.Marshal(map[string]string{"role": "system", "content": p.SystemPrompt})
	blobId := sha256Sum(systemJSON)
	p.BlobStore[hex.EncodeToString(blobId)] = systemJSON

	// --- ConversationStateStructure ---
	css := e.newMsg("ConversationStateStructure")
	// rootPromptMessagesJson: repeated bytes
	e.appendBytes(css, "root_prompt_messages_json", blobId)
	// turns: repeated bytes (field 8) + turns_old (field 2) for compatibility
	for _, tb := range turnBytes {
		e.appendBytes(css, "turns", tb)
	}
	for _, tb := range turnBytes {
		e.appendBytesIfPresent(css, "turns_old", tb)
	}

	// --- UserMessage (current) ---
	userMessage := e.newMsg("UserMessage")
	e.setStr(userMessage, "text", p.UserText)
	e.setStr(userMessage, "message_id", p.MessageId)

	// Images via SelectedContext
	if len(p.Images) > 0 {
		sc := e.newMsg("SelectedContext")
		for _, img := range p.Images {
			si := e.newMsg("SelectedImage")
			e.setStr(si, "uuid", generateId())
			e.setStr(si, "mime_type", img.MimeType)
			e.setBytes(si, "data", img.Data)
			// Some upstream models silently drop attachments without dimensions.
			if img.Width > 0 && img.Height > 0 {
				dim := e.newMsg("SelectedImage_Dimension")
				e.setInt32(dim, "width", int32(img.Width))
				e.setInt32(dim, "height", int32(img.Height))
				e.setMsg(si, "dimension", dim)
			}
			e.appendMsg(sc, "selected_images", si)
		}
		e.setMsg(userMessage, "selected_context", sc)
	}

	// --- UserMessageAction ---
	uma := e.newMsg("UserMessageAction")
	e.setMsg(uma, "user_message", userMessage)

	// --- ConversationAction ---
	ca := e.newMsg("ConversationAction")
	e.setMsg(ca, "user_message_action", uma)

	// --- ModelDetails ---
	md := e.newMsg("ModelDetails")
	e.setStr(md, "model_id", p.ModelId)
	e.setStr(md, "display_model_id", p.ModelId)
	e.setStr(md, "display_name", p.ModelId)

	// --- AgentRunRequest ---
	arr := e.newMsg("AgentRunRequest")
	e.setMsg(arr, "conversation_state", css)
	e.setMsg(arr, "action", ca)
	e.setMsg(arr, "model_details", md)
	e.setStr(arr, "conversation_id", p.ConversationId)

	// McpTools
	if len(p.McpTools) > 0 {
		mcpTools := e.newMsg("McpTools")
		for _, tool := range p.McpTools {
			td := e.newMsg("McpToolDefinition")
			e.setStr(td, "name", tool.Name)
			e.setStr(td, "description", tool.Description)
			if len(tool.InputSchema) > 0 {
				e.setBytes(td, "input_schema", jsonToProtobufValueBytes(tool.InputSchema))
			}
			e.setStr(td, "provider_identifier", "proxy")
			e.setStr(td, "tool_name", tool.Name)
			e.appendMsg(mcpTools, "mcp_tools", td)
		}
		e.setMsg(arr, "mcp_tools", mcpTools)
	}

	// RequestedModel carries per-model parameters (thinking/effort). Upstream
	// rejects ids a model does not declare, so callers gate on the catalog.
	if rm := e.requestedModel(p.ModelId, p.ModelParams); rm != nil {
		e.setMsg(arr, "requested_model", rm)
	}

	// --- AgentClientMessage ---
	acm := e.newMsg("AgentClientMessage")
	e.setMsg(acm, "run_request", arr)

	b := e.marshal(acm)
	return b, e.err
}

// encodeRunRequestWithCheckpoint builds an AgentClientMessage using a raw checkpoint
// as conversation_state. The checkpoint bytes are embedded directly without deserialization.
func encodeRunRequestWithCheckpoint(p *RunRequestParams) ([]byte, error) {
	var e encoder

	// Build UserMessage
	userMessage := e.newMsg("UserMessage")
	e.setStr(userMessage, "text", p.UserText)
	e.setStr(userMessage, "message_id", p.MessageId)
	if len(p.Images) > 0 {
		sc := e.newMsg("SelectedContext")
		for _, img := range p.Images {
			si := e.newMsg("SelectedImage")
			e.setStr(si, "uuid", generateId())
			e.setStr(si, "mime_type", img.MimeType)
			e.setBytes(si, "data", img.Data)
			// Some upstream models silently drop attachments without dimensions.
			if img.Width > 0 && img.Height > 0 {
				dim := e.newMsg("SelectedImage_Dimension")
				e.setInt32(dim, "width", int32(img.Width))
				e.setInt32(dim, "height", int32(img.Height))
				e.setMsg(si, "dimension", dim)
			}
			e.appendMsg(sc, "selected_images", si)
		}
		e.setMsg(userMessage, "selected_context", sc)
	}

	// Build ConversationAction with UserMessageAction
	uma := e.newMsg("UserMessageAction")
	e.setMsg(uma, "user_message", userMessage)
	ca := e.newMsg("ConversationAction")
	e.setMsg(ca, "user_message_action", uma)
	caBytes := e.marshal(ca)

	// Build ModelDetails
	md := e.newMsg("ModelDetails")
	e.setStr(md, "model_id", p.ModelId)
	e.setStr(md, "display_model_id", p.ModelId)
	e.setStr(md, "display_name", p.ModelId)
	mdBytes := e.marshal(md)

	// Build McpTools
	var mcpToolsBytes []byte
	if len(p.McpTools) > 0 {
		mcpTools := e.newMsg("McpTools")
		for _, tool := range p.McpTools {
			td := e.newMsg("McpToolDefinition")
			e.setStr(td, "name", tool.Name)
			e.setStr(td, "description", tool.Description)
			if len(tool.InputSchema) > 0 {
				e.setBytes(td, "input_schema", jsonToProtobufValueBytes(tool.InputSchema))
			}
			e.setStr(td, "provider_identifier", "proxy")
			e.setStr(td, "tool_name", tool.Name)
			e.appendMsg(mcpTools, "mcp_tools", td)
		}
		mcpToolsBytes = e.marshal(mcpTools)
	}

	if e.err != nil {
		return nil, e.err
	}

	// Manually assemble AgentRunRequest using protowire to embed raw checkpoint
	var arrBuf []byte
	// field 1: conversation_state = raw checkpoint bytes (length-delimited)
	arrBuf = protowire.AppendTag(arrBuf, ARR_ConversationState, protowire.BytesType)
	arrBuf = protowire.AppendBytes(arrBuf, p.RawCheckpoint)
	// field 2: action = ConversationAction
	arrBuf = protowire.AppendTag(arrBuf, ARR_Action, protowire.BytesType)
	arrBuf = protowire.AppendBytes(arrBuf, caBytes)
	// field 3: model_details = ModelDetails
	arrBuf = protowire.AppendTag(arrBuf, ARR_ModelDetails, protowire.BytesType)
	arrBuf = protowire.AppendBytes(arrBuf, mdBytes)
	// field 4: mcp_tools = McpTools
	if len(mcpToolsBytes) > 0 {
		arrBuf = protowire.AppendTag(arrBuf, ARR_McpTools, protowire.BytesType)
		arrBuf = protowire.AppendBytes(arrBuf, mcpToolsBytes)
	}
	// field 5: conversation_id = string
	if p.ConversationId != "" {
		arrBuf = protowire.AppendTag(arrBuf, ARR_ConversationId, protowire.BytesType)
		arrBuf = protowire.AppendString(arrBuf, p.ConversationId)
	}
	// field 9: requested_model (thinking/effort parameters)
	if rmBytes := e.marshal(e.requestedModel(p.ModelId, p.ModelParams)); len(rmBytes) > 0 {
		arrBuf = protowire.AppendTag(arrBuf, ARR_RequestedModel, protowire.BytesType)
		arrBuf = protowire.AppendBytes(arrBuf, rmBytes)
	}

	// Wrap in AgentClientMessage field 1 (run_request)
	var acmBuf []byte
	acmBuf = protowire.AppendTag(acmBuf, ACM_RunRequest, protowire.BytesType)
	acmBuf = protowire.AppendBytes(acmBuf, arrBuf)

	log.Debugf("cursor encode: built RunRequest with checkpoint (%d bytes), total=%d bytes", len(p.RawCheckpoint), len(acmBuf))
	return acmBuf, nil
}

// ResumeRequestParams holds data for a ResumeAction request.
type ResumeRequestParams struct {
	ModelId        string
	ConversationId string
	McpTools       []McpToolDef
}

// EncodeResumeRequest builds an AgentClientMessage with ResumeAction.
// Used to resume a conversation by conversation_id without re-sending full history.
func EncodeResumeRequest(p *ResumeRequestParams) ([]byte, error) {
	if p == nil {
		return nil, fmt.Errorf("cursor proto: resume request params are nil")
	}
	var e encoder

	// RequestContext with tools
	rc := e.newMsg("RequestContext")
	if len(p.McpTools) > 0 {
		for _, tool := range p.McpTools {
			td := e.newMsg("McpToolDefinition")
			e.setStr(td, "name", tool.Name)
			e.setStr(td, "description", tool.Description)
			if len(tool.InputSchema) > 0 {
				e.setBytes(td, "input_schema", jsonToProtobufValueBytes(tool.InputSchema))
			}
			e.setStr(td, "provider_identifier", "proxy")
			e.setStr(td, "tool_name", tool.Name)
			e.appendMsg(rc, "tools", td)
		}
	}

	// ResumeAction
	ra := e.newMsg("ResumeAction")
	e.setMsg(ra, "request_context", rc)

	// ConversationAction with resume_action
	ca := e.newMsg("ConversationAction")
	e.setMsg(ca, "resume_action", ra)

	// ModelDetails
	md := e.newMsg("ModelDetails")
	e.setStr(md, "model_id", p.ModelId)
	e.setStr(md, "display_model_id", p.ModelId)
	e.setStr(md, "display_name", p.ModelId)

	// AgentRunRequest — no conversation_state needed for resume
	arr := e.newMsg("AgentRunRequest")
	e.setMsg(arr, "action", ca)
	e.setMsg(arr, "model_details", md)
	e.setStr(arr, "conversation_id", p.ConversationId)

	// McpTools at top level
	if len(p.McpTools) > 0 {
		mcpTools := e.newMsg("McpTools")
		for _, tool := range p.McpTools {
			td := e.newMsg("McpToolDefinition")
			e.setStr(td, "name", tool.Name)
			e.setStr(td, "description", tool.Description)
			if len(tool.InputSchema) > 0 {
				e.setBytes(td, "input_schema", jsonToProtobufValueBytes(tool.InputSchema))
			}
			e.setStr(td, "provider_identifier", "proxy")
			e.setStr(td, "tool_name", tool.Name)
			e.appendMsg(mcpTools, "mcp_tools", td)
		}
		e.setMsg(arr, "mcp_tools", mcpTools)
	}

	acm := e.newMsg("AgentClientMessage")
	e.setMsg(acm, "run_request", arr)
	b := e.marshal(acm)
	return b, e.err
}

// --- KV response encoders ---
// Mirrors handleKvMessage() in cursor-fetch.ts

// EncodeKvGetBlobResult responds to a getBlobArgs request.
func EncodeKvGetBlobResult(kvId uint32, blobData []byte) ([]byte, error) {
	var e encoder
	result := e.newMsg("GetBlobResult")
	if blobData != nil {
		e.setBytes(result, "blob_data", blobData)
	}

	kvc := e.newMsg("KvClientMessage")
	e.setUint32(kvc, "id", kvId)
	e.setMsg(kvc, "get_blob_result", result)

	acm := e.newMsg("AgentClientMessage")
	e.setMsg(acm, "kv_client_message", kvc)
	b := e.marshal(acm)
	return b, e.err
}

// EncodeKvSetBlobResult responds to a setBlobArgs request.
func EncodeKvSetBlobResult(kvId uint32) ([]byte, error) {
	var e encoder
	result := e.newMsg("SetBlobResult")

	kvc := e.newMsg("KvClientMessage")
	e.setUint32(kvc, "id", kvId)
	e.setMsg(kvc, "set_blob_result", result)

	acm := e.newMsg("AgentClientMessage")
	e.setMsg(acm, "kv_client_message", kvc)
	b := e.marshal(acm)
	return b, e.err
}

// --- Exec response encoders ---
// Mirrors handleExecMessage() and sendExec() in cursor-fetch.ts

// EncodeExecRequestContextResult responds to requestContextArgs with tool definitions.
func EncodeExecRequestContextResult(execMsgId uint32, execId string, tools []McpToolDef) ([]byte, error) {
	var e encoder
	// RequestContext with tools
	rc := e.newMsg("RequestContext")
	if len(tools) > 0 {
		for _, tool := range tools {
			td := e.newMsg("McpToolDefinition")
			e.setStr(td, "name", tool.Name)
			e.setStr(td, "description", tool.Description)
			if len(tool.InputSchema) > 0 {
				e.setBytes(td, "input_schema", jsonToProtobufValueBytes(tool.InputSchema))
			}
			e.setStr(td, "provider_identifier", "proxy")
			e.setStr(td, "tool_name", tool.Name)
			e.appendMsg(rc, "tools", td)
		}
	}

	// RequestContextSuccess
	rcs := e.newMsg("RequestContextSuccess")
	e.setMsg(rcs, "request_context", rc)

	// RequestContextResult (oneof success)
	rcr := e.newMsg("RequestContextResult")
	e.setMsg(rcr, "success", rcs)

	if e.err != nil {
		return nil, e.err
	}
	return encodeExecClientMsg(execMsgId, execId, "request_context_result", rcr)
}

// EncodeInteractionQueryResponse grants a server-initiated InteractionQuery.
// Only the web-search query is answered (approved); the upstream otherwise
// waits on the query and the turn stalls. Mirrors the observed CLI behavior
// of granting the search so the stream proceeds.
func EncodeInteractionQueryResponse(queryId uint32) ([]byte, error) {
	var e encoder
	approved := e.newMsg("WebSearchRequestResponse_Approved")
	wsResp := e.newMsg("WebSearchRequestResponse")
	e.setMsg(wsResp, "approved", approved)
	resp := e.newMsg("InteractionResponse")
	e.setUint32(resp, "id", queryId)
	e.setMsg(resp, "web_search_request_response", wsResp)
	acm := e.newMsg("AgentClientMessage")
	e.setMsg(acm, "interaction_response", resp)
	b := e.marshal(acm)
	return b, e.err
}

// EncodeExecMcpResult responds with MCP tool result.
func EncodeExecMcpResult(execMsgId uint32, execId string, content string, isError bool) ([]byte, error) {
	var e encoder
	textContent := e.newMsg("McpTextContent")
	e.setStr(textContent, "text", content)

	contentItem := e.newMsg("McpToolResultContentItem")
	e.setMsg(contentItem, "text", textContent)

	success := e.newMsg("McpSuccess")
	e.appendMsg(success, "content", contentItem)
	e.setBool(success, "is_error", isError)

	result := e.newMsg("McpResult")
	e.setMsg(result, "success", success)

	if e.err != nil {
		return nil, e.err
	}
	return encodeExecClientMsg(execMsgId, execId, "mcp_result", result)
}

// EncodeExecMcpError responds with MCP error.
func EncodeExecMcpError(execMsgId uint32, execId string, errMsg string) ([]byte, error) {
	var e encoder
	mcpErr := e.newMsg("McpError")
	e.setStr(mcpErr, "error", errMsg)

	result := e.newMsg("McpResult")
	e.setMsg(result, "error", mcpErr)

	if e.err != nil {
		return nil, e.err
	}
	return encodeExecClientMsg(execMsgId, execId, "mcp_result", result)
}

// --- Rejection encoders (mirror handleExecMessage rejections) ---

func EncodeExecReadRejected(execMsgId uint32, execId string, path, reason string) ([]byte, error) {
	var e encoder
	rej := e.newMsg("ReadRejected")
	e.setStr(rej, "path", path)
	e.setStr(rej, "reason", reason)
	result := e.newMsg("ReadResult")
	e.setMsg(result, "rejected", rej)
	if e.err != nil {
		return nil, e.err
	}
	return encodeExecClientMsg(execMsgId, execId, "read_result", result)
}

func EncodeExecShellRejected(execMsgId uint32, execId string, command, workDir, reason string) ([]byte, error) {
	var e encoder
	rej := e.newMsg("ShellRejected")
	e.setStr(rej, "command", command)
	e.setStr(rej, "working_directory", workDir)
	e.setStr(rej, "reason", reason)
	result := e.newMsg("ShellResult")
	e.setMsg(result, "rejected", rej)
	if e.err != nil {
		return nil, e.err
	}
	return encodeExecClientMsg(execMsgId, execId, "shell_result", result)
}

func EncodeExecWriteRejected(execMsgId uint32, execId string, path, reason string) ([]byte, error) {
	var e encoder
	rej := e.newMsg("WriteRejected")
	e.setStr(rej, "path", path)
	e.setStr(rej, "reason", reason)
	result := e.newMsg("WriteResult")
	e.setMsg(result, "rejected", rej)
	if e.err != nil {
		return nil, e.err
	}
	return encodeExecClientMsg(execMsgId, execId, "write_result", result)
}

func EncodeExecDeleteRejected(execMsgId uint32, execId string, path, reason string) ([]byte, error) {
	var e encoder
	rej := e.newMsg("DeleteRejected")
	e.setStr(rej, "path", path)
	e.setStr(rej, "reason", reason)
	result := e.newMsg("DeleteResult")
	e.setMsg(result, "rejected", rej)
	if e.err != nil {
		return nil, e.err
	}
	return encodeExecClientMsg(execMsgId, execId, "delete_result", result)
}

func EncodeExecLsRejected(execMsgId uint32, execId string, path, reason string) ([]byte, error) {
	var e encoder
	rej := e.newMsg("LsRejected")
	e.setStr(rej, "path", path)
	e.setStr(rej, "reason", reason)
	result := e.newMsg("LsResult")
	e.setMsg(result, "rejected", rej)
	if e.err != nil {
		return nil, e.err
	}
	return encodeExecClientMsg(execMsgId, execId, "ls_result", result)
}

func EncodeExecGrepError(execMsgId uint32, execId string, errMsg string) ([]byte, error) {
	var e encoder
	grepErr := e.newMsg("GrepError")
	e.setStr(grepErr, "error", errMsg)
	result := e.newMsg("GrepResult")
	e.setMsg(result, "error", grepErr)
	if e.err != nil {
		return nil, e.err
	}
	return encodeExecClientMsg(execMsgId, execId, "grep_result", result)
}

func EncodeExecFetchError(execMsgId uint32, execId string, url, errMsg string) ([]byte, error) {
	var e encoder
	fetchErr := e.newMsg("FetchError")
	e.setStr(fetchErr, "url", url)
	e.setStr(fetchErr, "error", errMsg)
	result := e.newMsg("FetchResult")
	e.setMsg(result, "error", fetchErr)
	if e.err != nil {
		return nil, e.err
	}
	return encodeExecClientMsg(execMsgId, execId, "fetch_result", result)
}

func EncodeExecDiagnosticsResult(execMsgId uint32, execId string) ([]byte, error) {
	var e encoder
	result := e.newMsg("DiagnosticsResult")
	if e.err != nil {
		return nil, e.err
	}
	return encodeExecClientMsg(execMsgId, execId, "diagnostics_result", result)
}

func EncodeExecBackgroundShellSpawnRejected(execMsgId uint32, execId string, command, workDir, reason string) ([]byte, error) {
	var e encoder
	rej := e.newMsg("ShellRejected")
	e.setStr(rej, "command", command)
	e.setStr(rej, "working_directory", workDir)
	e.setStr(rej, "reason", reason)
	result := e.newMsg("BackgroundShellSpawnResult")
	e.setMsg(result, "rejected", rej)
	if e.err != nil {
		return nil, e.err
	}
	return encodeExecClientMsg(execMsgId, execId, "background_shell_spawn_result", result)
}

func EncodeExecWriteShellStdinError(execMsgId uint32, execId string, errMsg string) ([]byte, error) {
	var e encoder
	wsErr := e.newMsg("WriteShellStdinError")
	e.setStr(wsErr, "error", errMsg)
	result := e.newMsg("WriteShellStdinResult")
	e.setMsg(result, "error", wsErr)
	if e.err != nil {
		return nil, e.err
	}
	return encodeExecClientMsg(execMsgId, execId, "write_shell_stdin_result", result)
}

// encodeExecClientMsg wraps an exec result in AgentClientMessage.
// Mirrors sendExec() in cursor-fetch.ts.
func encodeExecClientMsg(id uint32, execId string, resultFieldName string, resultMsg *dynamicpb.Message) ([]byte, error) {
	var e encoder
	ecm := e.newMsg("ExecClientMessage")
	e.setUint32(ecm, "id", id)
	// Force set exec_id even if empty - Cursor requires this field to be set
	e.setStrForce(ecm, "exec_id", execId)

	fd := e.requireMessageField(ecm, resultFieldName, resultMsg, false)
	if fd == nil {
		return nil, e.err
	}

	// Debug: log the actual field being set
	log.Debugf("encodeExecClientMsg: setting field %q (number=%d, kind=%s)", fd.Name(), fd.Number(), fd.Kind())
	ecm.Set(fd, protoreflect.ValueOfMessage(resultMsg.ProtoReflect()))

	acm := e.newMsg("AgentClientMessage")
	e.setMsg(acm, "exec_client_message", ecm)
	b := e.marshal(acm)
	return b, e.err
}

// --- Utilities ---

// jsonToProtobufValueBytes converts a JSON schema (json.RawMessage) to protobuf Value binary.
// This mirrors the TS pattern: toBinary(ValueSchema, fromJson(ValueSchema, jsonSchema))
func jsonToProtobufValueBytes(jsonData json.RawMessage) []byte {
	if len(jsonData) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(jsonData, &v); err != nil {
		return jsonData // fallback to raw JSON if parsing fails
	}
	pbVal, err := structpb.NewValue(v)
	if err != nil {
		return jsonData // fallback
	}
	b, err := proto.Marshal(pbVal)
	if err != nil {
		return jsonData // fallback
	}
	return b
}

// ProtobufValueBytesToJSON converts protobuf Value binary back to JSON.
// This mirrors the TS pattern: toJson(ValueSchema, fromBinary(ValueSchema, value))
func ProtobufValueBytesToJSON(data []byte) (any, error) {
	val := &structpb.Value{}
	if err := proto.Unmarshal(data, val); err != nil {
		return nil, err
	}
	return val.AsInterface(), nil
}

func sha256Sum(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}

var idCounter uint64

func generateId() string {
	idCounter++
	h := sha256.Sum256([]byte{byte(idCounter), byte(idCounter >> 8), byte(idCounter >> 16)})
	return hex.EncodeToString(h[:16])
}
