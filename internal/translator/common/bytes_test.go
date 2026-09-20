package common

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestTextFromContentBlocks_StringifiedContentBlocksExtractText(t *testing.T) {
	input := `"[{\"type\":\"text\",\"text\":\"hello\"},{\"type\":\"output_text\",\"text\":\" world\"}]"`

	got := TextFromContentBlocks(gjson.Parse(input))
	if got != "hello world" {
		t.Fatalf("text = %q, want hello world", got)
	}
}

func TestTextFromContentBlocks_PlainStringPassesThrough(t *testing.T) {
	got := TextFromContentBlocks(gjson.Parse(`"hello"`))
	if got != "hello" {
		t.Fatalf("text = %q, want hello", got)
	}
}

func TestTextFromContentBlocks_NestedStringifiedContentBlocksExtractText(t *testing.T) {
	input := `[{"type":"text","text":"[{\"type\":\"text\",\"text\":\"代码修复已完成，我会先 gofmt，再运行新增测试验证 GREEN。\"}]"}]`

	got := TextFromContentBlocks(gjson.Parse(input))
	want := "代码修复已完成，我会先 gofmt，再运行新增测试验证 GREEN。"
	if got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

func TestSetStringWithoutHTMLEscape(t *testing.T) {
	tests := []struct {
		name  string
		data  string
		path  string
		value string
		want  string
	}{
		{
			name:  "command with redirection and pipe",
			data:  `{"arguments":""}`,
			path:  "arguments",
			value: `gh issue view 5802 --json number,title,body,url,state,labels,assignees 2>&1 | head -100`,
			want:  `{"arguments":"gh issue view 5802 --json number,title,body,url,state,labels,assignees 2>&1 | head -100"}`,
		},
		{
			name:  "json string arguments containing redirection",
			data:  `{"arguments":""}`,
			path:  "arguments",
			value: `{"command": "gh issue view 5802 2>&1 | head -100", "timeout": 60}`,
			want:  `{"arguments":"{\"command\": \"gh issue view 5802 2>&1 | head -100\", \"timeout\": 60}"}`,
		},
		{
			name:  "html tags preserved without escaping",
			data:  `{"type":"function_call","name":"bash","arguments":""}`,
			path:  "arguments",
			value: `{"html": "<tag>&value</tag>"}`,
			want:  `{"type":"function_call","name":"bash","arguments":"{\"html\": \"<tag>&value</tag>\"}"}`,
		},
		{
			name:  "nested path",
			data:  `{"item":{"arguments":""}}`,
			path:  "item.arguments",
			value: `2>&1`,
			want:  `{"item":{"arguments":"2>&1"}}`,
		},
		{
			name:  "empty string",
			data:  `{"arguments":"old"}`,
			path:  "arguments",
			value: ``,
			want:  `{"arguments":""}`,
		},
		{
			name:  "newlines tabs and backslashes",
			data:  `{"arguments":""}`,
			path:  "arguments",
			value: "line1\nline2\t\\path\\to\\file",
			want:  "{\"arguments\":\"line1\\nline2\\t\\\\path\\\\to\\\\file\"}",
		},
		{
			name:  "unicode and emoji",
			data:  `{"arguments":""}`,
			path:  "arguments",
			value: `你好，世界！🚀 <&>`,
			want:  `{"arguments":"你好，世界！🚀 <&>"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, errSet := SetStringWithoutHTMLEscape([]byte(tc.data), tc.path, tc.value)
			if errSet != nil {
				t.Fatalf("SetStringWithoutHTMLEscape() error: %v", errSet)
			}
			if string(got) != tc.want {
				t.Fatalf("SetStringWithoutHTMLEscape() = %s, want %s", string(got), tc.want)
			}

			// Verify that the JSON parses back and decodes to the exact same value (round-trip)
			res := gjson.GetBytes(got, tc.path)
			if !res.Exists() {
				t.Fatalf("path %q does not exist in result: %s", tc.path, string(got))
			}
			if res.String() != tc.value {
				t.Fatalf("round-trip value mismatch: got %q, want %q", res.String(), tc.value)
			}
		})
	}
}
