package management

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWaitForOAuthCallbackFile_Success(t *testing.T) {
	dir := t.TempDir()
	state := "state-ok"
	provider := "xai"
	RegisterOAuthSession(state, provider)
	t.Cleanup(func() { CompleteOAuthSession(state) })

	payload := map[string]string{
		"code":  "auth-code",
		"state": state,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(dir, ".oauth-xai-state-ok.oauth")
	if errWrite := os.WriteFile(path, data, 0o600); errWrite != nil {
		t.Fatalf("write callback: %v", errWrite)
	}

	got, errWait := waitForOAuthCallbackFile(dir, provider, state, time.Second)
	if errWait != nil {
		t.Fatalf("waitForOAuthCallbackFile: %v", errWait)
	}
	if got.Code != "auth-code" {
		t.Fatalf("code = %q, want auth-code", got.Code)
	}
	if got.State != state {
		t.Fatalf("state = %q, want %q", got.State, state)
	}
}

func TestWaitForOAuthCallbackFile_SessionCancelled(t *testing.T) {
	dir := t.TempDir()
	state := "state-cancel"
	provider := "xai"
	// Do not register session: IsOAuthSessionPending should fail fast.
	_, errWait := waitForOAuthCallbackFile(dir, provider, state, 200*time.Millisecond)
	if !errors.Is(errWait, errOAuthSessionNotPending) {
		t.Fatalf("err = %v, want errOAuthSessionNotPending", errWait)
	}
}

func TestValidateOAuthCallbackPayload(t *testing.T) {
	state := "st"
	RegisterOAuthSession(state, "xai")
	t.Cleanup(func() { CompleteOAuthSession(state) })

	if err := validateOAuthCallbackPayload("xai", state, &oauthCallbackPayload{Code: "c", State: state}, true); err != nil {
		t.Fatalf("valid payload: %v", err)
	}
	if err := validateOAuthCallbackPayload("xai", state, &oauthCallbackPayload{Error: "denied"}, true); err == nil {
		t.Fatal("expected error for callback error field")
	}
	if err := validateOAuthCallbackPayload("xai", state, &oauthCallbackPayload{Code: "", State: state}, true); err == nil {
		t.Fatal("expected error for empty code")
	}
}
