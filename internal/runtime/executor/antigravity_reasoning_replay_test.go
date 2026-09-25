package executor

import (
	"context"
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestAntigravityReasoningReplayScopeSupportsUnderscoreSessionIDHeader(t *testing.T) {
	opts := cliproxyexecutor.Options{
		Headers:  http.Header{"Session_id": []string{"underscore-session"}},
		Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "socket-uuid"},
	}
	scope := antigravityReasoningReplayScopeFromRequest(context.Background(), "gemini-3.6-flash-high", cliproxyexecutor.Request{}, opts, nil)
	if got := scope.sessionKey; got != "responses:underscore-session" {
		t.Fatalf("session key = %q, want stable Responses session from Session_id header", got)
	}
}
