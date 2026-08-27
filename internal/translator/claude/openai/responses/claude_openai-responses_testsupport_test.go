package responses

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

// translateClaudeResponsesStream runs Claude SSE chunks through the streaming
// Responses translator with a single shared state, mirroring how the SDK drives
// the translator over a live stream.
func translateClaudeResponsesStream(chunks [][]byte) [][]byte {
	var param any
	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", nil, nil, chunk, &param)...)
	}
	return outputs
}

// claudeAssistantBlockTypes returns the content block types of the last Claude
// assistant message produced by the Responses -> Claude request translator.
func claudeAssistantBlockTypes(t *testing.T, claudeReq []byte) []string {
	t.Helper()
	var kinds []string
	gjson.GetBytes(claudeReq, "messages").ForEach(func(_, m gjson.Result) bool {
		if m.Get("role").String() != "assistant" {
			return true
		}
		kinds = kinds[:0]
		m.Get("content").ForEach(func(_, b gjson.Result) bool {
			kinds = append(kinds, b.Get("type").String())
			return true
		})
		return true
	})
	return kinds
}

func responsesRequestFromItems(items ...string) []byte {
	raw := `{"model":"claude-test","input":[`
	for i, item := range items {
		if i > 0 {
			raw += ","
		}
		raw += item
	}
	return []byte(raw + `]}`)
}

func mustTestSignature(t *testing.T) string {
	t.Helper()
	raw, _ := testClaudeResponsesThinkingSignature(t)
	return raw
}
