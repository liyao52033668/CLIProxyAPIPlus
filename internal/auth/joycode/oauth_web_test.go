package joycode

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestRegisterRoutesProtectsJoyCodeSetup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewOAuthWebHandler(&config.Config{})
	handler.RegisterRoutes(router, func(c *gin.Context) {
		c.AbortWithStatus(http.StatusUnauthorized)
	})

	for _, path := range []string{"/v0/oauth/joycode", "/v0/oauth/joycode/start"} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		router.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s status = %d, want %d", path, recorder.Code, http.StatusUnauthorized)
		}
	}

	for _, path := range []string{"/v0/oauth/joycode/status?state=missing", "/v0/oauth/joycode/callback"} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		router.ServeHTTP(recorder, req)
		if recorder.Code == http.StatusUnauthorized {
			t.Fatalf("%s unexpectedly required setup authentication", path)
		}
	}
}

func TestJoyCodeCallbackRequiresMatchingAuthKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewOAuthWebHandler(&config.Config{})
	sess := &jcWebSession{
		stateID:   "state-1",
		authKey:   "auth-key-1",
		status:    jcWaiting,
		startedAt: time.Now(),
	}
	handler.sessions[sess.stateID] = sess

	req := httptest.NewRequest(http.MethodGet, "/?authKey=wrong-key&pt_key=credential", nil)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = req

	handler.handleCallback(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	if sess.status != jcWaiting {
		t.Fatalf("session status = %s, want %s", sess.status, jcWaiting)
	}
}
