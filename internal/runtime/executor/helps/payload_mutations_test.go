package helps

import (
	"os"
	"strconv"
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

func TestMoveOpenAISystemToUserMessage(t *testing.T) {
	payload := []byte(`{"model":"glm-5.3","messages":[` +
		`{"role":"system","content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]},` +
		`{"role":"user","content":[{"type":"text","text":"hello"}]},` +
		`{"role":"system","content":"plain system"}]}`)
	updated := MoveOpenAISystemToUserMessage(payload)

	// Leading system message is removed; its text is prepended to the user.
	if got := gjson.GetBytes(updated, "messages.0.role").String(); got != "user" {
		t.Fatalf("leading system must be removed, got role %q", got)
	}
	if got := gjson.GetBytes(updated, "messages.0.content.0").Raw; got != `{"type":"text","text":"first\n\nsecond"}` {
		t.Fatalf("expected prepended text block, got %s", got)
	}
	if got := gjson.GetBytes(updated, "messages.0.content.1.text").String(); got != "hello" {
		t.Fatalf("original user text must follow, got %q", got)
	}
	// Mid-conversation system stays in place.
	if got := gjson.GetBytes(updated, "messages.1.role").String(); got != "system" {
		t.Fatalf("mid-conversation system must stay, got role %q", got)
	}
	if got := gjson.GetBytes(updated, "messages.1.content").String(); got != "plain system" {
		t.Fatalf("string system content must stay untouched, got %q", got)
	}
	if got := len(gjson.GetBytes(updated, "messages").Array()); got != 2 {
		t.Fatalf("expected 2 messages, got %d", got)
	}
}

func TestMoveOpenAISystemToUserMessageMidSystemFlattened(t *testing.T) {
	payload := []byte(`{"messages":[` +
		`{"role":"user","content":"hi"},` +
		`{"role":"system","content":[{"type":"text","text":"keep"},` +
		`{"type":"image","source":"data"}]}]}`)
	updated := MoveOpenAISystemToUserMessage(payload)
	if got := gjson.GetBytes(updated, "messages.1.role").String(); got != "system" {
		t.Fatalf("mid-conversation system must stay, got role %q", got)
	}
	if got := gjson.GetBytes(updated, "messages.1.content").String(); got != "keep" {
		t.Fatalf("expected only text blocks joined, got %q", got)
	}
}

func TestMoveOpenAISystemToUserMessageRealTranslatedRequest(t *testing.T) {
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
	originalUserBlocks := gjson.GetBytes(payload, "messages.1.content")

	updated := MoveOpenAISystemToUserMessage(payload)

	// Leading system message is gone; the first message is now the user.
	if got := gjson.GetBytes(updated, "messages.0.role").String(); got != "user" {
		t.Fatalf("leading system must be removed, got role %q", got)
	}
	if got := gjson.GetBytes(updated, "messages.0.content.0.text").String(); got != expected {
		t.Fatalf("expected prepended system text, got %d chars want %d", len(got), len(expected))
	}
	// The original user blocks follow the prepended block.
	for i, block := range originalUserBlocks.Array() {
		if got := gjson.GetBytes(updated, "messages.0.content."+strconv.Itoa(i+1)).Raw; got != block.Raw {
			t.Fatalf("original user block %d changed", i)
		}
	}
	// Mid-conversation string system message stays.
	if got := gjson.GetBytes(updated, "messages.1.role").String(); got != "system" {
		t.Fatalf("mid-conversation system must stay, got role %q", got)
	}
}

func TestMoveOpenAISystemToUserMessageRealClientRequest(t *testing.T) {
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
	messageCount := len(gjson.GetBytes(payload, "messages").Array())

	updated := MoveOpenAISystemToUserMessage(payload)

	if gjson.GetBytes(updated, "system").Exists() {
		t.Fatalf("top-level system field must be removed")
	}
	// No leading system message; message count unchanged.
	if got := len(gjson.GetBytes(updated, "messages").Array()); got != messageCount {
		t.Fatalf("message count changed: got %d want %d", got, messageCount)
	}
	if got := gjson.GetBytes(updated, "messages.0.role").String(); got != "user" {
		t.Fatalf("first message must be user, got role %q", got)
	}
	// Top-level system text is prepended as a block into the first user message.
	if got := gjson.GetBytes(updated, "messages.0.content.0.text").String(); got != expected {
		t.Fatalf("expected prepended system text, got %d chars want %d", len(got), len(expected))
	}
}

func TestMoveOpenAISystemToUserMessageTopLevelOnly(t *testing.T) {
	payload := []byte(`{"system":"top level","messages":[{"role":"user","content":"hi"}]}`)
	updated := MoveOpenAISystemToUserMessage(payload)
	if gjson.GetBytes(updated, "system").Exists() {
		t.Fatalf("top-level system field must be removed")
	}
	if got := gjson.GetBytes(updated, "messages.0.role").String(); got != "user" {
		t.Fatalf("expected user message, got role %q", got)
	}
	if got := gjson.GetBytes(updated, "messages.0.content.0.text").String(); got != "top level" {
		t.Fatalf("expected prepended system text, got %q", got)
	}
	if got := gjson.GetBytes(updated, "messages.0.content.1.text").String(); got != "hi" {
		t.Fatalf("original user text must follow, got %q", got)
	}
}

func TestMoveOpenAISystemToUserMessageNoUserMessage(t *testing.T) {
	payload := []byte(`{"system":"top level","messages":[{"role":"system","content":"sys"}]}`)
	updated := MoveOpenAISystemToUserMessage(payload)
	// No user message to absorb the text, so the system message stays.
	if got := gjson.GetBytes(updated, "messages.0.role").String(); got != "system" {
		t.Fatalf("system message must stay when no user exists, got role %q", got)
	}
	if got := gjson.GetBytes(updated, "messages.0.content").String(); got != "sys" {
		t.Fatalf("system content must stay untouched, got %q", got)
	}
	if got := gjson.GetBytes(updated, "system").String(); got != "top level" {
		t.Fatalf("top-level system must be preserved when it cannot be moved, got %q", got)
	}
}

func TestMoveOpenAISystemToUserMessageNoChanges(t *testing.T) {
	payload := []byte(`{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`)
	if got := MoveOpenAISystemToUserMessage(payload); string(got) != string(payload) {
		t.Fatalf("expected payload unchanged, got %s", string(got))
	}
}

func TestEnsureOpenAILeadingSystemMessageAlreadyLeading(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"}]}`)
	if got := EnsureOpenAILeadingSystemMessage(payload, "default"); string(got) != string(payload) {
		t.Fatalf("expected payload unchanged, got %s", string(got))
	}
}

func TestEnsureOpenAILeadingSystemMessageLeadingFlattened(t *testing.T) {
	payload := []byte(`{"messages":[` +
		`{"role":"system","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]},` +
		`{"role":"user","content":"hi"}]}`)
	updated := EnsureOpenAILeadingSystemMessage(payload, "default")
	if got := gjson.GetBytes(updated, "messages.0.content").String(); got != "a\n\nb" {
		t.Fatalf("expected flattened string system content, got %q", got)
	}
	if got := gjson.GetBytes(updated, "messages.1.role").String(); got != "user" {
		t.Fatalf("user message must stay in place, got role %q", got)
	}
}

func TestEnsureOpenAILeadingSystemMessagePrependsDefault(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	updated := EnsureOpenAILeadingSystemMessage(payload, "You are CodeBuddy.")
	if got := gjson.GetBytes(updated, "messages.0.role").String(); got != "system" {
		t.Fatalf("first message must be system, got role %q", got)
	}
	if got := gjson.GetBytes(updated, "messages.0.content").String(); got != "You are CodeBuddy." {
		t.Fatalf("expected default system prompt, got %q", got)
	}
	if got := gjson.GetBytes(updated, "messages.1.content").String(); got != "hi" {
		t.Fatalf("user message must be preserved, got %q", got)
	}
}

func TestEnsureOpenAILeadingSystemMessagePromotesTopLevelSystem(t *testing.T) {
	payload := []byte(`{"system":"top level","messages":[{"role":"user","content":"hi"}]}`)
	updated := EnsureOpenAILeadingSystemMessage(payload, "default")
	if gjson.GetBytes(updated, "system").Exists() {
		t.Fatalf("top-level system field must be removed")
	}
	if got := gjson.GetBytes(updated, "messages.0.role").String(); got != "system" {
		t.Fatalf("first message must be system, got role %q", got)
	}
	if got := gjson.GetBytes(updated, "messages.0.content").String(); got != "top level" {
		t.Fatalf("expected promoted top-level system text, got %q", got)
	}
	if got := gjson.GetBytes(updated, "messages.1.content").String(); got != "hi" {
		t.Fatalf("user message must be preserved, got %q", got)
	}
}

func TestEnsureOpenAILeadingSystemMessageMidSystemFlattened(t *testing.T) {
	payload := []byte(`{"messages":[` +
		`{"role":"user","content":"hi"},` +
		`{"role":"system","content":[{"type":"text","text":"note"},{"type":"image","source":"data"}]}]}`)
	updated := EnsureOpenAILeadingSystemMessage(payload, "default")
	if got := gjson.GetBytes(updated, "messages.0.role").String(); got != "system" {
		t.Fatalf("default system must be prepended, got role %q", got)
	}
	if got := gjson.GetBytes(updated, "messages.1.content").String(); got != "hi" {
		t.Fatalf("user message must be preserved, got %q", got)
	}
	if got := gjson.GetBytes(updated, "messages.2.content").String(); got != "note" {
		t.Fatalf("mid-conversation system must be flattened, got %q", got)
	}
}

func TestEnsureOpenAILeadingSystemMessageNoMessages(t *testing.T) {
	payload := []byte(`{"model":"m"}`)
	if got := EnsureOpenAILeadingSystemMessage(payload, "default"); string(got) != string(payload) {
		t.Fatalf("expected payload unchanged, got %s", string(got))
	}
}
