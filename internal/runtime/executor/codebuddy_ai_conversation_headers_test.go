package executor

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codebuddy_ai"
)

func TestApplyCodeBuddyAIConversationHeadersShapes(t *testing.T) {
	req := &http.Request{Header: make(http.Header)}
	applyCodeBuddyAIConversationHeaders(req)

	uuidRe := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	hexRe := regexp.MustCompile(`^[0-9a-f]{32}$`)

	conversationID := req.Header.Get("X-Conversation-ID")
	if !uuidRe.MatchString(conversationID) {
		t.Fatalf("X-Conversation-ID is not a dashed UUIDv4: %q", conversationID)
	}

	reqID := req.Header.Get("X-Request-ID")
	if !hexRe.MatchString(reqID) {
		t.Fatalf("X-Request-ID is not 32 lowercase hex chars: %q", reqID)
	}
	for _, name := range []string{"X-Conversation-Request-ID", "X-Root-Request-ID", "X-Conversation-Message-ID"} {
		if got := req.Header.Get(name); got != reqID {
			t.Fatalf("%s = %q, want the same id as X-Request-ID %q", name, got, reqID)
		}
	}
}

func TestApplyCodeBuddyAIConversationHeadersFreshPerCall(t *testing.T) {
	first := &http.Request{Header: make(http.Header)}
	second := &http.Request{Header: make(http.Header)}
	applyCodeBuddyAIConversationHeaders(first)
	applyCodeBuddyAIConversationHeaders(second)

	if first.Header.Get("X-Conversation-ID") == second.Header.Get("X-Conversation-ID") {
		t.Fatalf("X-Conversation-ID must be fresh per call, got %q twice", first.Header.Get("X-Conversation-ID"))
	}
	if first.Header.Get("X-Request-ID") == second.Header.Get("X-Request-ID") {
		t.Fatalf("X-Request-ID must be fresh per call, got %q twice", first.Header.Get("X-Request-ID"))
	}
}

func TestCodeBuddyAIConversationHeadersKeepIDEIdentity(t *testing.T) {
	e := NewCodeBuddyAIExecutor(nil)
	req := &http.Request{Header: make(http.Header)}
	e.applyHeaders(req, "token", "user", "domain")
	applyCodeBuddyAIConversationHeaders(req)

	// The conversation family must not overwrite the existing IDE identity.
	wantUA := codebuddy_ai.WorkBuddyAIUserAgent()
	if got := req.Header.Get("User-Agent"); got != wantUA {
		t.Fatalf("User-Agent changed by conversation headers: got %q, want %q", got, wantUA)
	}
	if got := req.Header.Get("X-IDE-Type"); got != "WorkBuddyAI" {
		t.Fatalf("X-IDE-Type changed by conversation headers: %q", got)
	}
	if got := req.Header.Get("X-Product"); got != "cloud" {
		t.Fatalf("X-Product changed by conversation headers: %q", got)
	}
}
