package executor

import (
	"strings"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestShouldStripCodeBuddyReasoning(t *testing.T) {
	tests := []struct {
		name            string
		from            sdktranslator.Format
		originalRequest string
		model           string
		want            bool
	}{
		{
			name:            "claude request without thinking config strips",
			from:            sdktranslator.FormatClaude,
			originalRequest: `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`,
			model:           "glm-5.3",
			want:            true,
		},
		{
			name:            "claude request with disabled thinking strips",
			from:            sdktranslator.FormatClaude,
			originalRequest: `{"model":"glm-5.3","thinking":{"type":"disabled"},"messages":[]}`,
			model:           "glm-5.3",
			want:            true,
		},
		{
			name:            "claude request with enabled thinking keeps reasoning",
			from:            sdktranslator.FormatClaude,
			originalRequest: `{"model":"glm-5.3","thinking":{"type":"enabled","budget_tokens":8000},"messages":[]}`,
			model:           "glm-5.3",
			want:            false,
		},
		{
			name:            "claude request with none suffix strips",
			from:            sdktranslator.FormatClaude,
			originalRequest: `{"model":"glm-5.3","messages":[]}`,
			model:           "glm-5.3(none)",
			want:            true,
		},
		{
			name:            "claude request with high suffix keeps reasoning",
			from:            sdktranslator.FormatClaude,
			originalRequest: `{"model":"glm-5.3","messages":[]}`,
			model:           "glm-5.3(high)",
			want:            false,
		},
		{
			name:            "non-claude format keeps reasoning",
			from:            sdktranslator.FormatOpenAI,
			originalRequest: `{"model":"glm-5.3","messages":[]}`,
			model:           "glm-5.3",
			want:            false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldStripCodeBuddyReasoning(tt.from, []byte(tt.originalRequest), tt.model)
			if got != tt.want {
				t.Fatalf("shouldStripCodeBuddyReasoning = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStripOpenAIReasoningNonStreaming(t *testing.T) {
	payload := []byte(`{"id":"cmb-1","model":"glm-5.3","choices":[{"index":0,"message":{"role":"assistant","content":"answer","reasoning_content":"thoughts"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`)
	out := stripOpenAIReasoning(payload)
	if gjson.GetBytes(out, "choices.0.message.reasoning_content").Exists() {
		t.Fatalf("reasoning_content not stripped: %s", string(out))
	}
	if content := gjson.GetBytes(out, "choices.0.message.content").String(); content != "answer" {
		t.Fatalf("content changed: %q", content)
	}
	if finish := gjson.GetBytes(out, "choices.0.finish_reason").String(); finish != "stop" {
		t.Fatalf("finish_reason changed: %q", finish)
	}
}

func TestStripOpenAIReasoningStreamingDelta(t *testing.T) {
	payload := []byte(`{"id":"cmb-1","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"thinking chunk"}}]}`)
	out := stripOpenAIReasoning(payload)
	if gjson.GetBytes(out, "choices.0.delta.reasoning_content").Exists() {
		t.Fatalf("delta reasoning_content not stripped: %s", string(out))
	}
	if role := gjson.GetBytes(out, "choices.0.delta.role").String(); role != "assistant" {
		t.Fatalf("delta role changed: %q", role)
	}
}

func TestStripOpenAIReasoningContentBlocks(t *testing.T) {
	payload := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"reasoning","text":"hidden"},{"type":"text","text":"shown"}]}}]}`)
	out := stripOpenAIReasoning(payload)
	content := gjson.GetBytes(out, "choices.0.message.content")
	if !content.IsArray() || len(content.Array()) != 1 {
		t.Fatalf("content array not filtered: %s", string(out))
	}
	if got := content.Array()[0].Get("type").String(); got != "text" {
		t.Fatalf("unexpected surviving block type: %q", got)
	}
}

func TestStripOpenAIReasoningNoReasoningUnchanged(t *testing.T) {
	payload := []byte(`{"id":"cmb-1","choices":[{"index":0,"message":{"role":"assistant","content":"plain"}}]}`)
	out := stripOpenAIReasoning(payload)
	if string(out) != string(payload) {
		t.Fatalf("payload modified without reasoning present: %s", string(out))
	}
}

func TestStripCodeBuddyReasoningLine(t *testing.T) {
	tests := []struct {
		name           string
		line           string
		wantReasoning  bool
		wantUnchanged  bool
		wantPrefixData bool
	}{
		{
			name:           "data line with reasoning stripped",
			line:           `data: {"choices":[{"index":0,"delta":{"content":"hi","reasoning_content":"thought"}}]}`,
			wantReasoning:  false,
			wantPrefixData: true,
		},
		{
			name:          "data line without reasoning unchanged",
			line:          `data: {"choices":[{"index":0,"delta":{"content":"hi"}}]}`,
			wantReasoning: false,
			wantUnchanged: true,
		},
		{
			name:          "done marker unchanged",
			line:          `data: [DONE]`,
			wantUnchanged: true,
		},
		{
			name:          "non-data line unchanged",
			line:          `event: error`,
			wantUnchanged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := stripCodeBuddyReasoningLine([]byte(tt.line))
			if tt.wantUnchanged {
				if string(out) != tt.line {
					t.Fatalf("line modified: %q", string(out))
				}
				return
			}
			if strings.Contains(string(out), "reasoning_content") != tt.wantReasoning {
				t.Fatalf("reasoning presence mismatch: %q", string(out))
			}
			if tt.wantPrefixData && !strings.HasPrefix(string(out), "data: ") {
				t.Fatalf("data prefix lost: %q", string(out))
			}
		})
	}
}

func TestCodeBuddyAggregateThenStripReasoning(t *testing.T) {
	raw := strings.Join([]string{
		`data: {"id":"cmb-1","model":"glm-5.3","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"thinking..."}}]}`,
		`data: {"id":"cmb-1","model":"glm-5.3","choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}, "\n")

	aggregated, _, err := aggregateOpenAIChatCompletionStream([]byte(raw))
	if err != nil {
		t.Fatalf("aggregateOpenAIChatCompletionStream error: %v", err)
	}
	if !gjson.GetBytes(aggregated, "choices.0.message.reasoning_content").Exists() {
		t.Fatalf("aggregation lost reasoning_content: %s", string(aggregated))
	}

	stripped := stripOpenAIReasoning(aggregated)
	if gjson.GetBytes(stripped, "choices.0.message.reasoning_content").Exists() {
		t.Fatalf("reasoning_content survived strip: %s", string(stripped))
	}
	if content := gjson.GetBytes(stripped, "choices.0.message.content").String(); content != "answer" {
		t.Fatalf("content changed after strip: %q", content)
	}
}
