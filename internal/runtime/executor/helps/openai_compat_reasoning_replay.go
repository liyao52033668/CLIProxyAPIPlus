package helps

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// OpenAICompatReasoningTurn is one assistant turn's reasoning captured from an
// OpenAI-compatible upstream response, used to satisfy DeepSeek-style thinking
// mode requirements that reasoning_content be passed back on follow-up turns.
type OpenAICompatReasoningTurn struct {
	Reasoning          string
	ToolCallIDs        []string
	ContentFingerprint string
}

// ReasoningLookup resolves cached reasoning for one assistant message. It
// receives whether the message carries tool calls, the tool-call IDs, and the
// message content fingerprint.
type ReasoningLookup func(hasToolCalls bool, toolCallIDs []string, contentFingerprint string) (string, bool)

// OpenAICompatReasoningCallerKey derives the caller-isolated cache scope from
// the request's API key. Empty when the caller is unauthenticated.
func OpenAICompatReasoningCallerKey(ctx context.Context) string {
	apiKey := strings.TrimSpace(APIKeyFromContext(ctx))
	if apiKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:8])
}

// RestoreOpenAICompatReasoningContent injects missing reasoning_content into
// assistant messages so thinking-mode upstreams with tool support accept the
// follow-up request. Messages that already carry non-empty reasoning_content
// are left untouched. Returns the updated payload and the injected count.
func RestoreOpenAICompatReasoningContent(payload []byte, lookup ReasoningLookup) ([]byte, int) {
	if lookup == nil || len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload, 0
	}
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return payload, 0
	}
	out := payload
	injected := 0
	messages.ForEach(func(index, message gjson.Result) bool {
		if !strings.EqualFold(strings.TrimSpace(message.Get("role").String()), "assistant") {
			return true
		}
		existing := message.Get("reasoning_content")
		if existing.Exists() && strings.TrimSpace(existing.String()) != "" {
			return true
		}
		toolCallIDs := openAICompatToolCallIDs(message)
		fingerprint := OpenAICompatReasoningContentFingerprint(message.Get("content"))
		reasoning, ok := lookup(len(toolCallIDs) > 0, toolCallIDs, fingerprint)
		if !ok {
			return true
		}
		reasoning = strings.TrimSpace(reasoning)
		if reasoning == "" {
			return true
		}
		path := fmt.Sprintf("messages.%d.reasoning_content", index.Int())
		updated, errSet := sjson.SetBytes(out, path, reasoning)
		if errSet != nil {
			return true
		}
		out = updated
		injected++
		return true
	})
	if injected > 0 {
		log.WithField("injected_reasoning_messages", injected).Debug("openai compat executor: restored reasoning_content from replay cache")
	}
	return out, injected
}

// CaptureOpenAICompatReasoningResponse extracts replayable reasoning turns
// from a completed chat completions response body.
func CaptureOpenAICompatReasoningResponse(body []byte) []OpenAICompatReasoningTurn {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return nil
	}
	choices := gjson.GetBytes(body, "choices")
	if !choices.IsArray() {
		return nil
	}
	var turns []OpenAICompatReasoningTurn
	choices.ForEach(func(_, choice gjson.Result) bool {
		message := choice.Get("message")
		turn, ok := openAICompatReasoningTurnFromMessage(message)
		if ok {
			turns = append(turns, turn)
		}
		return true
	})
	return turns
}

func openAICompatReasoningTurnFromMessage(message gjson.Result) (OpenAICompatReasoningTurn, bool) {
	turn := OpenAICompatReasoningTurn{
		Reasoning:          strings.TrimSpace(message.Get("reasoning_content").String()),
		ToolCallIDs:        openAICompatToolCallIDs(message),
		ContentFingerprint: OpenAICompatReasoningContentFingerprint(message.Get("content")),
	}
	return turn, turn.Reasoning != ""
}

// OpenAICompatReasoningStreamCapture accumulates reasoning and tool-call IDs
// from streamed chat completions chunks. Feed it the same SSE lines that are
// translated downstream, then call Finish once the stream ends cleanly.
type OpenAICompatReasoningStreamCapture struct {
	choices map[int]*openAICompatReasoningStreamChoice
}

type openAICompatReasoningStreamChoice struct {
	reasoning   strings.Builder
	content     strings.Builder
	toolCallIDs []string
}

// NewOpenAICompatReasoningStreamCapture creates an empty stream capture.
func NewOpenAICompatReasoningStreamCapture() *OpenAICompatReasoningStreamCapture {
	return &OpenAICompatReasoningStreamCapture{choices: make(map[int]*openAICompatReasoningStreamChoice)}
}

// Observe records reasoning, content, and tool-call IDs from one SSE data
// line. Non-JSON lines and the [DONE] marker are ignored.
func (c *OpenAICompatReasoningStreamCapture) Observe(line []byte) {
	if c == nil {
		return
	}
	data := bytes.TrimSpace(bytes.TrimPrefix(bytes.TrimSpace(line), []byte("data:")))
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) || !gjson.ValidBytes(data) {
		return
	}
	root := gjson.ParseBytes(data)
	choices := root.Get("choices")
	if !choices.IsArray() {
		return
	}
	choices.ForEach(func(_, choice gjson.Result) bool {
		index := int(choice.Get("index").Int())
		state := c.choices[index]
		if state == nil {
			state = &openAICompatReasoningStreamChoice{}
			c.choices[index] = state
		}
		if reasoning := choice.Get("delta.reasoning_content"); reasoning.Exists() && reasoning.Type == gjson.String {
			state.reasoning.WriteString(reasoning.String())
		}
		if content := choice.Get("delta.content"); content.Exists() && content.Type == gjson.String {
			state.content.WriteString(content.String())
		}
		if toolCalls := choice.Get("delta.tool_calls"); toolCalls.IsArray() {
			for _, toolCall := range toolCalls.Array() {
				id := strings.TrimSpace(toolCall.Get("id").String())
				if id == "" {
					continue
				}
				known := false
				for _, existing := range state.toolCallIDs {
					if existing == id {
						known = true
						break
					}
				}
				if !known {
					state.toolCallIDs = append(state.toolCallIDs, id)
				}
			}
		}
		return true
	})
}

// Finish returns the captured turns and resets the capture. Returns nothing
// when no choice accumulated non-empty reasoning.
func (c *OpenAICompatReasoningStreamCapture) Finish() []OpenAICompatReasoningTurn {
	if c == nil || len(c.choices) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(c.choices))
	for index := range c.choices {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	var turns []OpenAICompatReasoningTurn
	for _, index := range indexes {
		state := c.choices[index]
		reasoning := strings.TrimSpace(state.reasoning.String())
		if reasoning == "" {
			continue
		}
		turn := OpenAICompatReasoningTurn{
			Reasoning:          reasoning,
			ToolCallIDs:        append([]string(nil), state.toolCallIDs...),
			ContentFingerprint: OpenAICompatReasoningContentFingerprint(gjson.Result{Type: gjson.String, Str: state.content.String()}),
		}
		turns = append(turns, turn)
	}
	c.choices = make(map[int]*openAICompatReasoningStreamChoice)
	return turns
}

// OpenAICompatReasoningContentFingerprint builds the stable lookup key for one
// assistant message content value.
func OpenAICompatReasoningContentFingerprint(content gjson.Result) string {
	text := openAICompatAssistantContentText(content)
	if text == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func openAICompatAssistantContentText(content gjson.Result) string {
	if !content.Exists() || content.Type == gjson.Null {
		return ""
	}
	if content.Type == gjson.String {
		return strings.TrimSpace(content.String())
	}
	if content.IsObject() {
		return strings.TrimSpace(content.Get("text").String())
	}
	if !content.IsArray() {
		return ""
	}
	parts := make([]string, 0, len(content.Array()))
	for _, part := range content.Array() {
		switch part.Type {
		case gjson.String:
			if text := strings.TrimSpace(part.String()); text != "" {
				parts = append(parts, text)
			}
		case gjson.JSON:
			if text := strings.TrimSpace(part.Get("text").String()); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func openAICompatToolCallIDs(message gjson.Result) []string {
	toolCalls := message.Get("tool_calls")
	if !toolCalls.IsArray() {
		return nil
	}
	var ids []string
	for _, toolCall := range toolCalls.Array() {
		if id := strings.TrimSpace(toolCall.Get("id").String()); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}
