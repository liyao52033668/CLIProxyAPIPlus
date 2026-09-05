package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const (
	refreshMergeStaleAccess  = "stale-credential-value"
	refreshMergeStaleRefresh = "stale-rotation-value"
	refreshMergeFreshAccess  = "fresh-credential-value"
	refreshMergeFreshRefresh = "rotated-credential-value"
)

type refreshMergeExecutor struct {
	id string
}

func (e *refreshMergeExecutor) Identifier() string {
	return e.id
}

func (e *refreshMergeExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *refreshMergeExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	ch := make(chan cliproxyexecutor.StreamChunk)
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

// Refresh mutates the passed auth in place and returns it, mirroring executors
// that write refreshed credentials directly into auth metadata.
func (e *refreshMergeExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = refreshMergeFreshAccess
	auth.Metadata["refresh_token"] = refreshMergeFreshRefresh
	auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	return auth, nil
}

func (e *refreshMergeExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *refreshMergeExecutor) HTTPRequestWithContext(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *refreshMergeExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *refreshMergeExecutor) CloseExecutionSession(string) {}

// Regression test: refreshBase must be snapshotted before exec.Refresh runs.
// A post-refresh snapshot makes the three-way merge see no executor-side
// metadata changes, silently dropping the refreshed credentials.
func TestRefreshAuthMergesExecutorMetadataChanges(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(&refreshMergeExecutor{id: "fakerefresh"})
	auth := &Auth{
		ID:       "refresh-merge-1",
		Provider: "fakerefresh",
		Metadata: map[string]any{
			"access_token":  refreshMergeStaleAccess,
			"refresh_token": refreshMergeStaleRefresh,
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	updated, err := manager.RefreshAuth(context.Background(), "refresh-merge-1")
	if err != nil {
		t.Fatalf("refresh auth: %v", err)
	}
	if updated == nil {
		t.Fatalf("refresh auth returned nil auth")
	}
	if got := updated.Metadata["access_token"]; got != refreshMergeFreshAccess {
		t.Fatalf("returned auth access_token = %v, want %q", got, refreshMergeFreshAccess)
	}
	current, ok := manager.GetByID("refresh-merge-1")
	if !ok || current == nil {
		t.Fatalf("auth missing from manager after refresh")
	}
	if got := current.Metadata["access_token"]; got != refreshMergeFreshAccess {
		t.Fatalf("manager access_token = %v, want %q", got, refreshMergeFreshAccess)
	}
	if got := current.Metadata["refresh_token"]; got != refreshMergeFreshRefresh {
		t.Fatalf("manager refresh_token = %v, want %q", got, refreshMergeFreshRefresh)
	}
}
