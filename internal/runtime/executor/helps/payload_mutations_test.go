package helps

import (
	"os"
	"strings"
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

func TestFlattenOpenAISystemContentRealTranslatedRequest(t *testing.T) {
	payload, err := os.ReadFile("testdata/codebuddy_translated_request.json")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}

	blocks := gjson.GetBytes(payload, "messages.0.content")
	if !blocks.IsArray() {
		t.Fatalf("precondition: messages.0.content must be a block array")
	}
	parts := make([]string, 0, len(blocks.Array()))
	for _, block := range blocks.Array() {
		if block.Get("type").String() == "text" {
			parts = append(parts, block.Get("text").String())
		}
	}
	expected := strings.Join(parts, "\n\n")
	messageCount := len(gjson.GetBytes(payload, "messages").Array())

	updated := FlattenOpenAISystemContent(payload)

	if got := gjson.GetBytes(updated, "messages.0.content").String(); got != expected {
		t.Fatalf("expected flattened system content, got %d chars want %d", len(got), len(expected))
	}
	if gjson.GetBytes(updated, "messages.0.content").IsArray() {
		t.Fatalf("messages.0.content must be a string after flattening")
	}
	if got := len(gjson.GetBytes(updated, "messages").Array()); got != messageCount {
		t.Fatalf("message count changed: got %d want %d", got, messageCount)
	}
	// Mid-conversation system message with string content must stay intact.
	if got := gjson.GetBytes(updated, "messages.2.content").String(); got != gjson.GetBytes(payload, "messages.2.content").String() {
		t.Fatalf("string system message content changed")
	}
	// User message block arrays must stay untouched.
	if got := gjson.GetBytes(updated, "messages.1.content").Raw; got != gjson.GetBytes(payload, "messages.1.content").Raw {
		t.Fatalf("user message content changed")
	}
}

func TestFlattenOpenAISystemContentRealClientRequest(t *testing.T) {
	payload, err := os.ReadFile("testdata/codebuddy_client_request.json")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}

	topBlocks := gjson.GetBytes(payload, "system")
	if !topBlocks.IsArray() {
		t.Fatalf("precondition: top-level system must be a block array")
	}
	parts := make([]string, 0, len(topBlocks.Array()))
	for _, block := range topBlocks.Array() {
		if block.Get("type").String() == "text" {
			parts = append(parts, block.Get("text").String())
		}
	}
	expected := strings.Join(parts, "\n\n")
	existingMidSystem := gjson.GetBytes(payload, "messages.1.content").String()
	messageCount := len(gjson.GetBytes(payload, "messages").Array())

	updated := FlattenOpenAISystemContent(payload)

	if gjson.GetBytes(updated, "system").Exists() {
		t.Fatalf("top-level system field must be removed")
	}
	// An existing system message is already present mid-conversation, so the
	// top-level system text merges into it instead of being prepended.
	if got := len(gjson.GetBytes(updated, "messages").Array()); got != messageCount {
		t.Fatalf("message count changed: got %d want %d", got, messageCount)
	}
	if got := gjson.GetBytes(updated, "messages.0.role").String(); got != "user" {
		t.Fatalf("first message must stay untouched, got role %q", got)
	}
	if got := gjson.GetBytes(updated, "messages.1.content").String(); got != expected+"\n\n"+existingMidSystem {
		t.Fatalf("expected top-level system merged into existing system message, got %d chars", len(got))
	}
}

func TestFlattenOpenAISystemContentTopLevelMergedIntoSystemMessage(t *testing.T) {
	payload := []byte(`{"model":"glm-5.3","system":"top level prompt",` +
		`"messages":[{"role":"system","content":[{"type":"text","text":"existing"}]},` +
		`{"role":"user","content":"hi"}]}`)
	updated := FlattenOpenAISystemContent(payload)
	if gjson.GetBytes(updated, "system").Exists() {
		t.Fatalf("top-level system field must be removed")
	}
	if got := gjson.GetBytes(updated, "messages.0.content").String(); got != "top level prompt\n\nexisting" {
		t.Fatalf("expected merged system content, got %q", got)
	}
	if got := gjson.GetBytes(updated, "messages.1.role").String(); got != "user" {
		t.Fatalf("user message must keep its position, got %q", got)
	}
}

func TestFlattenOpenAISystemContentTopLevelWithoutSystemMessage(t *testing.T) {
	payload := []byte(`{"system":"top level","messages":[{"role":"user","content":"hi"}]}`)
	updated := FlattenOpenAISystemContent(payload)
	if gjson.GetBytes(updated, "system").Exists() {
		t.Fatalf("top-level system field must be removed")
	}
	if got := gjson.GetBytes(updated, "messages.0.role").String(); got != "system" {
		t.Fatalf("expected prepended system message, got role %q", got)
	}
	if got := gjson.GetBytes(updated, "messages.0.content").String(); got != "top level" {
		t.Fatalf("expected system content, got %q", got)
	}
	if got := gjson.GetBytes(updated, "messages.1.role").String(); got != "user" {
		t.Fatalf("user message must follow system, got role %q", got)
	}
}

func TestFlattenOpenAISystemContentTopLevelBlocks(t *testing.T) {
	payload := []byte(`{"system":[{"type":"text","text":"one"},{"type":"text","text":"two"}],` +
		`"messages":[{"role":"user","content":"hi"}]}`)
	updated := FlattenOpenAISystemContent(payload)
	if gjson.GetBytes(updated, "system").Exists() {
		t.Fatalf("top-level system field must be removed")
	}
	if got := gjson.GetBytes(updated, "messages.0.content").String(); got != "one\n\ntwo" {
		t.Fatalf("expected joined top-level blocks, got %q", got)
	}
}

func TestFlattenOpenAISystemContentTopLevelNoMessages(t *testing.T) {
	payload := []byte(`{"model":"glm-5.3","system":"only system"}`)
	updated := FlattenOpenAISystemContent(payload)
	if gjson.GetBytes(updated, "system").Exists() {
		t.Fatalf("top-level system field must be removed")
	}
	if got := gjson.GetBytes(updated, "messages.0.content").String(); got != "only system" {
		t.Fatalf("expected system message created, got %q", got)
	}
}
