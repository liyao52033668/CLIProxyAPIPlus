package codearts

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestRegisterRoutesProtectsCodeArtsSetup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewOAuthWebHandler(&config.Config{})
	handler.RegisterRoutes(router, func(c *gin.Context) {
		c.AbortWithStatus(http.StatusUnauthorized)
	})

	for _, path := range []string{"/v0/oauth/codearts", "/v0/oauth/codearts/start"} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		router.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s status = %d, want %d", path, recorder.Code, http.StatusUnauthorized)
		}
	}

	for _, path := range []string{"/v0/oauth/codearts/status?state=missing", "/v0/oauth/codearts/callback"} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		router.ServeHTTP(recorder, req)
		if recorder.Code == http.StatusUnauthorized {
			t.Fatalf("%s unexpectedly required setup authentication", path)
		}
	}
}

// newTestSession registers a session without starting the background poller.
func newTestSession(handler *OAuthWebHandler, stateID, ticketID, secret string) *webSession {
	sess := &webSession{
		stateID:   stateID,
		ticketID:  ticketID,
		status:    sWaitingCB,
		startedAt: time.Now(),
	}
	sess.pollSecret.Store(secret)
	handler.sessions[stateID] = sess
	handler.ticketToState[ticketID] = stateID
	return sess
}

func callbackContext(t *testing.T, target string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, target, nil)
	return ctx, recorder
}

func TestCodeArtsCallbackRejectsUnknownTicket(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewOAuthWebHandler(&config.Config{})
	sess := newTestSession(handler, "state-1", "ticket-1", "local-secret")

	redirect := url.QueryEscape("https://codearts.huaweicloud.com/portal/callback?ticket_id=wrong-ticket")
	ctx, recorder := callbackContext(t, "/oauth/callback?secret=portal-secret&redirect="+redirect)

	handler.handleCallback(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	if sess.status != sWaitingCB {
		t.Fatalf("session status = %s, want %s", sess.status, sWaitingCB)
	}
	if got := sess.currentSecret(); got != "local-secret" {
		t.Fatalf("session secret = %q, want the untouched local secret", got)
	}
}

func TestCodeArtsCallbackStoresSecretAndBouncesBrowser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewOAuthWebHandler(&config.Config{})
	sess := newTestSession(handler, "state-1", "ticket-1", "local-secret")

	redirectTarget := "https://codearts.huaweicloud.com/portal/callback?ticket_id=ticket-1"
	ctx, recorder := callbackContext(t, "/oauth/callback?secret=portal-secret&redirect="+url.QueryEscape(redirectTarget))

	handler.handleCallback(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if got := sess.currentSecret(); got != "portal-secret" {
		t.Fatalf("session secret = %q, want %q", got, "portal-secret")
	}
	if sess.status != sPolling {
		t.Fatalf("session status = %s, want %s", sess.status, sPolling)
	}
	if body := recorder.Body.String(); !strings.Contains(body, `http-equiv="refresh"`) {
		t.Fatalf("callback response does not bounce the browser back: %s", body)
	}
}

func TestSubmitCallbackSecretValidatesTicket(t *testing.T) {
	handler := NewOAuthWebHandler(&config.Config{})
	sess := newTestSession(handler, "state-1", "ticket-1", "local-secret")

	if err := handler.SubmitCallbackSecret("state-1", "other-ticket", "portal-secret"); err == nil {
		t.Fatal("SubmitCallbackSecret accepted a mismatched ticket")
	}
	if err := handler.SubmitCallbackSecret("missing-state", "ticket-1", "portal-secret"); err == nil {
		t.Fatal("SubmitCallbackSecret accepted an unknown state")
	}
	if err := handler.SubmitCallbackSecret("state-1", "ticket-1", ""); err == nil {
		t.Fatal("SubmitCallbackSecret accepted an empty secret")
	}
	if err := handler.SubmitCallbackSecret("state-1", "ticket-1", "portal-secret"); err != nil {
		t.Fatalf("SubmitCallbackSecret returned error: %v", err)
	}
	if got := sess.currentSecret(); got != "portal-secret" {
		t.Fatalf("session secret = %q, want %q", got, "portal-secret")
	}
	if sess.status != sPolling {
		t.Fatalf("session status = %s, want %s", sess.status, sPolling)
	}
}
