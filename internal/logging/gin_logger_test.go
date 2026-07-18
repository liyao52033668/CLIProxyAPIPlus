package logging

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func TestGinLogrusRecoveryDoesNotConvertErrAbortHandlerTo500OrLogStack(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var logrusOutput bytes.Buffer
	previousLogrusOutput := log.StandardLogger().Out
	log.SetOutput(&logrusOutput)
	defer log.SetOutput(previousLogrusOutput)

	var ginRecoveryOutput bytes.Buffer
	previousGinErrorWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &ginRecoveryOutput
	defer func() {
		gin.DefaultErrorWriter = previousGinErrorWriter
	}()

	engine := gin.New()
	engine.Use(GinLogrusRecovery())
	engine.GET("/abort", func(c *gin.Context) {
		panic(http.ErrAbortHandler)
	})

	req := httptest.NewRequest(http.MethodGet, "/abort", nil)
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, req)
	if recorder.Code == http.StatusInternalServerError {
		t.Fatalf("expected ErrAbortHandler not to be converted into 500, got %d", recorder.Code)
	}
	if logrusOutput.Len() != 0 {
		t.Fatalf("expected no logrus recovery log, got %q", logrusOutput.String())
	}
	if bytes.Contains(ginRecoveryOutput.Bytes(), []byte("[Recovery]")) {
		t.Fatalf("expected gin recovery to avoid stack log, got %q", ginRecoveryOutput.String())
	}
	if ginRecoveryOutput.Len() == 0 {
		t.Fatal("expected gin to handle ErrAbortHandler via its broken-pipe path")
	}
}

func TestGinLogrusRecoveryHandlesRegularPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(GinLogrusRecovery())
	engine.GET("/panic", func(c *gin.Context) {
		panic("boom")
	})

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", recorder.Code)
	}
}

func TestIsAIAPIPathIncludesPublicAPIGroups(t *testing.T) {
	for _, path := range []string{
		"/v1",
		"/v1/models",
		"/v1/alpha/search",
		"/v1beta/interactions",
		"/openai/v1/videos",
		"/backend-api/codex/responses",
		"/api/provider/openai/v1/chat/completions",
	} {
		if !isAIAPIPath(path) {
			t.Fatalf("expected %s to be treated as AI API path", path)
		}
	}
	for _, path := range []string{
		"/v0/management/config",
		"/v10/models",
		"/openai/v10/videos",
		"/backend-api/codex-status",
		"/api/provider-status",
	} {
		if isAIAPIPath(path) {
			t.Fatalf("expected %s not to be treated as AI API path", path)
		}
	}
}

func TestIsAIAPIPathIncludesImages(t *testing.T) {
	if !isAIAPIPath("/v1/images/generations") {
		t.Fatalf("expected /v1/images/generations to be treated as AI API path")
	}
	if !isAIAPIPath("/v1/images/edits") {
		t.Fatalf("expected /v1/images/edits to be treated as AI API path")
	}
	if !isAIAPIPath("/v1/videos") {
		t.Fatalf("expected /v1/videos to be treated as AI API path")
	}
	if !isAIAPIPath("/v1/videos/video_123") {
		t.Fatalf("expected /v1/videos/video_123 to be treated as AI API path")
	}
}
