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

// FlattenOpenAISystemContent rewrites system-role messages whose content is a
// block array into a plain text string. The CodeBuddy upstream rejects
// block-array content on system messages while accepting string content.
func FlattenOpenAISystemContent(payload []byte) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return payload
	}
	for index, message := range messages.Array() {
		if message.Get("role").String() != "system" {
			continue
		}
		text, ok := systemContentText(message.Get("content"))
		if !ok {
			continue
		}
		updated, errSet := sjson.SetBytes(payload, "messages."+strconv.Itoa(index)+".content", text)
		if errSet != nil {
			return payload
		}
		payload = updated
	}
	return payload
}

// systemContentText joins the text blocks of a system message content array.
// The ok result is false when the content is not a block array, leaving the
// message untouched.
func systemContentText(content gjson.Result) (string, bool) {
	if !content.IsArray() || len(content.Array()) == 0 {
		return "", false
	}
	parts := make([]string, 0, len(content.Array()))
	for _, block := range content.Array() {
		if block.Get("type").String() != "text" {
			continue
		}
		parts = append(parts, block.Get("text").String())
	}
	return strings.Join(parts, "\n\n"), true
}
