package helps

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestSetStringIfDifferent(t *testing.T) {
	payload := []byte(`{"a":"b"}`)
	updated := SetStringIfDifferent(payload, "a", "b")
	if string(updated) != `{"a":"b"}` {
		t.Fatalf("expected no change, got %s", string(updated))
	}
	updated = SetStringIfDifferent(payload, "a", "c")
	if got := gjson.GetBytes(updated, "a").String(); got != "c" {
		t.Fatalf("expected a=c, got %q", got)
	}
}

func TestSetBoolIfDifferent(t *testing.T) {
	payload := []byte(`{"a":true}`)
	updated := SetBoolIfDifferent(payload, "a", true)
	if string(updated) != `{"a":true}` {
		t.Fatalf("expected no change, got %s", string(updated))
	}
	updated = SetBoolIfDifferent(payload, "a", false)
	if got := gjson.GetBytes(updated, "a").Bool(); got {
		t.Fatalf("expected a=false")
	}
}

func TestFlattenOpenAISystemContent(t *testing.T) {
	payload := []byte(`{"model":"glm-5.3","messages":[` +
		`{"role":"system","content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]},` +
		`{"role":"user","content":[{"type":"text","text":"hello"}]},` +
		`{"role":"system","content":"plain system"}]}`)
	updated := FlattenOpenAISystemContent(payload)
	if got := gjson.GetBytes(updated, "messages.0.content").String(); got != "first\n\nsecond" {
		t.Fatalf("expected joined system text, got %q", got)
	}
	if got := gjson.GetBytes(updated, "messages.1.content").Raw; got != `[{"type":"text","text":"hello"}]` {
		t.Fatalf("user content must stay untouched, got %s", got)
	}
	if got := gjson.GetBytes(updated, "messages.2.content").String(); got != "plain system" {
		t.Fatalf("string system content must stay untouched, got %q", got)
	}
}

func TestFlattenOpenAISystemContentKeepsNonTextBlocks(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"system","content":[{"type":"text","text":"keep"},` +
		`{"type":"image","source":"data"}]}]}`)
	updated := FlattenOpenAISystemContent(payload)
	if got := gjson.GetBytes(updated, "messages.0.content").String(); got != "keep" {
		t.Fatalf("expected only text blocks joined, got %q", got)
	}
}

func TestFlattenOpenAISystemContentNoMessages(t *testing.T) {
	payload := []byte(`{"model":"glm-5.3"}`)
	if got := FlattenOpenAISystemContent(payload); string(got) != string(payload) {
		t.Fatalf("expected payload unchanged, got %s", string(got))
	}
}
