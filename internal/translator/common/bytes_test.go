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
