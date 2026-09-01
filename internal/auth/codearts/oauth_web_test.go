package codearts

import (
	"context"
	"net"
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
	sess.pollTicketID.Store(ticketID)
	sess.extendDeadline(LoginWindow)
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

func TestSubmitPastedCallbackValidatesTicket(t *testing.T) {
	handler := NewOAuthWebHandler(&config.Config{})
	sess := newTestSession(handler, "state-1", "ticket-1", "local-secret")

	if _, err := handler.SubmitPastedCallback("state-1", "other-ticket", "portal-secret", ""); err == nil {
		t.Fatal("SubmitPastedCallback accepted a mismatched ticket")
	}
	if _, err := handler.SubmitPastedCallback("missing-state", "ticket-1", "portal-secret", ""); err == nil {
		t.Fatal("SubmitPastedCallback accepted an unknown state")
	}
	if _, err := handler.SubmitPastedCallback("state-1", "ticket-1", "", ""); err == nil {
		t.Fatal("SubmitPastedCallback accepted an empty secret")
	}

	// Without a redirect there is nothing to finalize, but the secret must still land.
	finalizeURL, err := handler.SubmitPastedCallback("state-1", "ticket-1", "portal-secret", "")
	if err != nil {
		t.Fatalf("SubmitPastedCallback returned error: %v", err)
	}
	if finalizeURL != "" {
		t.Fatalf("finalize URL = %q, want empty", finalizeURL)
	}
	if got := sess.currentSecret(); got != "portal-secret" {
		t.Fatalf("session secret = %q, want %q", got, "portal-secret")
	}
	if sess.status != sPolling {
		t.Fatalf("session status = %s, want %s", sess.status, sPolling)
	}
}

func TestSubmitPastedCallbackReturnsFinalizeURL(t *testing.T) {
	handler := NewOAuthWebHandler(&config.Config{})
	newTestSession(handler, "state-1", "ticket-1", "local-secret")

	redirect := "https://codearts.huaweicloud.com/portal/callback?ticket_id=ticket-1"
	finalizeURL, err := handler.SubmitPastedCallback("state-1", "ticket-1", "portal-secret", redirect)
	if err != nil {
		t.Fatalf("SubmitPastedCallback returned error: %v", err)
	}
	if finalizeURL != redirect {
		t.Fatalf("finalize URL = %q, want %q", finalizeURL, redirect)
	}
}

func TestSubmitPastedCallbackRejectsNonHuaweiRedirect(t *testing.T) {
	handler := NewOAuthWebHandler(&config.Config{})
	sess := newTestSession(handler, "state-1", "ticket-1", "local-secret")

	if _, err := handler.SubmitPastedCallback("state-1", "ticket-1", "portal-secret",
		"http://127.0.0.1:8318/oauth/callback?ticket_id=ticket-1"); err == nil {
		t.Fatal("SubmitPastedCallback accepted a loopback redirect")
	}
	if got := sess.currentSecret(); got != "local-secret" {
		t.Fatalf("session secret = %q, want the untouched local secret", got)
	}
}

func TestSessionDeadlineFailureNotifiesListener(t *testing.T) {
	handler := NewOAuthWebHandler(&config.Config{})
	sess := newTestSession(handler, "state-1", "ticket-1", "local-secret")
	// Expire the session so the very first poll tick gives up.
	sess.deadline.Store(time.Now().Add(-time.Second))

	failed := make(chan error, 1)
	handler.SetAuthFailureCallback(func(stateID string, err error) {
		if stateID != "state-1" {
			t.Errorf("failure callback state = %q, want %q", stateID, "state-1")
		}
		failed <- err
	})

	ctx, cancel := context.WithCancel(context.Background())
	go handler.runTicketPoll(ctx, cancel, sess)

	select {
	case err := <-failed:
		if err == nil {
			t.Fatal("failure callback received a nil error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("failure callback was not invoked after the deadline passed")
	}

	handler.mu.RLock()
	status := sess.status
	handler.mu.RUnlock()
	if status != sFailed {
		t.Fatalf("session status = %s, want %s", status, sFailed)
	}
}

func TestLoginWindowStaysBelowManagementSessionTTL(t *testing.T) {
	// The management OAuth session TTL is 10 minutes; a longer login window would
	// expire the state before the timeout could be reported to the web UI.
	if LoginWindow >= 10*time.Minute {
		t.Fatalf("LoginWindow = %s, must stay below the 10m management session TTL", LoginWindow)
	}
	if CallbackGracePeriod > LoginWindow {
		t.Fatalf("CallbackGracePeriod = %s, must not exceed LoginWindow %s", CallbackGracePeriod, LoginWindow)
	}
}

func TestValidatePortalRedirect(t *testing.T) {
	if _, err := ValidatePortalRedirect("https://codearts.huaweicloud.com/portal/callback?ticket_id=t"); err != nil {
		t.Fatalf("rejected a valid portal redirect: %v", err)
	}
	for name, raw := range map[string]string{
		"empty":            "",
		"custom scheme":    "codearts://callback?ticket_id=t",
		"file scheme":      "file:///etc/passwd",
		"foreign host":     "https://example.com/portal/callback",
		"loopback literal": "http://127.0.0.1/portal/callback",
		"private literal":  "http://10.0.0.5/portal/callback",
		"localhost":        "http://localhost/portal/callback",
		"suffix spoof":     "https://codearts.huaweicloud.com.evil.test/portal/callback",
	} {
		if _, err := ValidatePortalRedirect(raw); err == nil {
			t.Fatalf("%s redirect was accepted: %s", name, raw)
		}
	}
}

func TestIsPublicIP(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.1.1",
		"100.64.0.1", "198.18.0.1", "203.0.113.5", "240.0.0.1", "::1", "fc00::1", "0.0.0.0"} {
		if isPublicIP(net.ParseIP(raw)) {
			t.Fatalf("%s classified as public", raw)
		}
	}
	for _, raw := range []string{"8.8.8.8", "1.1.1.1", "2400:cb00::1"} {
		if !isPublicIP(net.ParseIP(raw)) {
			t.Fatalf("%s classified as non-public", raw)
		}
	}
}
