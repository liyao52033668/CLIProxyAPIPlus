package helps

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestEnsureGeminiLeadingUserContent(t *testing.T) {
	modelFirst := []byte(`{"contents":[{"role":"model","parts":[{"text":"answer"}]}]}`)
	got := EnsureGeminiLeadingUserContent(modelFirst, "contents")
	contents := gjson.GetBytes(got, "contents").Array()
	if len(contents) != 2 {
		t.Fatalf("contents length = %d, want 2: %s", len(contents), got)
	}
	if role := contents[0].Get("role").String(); role != "user" {
		t.Fatalf("first role = %q, want user", role)
	}
	if text := contents[0].Get("parts.0.text").String(); text != "" {
		t.Fatalf("prepended text = %q, want empty", text)
	}
	if role := contents[1].Get("role").String(); role != "model" {
		t.Fatalf("original role = %q, want model", role)
	}
}

func TestEnsureGeminiLeadingUserContentLeavesUserFirstPayloadUnchanged(t *testing.T) {
	payload := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
	got := EnsureGeminiLeadingUserContent(payload, "contents")
	if string(got) != string(payload) {
		t.Fatalf("payload changed: got %s, want %s", got, payload)
	}
}
