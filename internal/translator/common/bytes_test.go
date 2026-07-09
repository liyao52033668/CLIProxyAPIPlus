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
