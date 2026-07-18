package helps

import (
	"context"
	"net/http"
	"testing"
)

func TestExtractClaudeCodeSessionIDPrefersHeaderOverPayload(t *testing.T) {
	headers := http.Header{}
	headers.Set(ClaudeCodeSessionHeader, "header-session")
	payload := []byte(`{"metadata":{"user_id":"{\"session_id\":\"payload-session\"}"}}`)

	got := ExtractClaudeCodeSessionID(context.Background(), payload, headers)
	if got != "header-session" {
		t.Fatalf("ExtractClaudeCodeSessionID() = %q, want header-session", got)
	}
}

func TestClaudeCodeExecutionScopeAcceptsLowercaseHeaderMapKeys(t *testing.T) {
	headers := http.Header{
		"x-claude-code-session-id": []string{"lower-session"},
		"x-claude-code-agent-id":   []string{"lower-agent"},
	}

	scope, ok := ClaudeCodeExecutionScope(context.Background(), nil, headers)
	if !ok || scope != "claude:lower-session:agent:lower-agent" {
		t.Fatalf("lowercase header scope = %q, %v", scope, ok)
	}
}

func TestClaudeCodeExecutionScopeIsolatesAgents(t *testing.T) {
	rootHeaders := http.Header{}
	rootHeaders.Set(ClaudeCodeSessionHeader, "session-agents")
	childAHeaders := rootHeaders.Clone()
	childAHeaders.Set(ClaudeCodeAgentHeader, "agent-a")
	childBHeaders := rootHeaders.Clone()
	childBHeaders.Set(ClaudeCodeAgentHeader, "agent-b")

	rootScope, ok := ClaudeCodeExecutionScope(context.Background(), nil, rootHeaders)
	if !ok || rootScope != "claude:session-agents:agent:main" {
		t.Fatalf("root scope = %q, %v", rootScope, ok)
	}
	childAScope, ok := ClaudeCodeExecutionScope(context.Background(), nil, childAHeaders)
	if !ok || childAScope != "claude:session-agents:agent:agent-a" {
		t.Fatalf("child A scope = %q, %v", childAScope, ok)
	}
	childBScope, ok := ClaudeCodeExecutionScope(context.Background(), nil, childBHeaders)
	if !ok || childBScope != "claude:session-agents:agent:agent-b" {
		t.Fatalf("child B scope = %q, %v", childBScope, ok)
	}
	if rootScope == childAScope || childAScope == childBScope || rootScope == childBScope {
		t.Fatalf("agent scopes are not isolated: root=%q a=%q b=%q", rootScope, childAScope, childBScope)
	}
}

func TestClaudeCodePromptCacheDeterministicAndAgentScoped(t *testing.T) {
	rootHeaders := http.Header{}
	rootHeaders.Set(ClaudeCodeSessionHeader, "session-cache-agents")
	childHeaders := rootHeaders.Clone()
	childHeaders.Set(ClaudeCodeAgentHeader, "agent-a")

	rootFirst, ok, errFirst := ClaudeCodePromptCache(context.Background(), "gpt-5.4", nil, rootHeaders)
	if errFirst != nil || !ok {
		t.Fatalf("root first cache = %#v, %v, %v", rootFirst, ok, errFirst)
	}
	rootSecond, ok, errSecond := ClaudeCodePromptCache(context.Background(), "gpt-5.4", nil, rootHeaders)
	if errSecond != nil || !ok || rootSecond.ID != rootFirst.ID {
		t.Fatalf("root second cache = %#v, %v, %v; want ID %q", rootSecond, ok, errSecond, rootFirst.ID)
	}
	child, ok, errChild := ClaudeCodePromptCache(context.Background(), "gpt-5.4", nil, childHeaders)
	if errChild != nil || !ok || child.ID == rootFirst.ID {
		t.Fatalf("child cache = %#v, %v, %v; root ID %q", child, ok, errChild, rootFirst.ID)
	}
	otherModel, ok, errModel := ClaudeCodePromptCache(context.Background(), "gpt-5.5", nil, rootHeaders)
	if errModel != nil || !ok || otherModel.ID == rootFirst.ID {
		t.Fatalf("other model cache = %#v, %v, %v; root ID %q", otherModel, ok, errModel, rootFirst.ID)
	}
}
