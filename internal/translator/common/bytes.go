package common

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type ContentBlockTextBuffer struct {
	pending strings.Builder
}

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
	for range trailingNewlines {
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
	for range trailingNewlines {
		out = append(out, '\n')
	}
	return out
}
