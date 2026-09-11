package management

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func writeTestConfigFile(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if errWrite := os.WriteFile(path, []byte("{}\n"), 0o600); errWrite != nil {
		t.Fatalf("failed to write test config: %v", errWrite)
	}
	return path
}

func TestPutCodexKeys_WaitsForRuntimeSyncBeforeResponding(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: writeTestConfigFile(t),
	}
	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	h.SetOnCodexConfigUpdated(func() error {
		h.mu.Lock()
		h.mu.Unlock()
		close(callbackStarted)
		<-releaseCallback
		return nil
	})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/codex-api-key", strings.NewReader(`[{"api-key":"codex-key","base-url":"https://codex.example.com"}]`))
	requestDone := make(chan struct{})
	go func() {
		h.PutCodexKeys(c)
		close(requestDone)
	}()

	select {
	case <-callbackStarted:
	case <-time.After(time.Second):
		t.Fatal("Codex runtime sync callback did not start")
	}
	select {
	case <-requestDone:
		t.Fatal("handler responded before Codex runtime sync completed")
	default:
	}

	close(releaseCallback)
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("handler did not respond after Codex runtime sync completed")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestPutCodexKeys_RollsBackWhenRuntimeSyncFails(t *testing.T) {
	gin.SetMode(gin.TestMode)

	configPath := writeTestConfigFile(t)
	h := &Handler{
		cfg: &config.Config{CodexKey: []config.CodexKey{{
			APIKey:  "old-key",
			BaseURL: "https://old.example.com",
		}}},
		configFilePath: configPath,
	}
	callbackCalls := 0
	h.SetOnCodexConfigUpdated(func() error {
		callbackCalls++
		if callbackCalls == 1 {
			return errors.New("sync failed")
		}
		return nil
	})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/codex-api-key", strings.NewReader(`[{"api-key":"new-key","base-url":"https://new.example.com"}]`))

	h.PutCodexKeys(c)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	if callbackCalls != 2 {
		t.Fatalf("callback calls = %d, want 2", callbackCalls)
	}
	if len(h.cfg.CodexKey) != 1 || h.cfg.CodexKey[0].APIKey != "old-key" {
		t.Fatalf("runtime config was not rolled back: %+v", h.cfg.CodexKey)
	}
	persisted, errLoad := config.LoadConfig(configPath)
	if errLoad != nil {
		t.Fatalf("load rolled back config: %v", errLoad)
	}
	if len(persisted.CodexKey) != 1 || persisted.CodexKey[0].APIKey != "old-key" {
		t.Fatalf("persisted config was not rolled back: %+v", persisted.CodexKey)
	}
}

func TestPutCodexKeys_RecoversCallbackPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{CodexKey: []config.CodexKey{{
			APIKey:  "old-key",
			BaseURL: "https://old.example.com",
		}}},
		configFilePath: writeTestConfigFile(t),
	}
	callbackCalls := 0
	h.SetOnCodexConfigUpdated(func() error {
		callbackCalls++
		if callbackCalls == 1 {
			panic("sync panic")
		}
		return nil
	})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/codex-api-key", strings.NewReader(`[{"api-key":"new-key","base-url":"https://new.example.com"}]`))

	h.PutCodexKeys(c)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "callback panic: sync panic") {
		t.Fatalf("body = %s, want callback panic", rec.Body.String())
	}
	if len(h.cfg.CodexKey) != 1 || h.cfg.CodexKey[0].APIKey != "old-key" {
		t.Fatalf("runtime config was not rolled back: %+v", h.cfg.CodexKey)
	}
}

func TestDeleteGeminiKey_RequiresBaseURLWhenAPIKeyDuplicated(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			GeminiKey: []config.GeminiKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/gemini-api-key?api-key=shared-key", nil)

	h.DeleteGeminiKey(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if got := len(h.cfg.GeminiKey); got != 2 {
		t.Fatalf("gemini keys len = %d, want 2", got)
	}
}

func TestDeleteGeminiKey_DeletesOnlyMatchingBaseURL(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			GeminiKey: []config.GeminiKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/gemini-api-key?api-key=shared-key&base-url=https://a.example.com", nil)

	h.DeleteGeminiKey(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.GeminiKey); got != 1 {
		t.Fatalf("gemini keys len = %d, want 1", got)
	}
	if got := h.cfg.GeminiKey[0].BaseURL; got != "https://b.example.com" {
		t.Fatalf("remaining base-url = %q, want %q", got, "https://b.example.com")
	}
}

func TestDeleteClaudeKey_DeletesEmptyBaseURLWhenExplicitlyProvided(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			ClaudeKey: []config.ClaudeKey{
				{APIKey: "shared-key", BaseURL: ""},
				{APIKey: "shared-key", BaseURL: "https://claude.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/claude-api-key?api-key=shared-key&base-url=", nil)

	h.DeleteClaudeKey(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.ClaudeKey); got != 1 {
		t.Fatalf("claude keys len = %d, want 1", got)
	}
	if got := h.cfg.ClaudeKey[0].BaseURL; got != "https://claude.example.com" {
		t.Fatalf("remaining base-url = %q, want %q", got, "https://claude.example.com")
	}
}

func TestDeleteVertexCompatKey_DeletesOnlyMatchingBaseURL(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			VertexCompatAPIKey: []config.VertexCompatKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/vertex-api-key?api-key=shared-key&base-url=https://b.example.com", nil)

	h.DeleteVertexCompatKey(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.VertexCompatAPIKey); got != 1 {
		t.Fatalf("vertex keys len = %d, want 1", got)
	}
	if got := h.cfg.VertexCompatAPIKey[0].BaseURL; got != "https://a.example.com" {
		t.Fatalf("remaining base-url = %q, want %q", got, "https://a.example.com")
	}
}

func TestDeleteCodexKey_RequiresBaseURLWhenAPIKeyDuplicated(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			CodexKey: []config.CodexKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/codex-api-key?api-key=shared-key", nil)

	h.DeleteCodexKey(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if got := len(h.cfg.CodexKey); got != 2 {
		t.Fatalf("codex keys len = %d, want 2", got)
	}
}

const testFreebuffCredential = "freebuff-placeholder-credential"

func TestPatchFreebuffKeyPreservesUnspecifiedFields(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{FreebuffKey: []config.FreebuffKey{{
			APIKey:  testFreebuffCredential,
			BaseURL: "https://www.codebuff.com",
			Models: []config.FreebuffModel{{
				Name: "deepseek/deepseek-v4-flash", Alias: "flash", AgentID: "base2-free-deepseek-flash",
			}},
		}}},
		configFilePath: writeTestConfigFile(t),
	}
	body := []byte(`{"index":0,"value":{"priority":7}}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/freebuff-api-key", strings.NewReader(string(body)))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchFreebuffKey(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(h.cfg.FreebuffKey) != 1 {
		t.Fatalf("freebuff keys = %#v", h.cfg.FreebuffKey)
	}
	entry := h.cfg.FreebuffKey[0]
	if entry.APIKey != testFreebuffCredential || entry.Priority != 7 || len(entry.Models) != 1 {
		t.Fatalf("patched entry = %#v", entry)
	}
}

func TestDeleteFreebuffKeyRejectsMalformedIndex(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg:            &config.Config{FreebuffKey: []config.FreebuffKey{{APIKey: testFreebuffCredential}}},
		configFilePath: writeTestConfigFile(t),
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/freebuff-api-key?index=invalid", nil)

	h.DeleteFreebuffKey(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if len(h.cfg.FreebuffKey) != 1 || h.cfg.FreebuffKey[0].APIKey != testFreebuffCredential {
		t.Fatalf("malformed index changed config: %#v", h.cfg.FreebuffKey)
	}
}

func TestPatchFreebuffKeyRejectsAmbiguousCredentialMatch(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{FreebuffKey: []config.FreebuffKey{
			{APIKey: testFreebuffCredential, Priority: 1},
			{APIKey: testFreebuffCredential, Priority: 2},
		}},
		configFilePath: writeTestConfigFile(t),
	}
	body := []byte(`{"match":"` + testFreebuffCredential + `","value":{"priority":9}}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/freebuff-api-key", strings.NewReader(string(body)))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchFreebuffKey(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if h.cfg.FreebuffKey[0].Priority != 1 || h.cfg.FreebuffKey[1].Priority != 2 {
		t.Fatalf("ambiguous match changed config: %#v", h.cfg.FreebuffKey)
	}
}

func TestDeleteFreebuffKeyRejectsAmbiguousCredentialMatch(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{FreebuffKey: []config.FreebuffKey{
			{APIKey: testFreebuffCredential, BaseURL: "https://a.example"},
			{APIKey: testFreebuffCredential, BaseURL: "https://b.example"},
		}},
		configFilePath: writeTestConfigFile(t),
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/freebuff-api-key?api-key="+testFreebuffCredential, nil)

	h.DeleteFreebuffKey(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if len(h.cfg.FreebuffKey) != 2 {
		t.Fatalf("ambiguous delete changed config: %#v", h.cfg.FreebuffKey)
	}
}
