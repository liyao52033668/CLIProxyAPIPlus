package executor

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
)

// observeClaudeStreamJSONPayload tracks the upstream message id and completion
// state from one Claude SSE payload. hasDataPrefix reports whether the payload
// came from a "data:" line; a bare JSON body (no SSE framing) only completes a
// message when it carries a stop_reason.
func observeClaudeStreamJSONPayload(payload []byte, hasDataPrefix bool, messageID *string, completed *bool) {
	root := gjson.ParseBytes(payload)
	switch root.Get("type").String() {
	case "message_start":
		if id := strings.TrimSpace(root.Get("message.id").String()); id != "" {
			*messageID = id
		}
	case "message_stop":
		*completed = true
	case "message":
		if !hasDataPrefix && strings.TrimSpace(root.Get("stop_reason").String()) != "" {
			if id := strings.TrimSpace(root.Get("id").String()); id != "" {
				*messageID = id
			}
			*completed = true
		}
	}
}

// observeClaudeSSEEvent parses one complete SSE event (possibly multi-line
// data), updates the stream state, and captures an upstream error event into
// streamErr so the executor can fail the attempt instead of silently
// reporting a truncated success.
func observeClaudeSSEEvent(rawEvent []byte, messageID *string, completed *bool, streamErr *error) {
	var dataLines [][]byte
	for _, line := range bytes.Split(rawEvent, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(trimmed[len("data:"):])
		if len(payload) > 0 {
			dataLines = append(dataLines, payload)
		}
	}
	if len(dataLines) == 0 {
		trimmed := bytes.TrimSpace(rawEvent)
		if bytes.HasPrefix(trimmed, []byte("{")) && gjson.ValidBytes(trimmed) {
			observeClaudeStreamJSONPayload(trimmed, false, messageID, completed)
		}
		return
	}
	joined := bytes.TrimSpace(bytes.Join(dataLines, []byte("\n")))
	if !gjson.ValidBytes(joined) {
		return
	}
	observeClaudeStreamJSONPayload(joined, true, messageID, completed)
	if streamErr != nil && *streamErr == nil && gjson.ParseBytes(joined).Get("type").String() == "error" {
		*streamErr = statusErr{code: 502, msg: string(joined)}
	}
}

// claudeMessageIDFromSSE extracts the upstream message id from a complete SSE
// body, returning empty when the stream never reached message_stop.
func claudeMessageIDFromSSE(data []byte) string {
	var messageID string
	completed := false
	var eventBuf bytes.Buffer
	for _, line := range bytes.Split(data, []byte("\n")) {
		eventBuf.Write(line)
		eventBuf.WriteByte('\n')
		if len(bytes.TrimSpace(line)) == 0 {
			observeClaudeSSEEvent(eventBuf.Bytes(), &messageID, &completed, nil)
			eventBuf.Reset()
		}
	}
	if eventBuf.Len() > 0 {
		observeClaudeSSEEvent(eventBuf.Bytes(), &messageID, &completed, nil)
	}
	if !completed {
		return ""
	}
	return messageID
}
