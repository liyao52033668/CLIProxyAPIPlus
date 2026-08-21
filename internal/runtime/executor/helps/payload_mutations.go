package helps

import (
	"strconv"
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

// FlattenOpenAISystemContent normalizes system prompts in an OpenAI-format
// payload for upstreams that only accept plain-string system content. A
// leftover top-level "system" field is removed and merged into a role=system
// message; any system-role message whose content is a block array is joined
// into a single string.
func FlattenOpenAISystemContent(payload []byte) []byte {
	topText := ""
	if topSystem := gjson.GetBytes(payload, "system"); topSystem.Exists() {
		topText = systemContentText(topSystem)
		if updated, errDelete := sjson.DeleteBytes(payload, "system"); errDelete == nil {
			payload = updated
		}
	}

	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		if topText == "" {
			return payload
		}
		systemMsg := buildSystemMessage(topText)
		updated, errSet := sjson.SetRawBytes(payload, "messages", JoinRawJSONArray([][]byte{systemMsg}))
		if errSet != nil {
			return payload
		}
		return updated
	}

	systemIndex := -1
	for index, message := range messages.Array() {
		if message.Get("role").String() != "system" {
			continue
		}
		if systemIndex == -1 {
			systemIndex = index
		}
		content := message.Get("content")
		if !content.IsArray() || len(content.Array()) == 0 {
			continue
		}
		updated, errSet := sjson.SetBytes(payload, "messages."+strconv.Itoa(index)+".content", systemContentText(content))
		if errSet != nil {
			return payload
		}
		payload = updated
	}

	if topText == "" {
		return payload
	}
	if systemIndex >= 0 {
		path := "messages." + strconv.Itoa(systemIndex) + ".content"
		merged := topText
		if existing := gjson.GetBytes(payload, path).String(); existing != "" {
			merged += "\n\n" + existing
		}
		updated, errSet := sjson.SetBytes(payload, path, merged)
		if errSet != nil {
			return payload
		}
		return updated
	}
	items := make([][]byte, 0, len(messages.Array())+1)
	items = append(items, buildSystemMessage(topText))
	for _, message := range messages.Array() {
		items = append(items, []byte(message.Raw))
	}
	updated, errSet := sjson.SetRawBytes(payload, "messages", JoinRawJSONArray(items))
	if errSet != nil {
		return payload
	}
	return updated
}

// buildSystemMessage renders a role=system message carrying plain text.
func buildSystemMessage(text string) []byte {
	out := []byte(`{"role":"system","content":""}`)
	out, _ = sjson.SetBytes(out, "content", text)
	return out
}

// systemContentText renders system content (a string or a block array) as a
// single plain-text string. Non-text blocks are skipped.
func systemContentText(content gjson.Result) string {
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
