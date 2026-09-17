package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func newOAuthSessionCancelRequest(t *testing.T, target string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodDelete, target, nil)
	return ctx, rec
}

func TestDeleteOAuthSessionCancelsPendingSession(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	state := "cancel-handler-pending"
	RegisterOAuthSession(state, "devin")
	t.Cleanup(func() { CancelOAuthSession(state) })

	ctx, rec := newOAuthSessionCancelRequest(t, "/v0/management/oauth-session?state="+state)
	h.DeleteOAuthSession(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Status    string `json:"status"`
		Cancelled bool   `json:"cancelled"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Status != "ok" || !payload.Cancelled {
		t.Fatalf("payload = %+v, want status=ok cancelled=true", payload)
	}
	if IsOAuthSessionPending(state, "devin") {
		t.Fatal("session is still pending after cancel")
	}
}

func TestDeleteOAuthSessionReportsUnknownStateAsNotCancelled(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)

	ctx, rec := newOAuthSessionCancelRequest(t, "/v0/management/oauth-session?state=cancel-handler-unknown")
	h.DeleteOAuthSession(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Status    string `json:"status"`
		Cancelled bool   `json:"cancelled"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Status != "ok" || payload.Cancelled {
		t.Fatalf("payload = %+v, want status=ok cancelled=false", payload)
	}
}

func TestDeleteOAuthSessionRejectsMissingOrInvalidState(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)

	for _, target := range []string{
		"/v0/management/oauth-session",
		"/v0/management/oauth-session?state=",
		"/v0/management/oauth-session?state=bad/state",
	} {
		ctx, rec := newOAuthSessionCancelRequest(t, target)
		h.DeleteOAuthSession(ctx)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want %d", target, rec.Code, http.StatusBadRequest)
		}
	}
}
