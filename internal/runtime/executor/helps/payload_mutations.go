package helps

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// SetStringIfDifferent updates path only when its value is not already the
// canonical JSON string. Values with another JSON type are still normalized.
func SetStringIfDifferent(payload []byte, path, value string) []byte {
	current := gjson.GetBytes(payload, path)
	if current.Type == gjson.String && current.String() == value {
		return payload
	}
	updated, errSet := sjson.SetBytes(payload, path, value)
	if errSet != nil {
		return payload
	}
	return updated
}

// SetBoolIfDifferent updates path only when its value is not already the
// canonical JSON boolean. Values with another JSON type are still normalized.
func SetBoolIfDifferent(payload []byte, path string, value bool) []byte {
	current := gjson.GetBytes(payload, path)
	if (value && current.Type == gjson.True) || (!value && current.Type == gjson.False) {
		return payload
	}
	updated, errSet := sjson.SetBytes(payload, path, value)
	if errSet != nil {
		return payload
	}
	return updated
}

// SetRawIfDifferent updates path only when the existing raw JSON is identical.
func SetRawIfDifferent(payload []byte, path string, value []byte) []byte {
	current := gjson.GetBytes(payload, path)
	if current.Exists() && len(current.Indexes) == 0 && current.Raw == string(value) {
		return payload
	}
	updated, errSet := sjson.SetRawBytes(payload, path, value)
	if errSet != nil {
		return payload
	}
	return updated
}

// JoinRawJSONArray joins validated raw JSON array items without re-encoding them.
func JoinRawJSONArray(items [][]byte) []byte {
	size := len(items) + 1
	for _, item := range items {
		size += len(item)
	}
	out := make([]byte, 0, size)
	out = append(out, '[')
	for index, item := range items {
		if index > 0 {
			out = append(out, ',')
		}
		out = append(out, item...)
	}
	return append(out, ']')
}

// MoveOpenAISystemToUserMessage removes the top-level "system" field and any
// leading role=system messages from an OpenAI-format payload, prepending their
// text as a new text block into the first role=user message. The CodeBuddy
// upstream rejects requests that start with a system message (code 11128), so
// the system prompt must be carried inside a user message instead. Remaining
// mid-conversation system messages keep their position but are flattened to
// plain-string content when they use a block array.
func MoveOpenAISystemToUserMessage(payload []byte) []byte {
	topSystem := gjson.GetBytes(payload, "system")

	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() || len(messages.Array()) == 0 {
		return payload
	}

	// The collected text needs a user message to live in; when none exists the
	// top-level field and leading system messages are left in place so the
	// prompt is not lost.
	hasUser := false
	for _, message := range messages.Array() {
		if message.Get("role").String() == "user" {
			hasUser = true
			break
		}
	}

	systemText := ""
	changed := false

	// Drop the top-level system field, keeping its text.
	if topSystem.Exists() && hasUser {
		systemText = contentPlainText(topSystem)
		if updated, errDelete := sjson.DeleteBytes(payload, "system"); errDelete == nil {
			payload = updated
			changed = true
		}
	}

	kept := make([][]byte, 0, len(messages.Array()))
	firstUserKeptIndex := -1
	leading := true
	for _, message := range messages.Array() {
		role := message.Get("role").String()
		raw := []byte(message.Raw)

		// Drop leading system messages, collecting their text.
		if leading && role == "system" && hasUser {
			systemText = joinNonEmpty(systemText, contentPlainText(message.Get("content")))
			changed = true
			continue
		}
		leading = false

		// Mid-conversation system message: upstream only accepts string content.
		if role == "system" {
			content := message.Get("content")
			if content.IsArray() && len(content.Array()) > 0 {
				if flattened, errSet := sjson.SetBytes(raw, "content", contentPlainText(content)); errSet == nil {
					raw = flattened
					changed = true
				}
			}
		}

		if role == "user" && firstUserKeptIndex == -1 {
			firstUserKeptIndex = len(kept)
		}
		kept = append(kept, raw)
	}

	if systemText != "" && firstUserKeptIndex >= 0 {
		kept[firstUserKeptIndex] = prependTextBlockToContent(kept[firstUserKeptIndex], systemText)
		changed = true
	}

	if !changed {
		return payload
	}

	updated, errSet := sjson.SetRawBytes(payload, "messages", JoinRawJSONArray(kept))
	if errSet != nil {
		return payload
	}
	return updated
}

// prependTextBlockToContent inserts a text block at the front of a message
// content. String content is promoted to a block array first.
func prependTextBlockToContent(message []byte, text string) []byte {
	block := []byte(`{"type":"text","text":""}`)
	block, _ = sjson.SetBytes(block, "text", text)

	content := gjson.GetBytes(message, "content")
	items := make([][]byte, 0, len(content.Array())+2)
	items = append(items, block)
	if content.IsArray() {
		for _, item := range content.Array() {
			items = append(items, []byte(item.Raw))
		}
	} else if content.Type == gjson.String && content.String() != "" {
		existing := []byte(`{"type":"text","text":""}`)
		existing, _ = sjson.SetBytes(existing, "text", content.String())
		items = append(items, existing)
	}
	updated, errSet := sjson.SetRawBytes(message, "content", JoinRawJSONArray(items))
	if errSet != nil {
		return message
	}
	return updated
}

// joinNonEmpty joins two strings with a blank line when both are non-empty.
func joinNonEmpty(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "\n\n" + b
	}
}

// contentPlainText renders system content (a string or a block array) as a
// single plain-text string. Non-text blocks are skipped.
func contentPlainText(content gjson.Result) string {
	switch {
	case content.Type == gjson.String:
		return content.String()
	case content.IsArray():
		parts := make([]string, 0, len(content.Array()))
		for _, block := range content.Array() {
			if block.Get("type").String() != "text" {
				continue
			}
			if text := block.Get("text").String(); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n\n")
	default:
		return ""
	}
}
