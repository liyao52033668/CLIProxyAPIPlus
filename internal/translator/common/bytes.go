package common

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ContentBlockTextBuffer accumulates streamed text fragments and transparently
// decodes content-block arrays that some providers stringify on the wire.
type ContentBlockTextBuffer struct {
	pending strings.Builder
}

// Text returns the decoded text for the given JSON result. When the result
// looks like a partial content-block array the fragment is buffered until a
// subsequent call completes it.
func (b *ContentBlockTextBuffer) Text(result gjson.Result) string {
	if !result.Exists() || result.Type == gjson.Null {
		return ""
	}
	if result.Type != gjson.String {
		return TextFromContentBlocks(result)
	}
	text := result.String()
	if b.pending.Len() > 0 {
		b.pending.WriteString(text)
		return b.flushIfComplete()
	}
	trimmed := strings.TrimLeft(text, " \t\r\n")
	if strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{") {
		if parsed := gjson.Parse(text); gjson.Valid(text) && (parsed.IsArray() || parsed.IsObject()) {
			if parsedText, ok := textFromContentBlocks(parsed); ok {
				return parsedText
			}
		}
		if looksLikeContentBlocksPrefix(trimmed) {
			b.pending.WriteString(text)
			return b.flushIfComplete()
		}
	}
	return text
}

// Flush drains any buffered partial content-block fragment.
func (b *ContentBlockTextBuffer) Flush() string {
	if b.pending.Len() == 0 {
		return ""
	}
	text := b.pending.String()
	b.pending.Reset()
	return text
}

func (b *ContentBlockTextBuffer) flushIfComplete() string {
	text := b.pending.String()
	if !gjson.Valid(text) {
		return ""
	}
	parsed := gjson.Parse(text)
	if !parsed.IsArray() && !parsed.IsObject() {
		return b.Flush()
	}
	if parsedText, ok := textFromContentBlocks(parsed); ok {
		b.pending.Reset()
		return parsedText
	}
	return b.Flush()
}

func looksLikeContentBlocksPrefix(text string) bool {
	return strings.Contains(text, `"type"`) || strings.Contains(text, `"text"`) || strings.Contains(text, `"output_text"`)
}

// TextFromContentBlocks extracts plain text from a gjson result that may be a
// content-block array, a stringified content-block array, or a plain string.
func TextFromContentBlocks(result gjson.Result) string {
	if !result.Exists() || result.Type == gjson.Null {
		return ""
	}
	if result.Type == gjson.String {
		text := result.String()
		if parsed := gjson.Parse(text); gjson.Valid(text) && (parsed.IsArray() || parsed.IsObject()) {
			if parsedText, ok := textFromContentBlocks(parsed); ok {
				return parsedText
			}
		}
		return text
	}
	if text, ok := textFromContentBlocks(result); ok {
		return text
	}
	return result.String()
}

func textFromContentBlocks(result gjson.Result) (string, bool) {
	if result.Type == gjson.String {
		return textFromPossiblyStringifiedContentBlocks(result.String()), true
	}
	if result.IsArray() {
		var builder strings.Builder
		found := false
		result.ForEach(func(_, part gjson.Result) bool {
			if text, ok := textFromContentBlocks(part); ok {
				builder.WriteString(text)
				found = true
			}
			return true
		})
		return builder.String(), found
	}
	switch result.Get("type").String() {
	case "text", "output_text":
		return textFromPossiblyStringifiedContentBlocks(result.Get("text").String()), true
	}
	return "", false
}

func textFromPossiblyStringifiedContentBlocks(text string) string {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	if !strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "{") {
		return text
	}
	if !gjson.Valid(text) {
		return text
	}
	parsed := gjson.Parse(text)
	if !parsed.IsArray() && !parsed.IsObject() {
		return text
	}
	if parsedText, ok := textFromContentBlocks(parsed); ok {
		return parsedText
	}
	return text
}

// WrapGeminiCLIResponse wraps a Gemini CLI response body inside a
// {"response": ...} envelope expected by downstream translators.
func WrapGeminiCLIResponse(response []byte) []byte {
	out, err := sjson.SetRawBytes([]byte(`{"response":{}}`), "response", response)
	if err != nil {
		return response
	}
	return out
}

func GeminiTokenCountJSON(count int64) []byte {
	out := make([]byte, 0, 96)
	out = append(out, `{"totalTokens":`...)
	out = strconv.AppendInt(out, count, 10)
	out = append(out, `,"promptTokensDetails":[{"modality":"TEXT","tokenCount":`...)
	out = strconv.AppendInt(out, count, 10)
	out = append(out, `}]}`...)
	return out
}

func ClaudeInputTokensJSON(count int64) []byte {
	out := make([]byte, 0, 32)
	out = append(out, `{"input_tokens":`...)
	out = strconv.AppendInt(out, count, 10)
	out = append(out, '}')
	return out
}

// NewRawArrayItems creates a raw item slice sized for the expected input.
func NewRawArrayItems(capacity int64) [][]byte {
	if capacity <= 0 {
		return nil
	}
	return make([][]byte, 0, int(capacity))
}

func JoinRawArray(items [][]byte) []byte {
	if len(items) == 0 {
		return []byte("[]")
	}
	size := len(items) + 1
	for _, item := range items {
		size += len(item)
	}
	out := make([]byte, 0, size)
	out = append(out, '[')
	for i, item := range items {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, item...)
	}
	return append(out, ']')
}

// SetRawArrayItems replaces an empty JSON array at path with raw items.
// The single-item path avoids allocating an intermediate joined array.
func SetRawArrayItems(data []byte, path string, items [][]byte) []byte {
	if len(items) == 0 {
		return data
	}
	if len(items) == 1 {
		array := gjson.GetBytes(data, path)
		if array.Raw == "[]" && array.Index >= 0 && array.Index+len(array.Raw) <= len(data) {
			out := make([]byte, 0, len(data)+len(items[0]))
			out = append(out, data[:array.Index]...)
			out = append(out, '[')
			out = append(out, items[0]...)
			out = append(out, ']')
			return append(out, data[array.Index+len(array.Raw):]...)
		}
	}
	data, _ = sjson.SetRawBytes(data, path, JoinRawArray(items))
	return data
}

func SSEEventData(event string, payload []byte) []byte {
	out := make([]byte, 0, len(event)+len(payload)+14)
	out = append(out, "event: "...)
	out = append(out, event...)
	out = append(out, '\n')
	out = append(out, "data: "...)
	out = append(out, payload...)
	return out
}

func AppendSSEEventString(out []byte, event, payload string, trailingNewlines int) []byte {
	out = append(out, "event: "...)
	out = append(out, event...)
	out = append(out, '\n')
	out = append(out, "data: "...)
	out = append(out, payload...)
	for i := 0; i < trailingNewlines; i++ {
		out = append(out, '\n')
	}
	return out
}

func AppendSSEEventBytes(out []byte, event string, payload []byte, trailingNewlines int) []byte {
	out = append(out, "event: "...)
	out = append(out, event...)
	out = append(out, '\n')
	out = append(out, "data: "...)
	out = append(out, payload...)
	for i := 0; i < trailingNewlines; i++ {
		out = append(out, '\n')
	}
	return out
}
