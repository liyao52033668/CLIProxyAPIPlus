package codearts

import (
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestCodeArtsCallbackRequiresMatchingTicket(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewOAuthWebHandler(&config.Config{})
	sess := &webSession{
		stateID:   "state-1",
		ticketID:  "ticket-1",
		status:    sWaitingCB,
		startedAt: time.Now(),
	}
	handler.sessions[sess.stateID] = sess
	handler.ticketToState[sess.ticketID] = sess.stateID

	redirect := url.QueryEscape("codearts://callback?ticket_id=wrong-ticket")
	req := httptest.NewRequest(http.MethodGet, "/callback?identifier=user-1&redirect="+redirect, nil)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = req

	handler.handleCallback(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	if sess.status != sWaitingCB || sess.identifier != "" {
		t.Fatalf("session changed after unmatched callback: status=%s identifier=%q", sess.status, sess.identifier)
	}
}
