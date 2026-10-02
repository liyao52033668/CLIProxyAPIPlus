package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPutAndGetOAuthSettings(t *testing.T) {
	configFile := writeTestConfigFile(t)
	cfg := &config.Config{}
	h := &Handler{cfg: cfg, configFilePath: configFile}

	putRec := httptest.NewRecorder()
	putCtx, _ := gin.CreateTestContext(putRec)
	putCtx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/oauth-settings",
		strings.NewReader(`{"codex":[{"name":" gpt-6-sol ","max-context-length":524288},{"name":"gpt-6-sol","max-context-length":1},{"name":"gpt-6-astra","max-context-length":1048576}]}`))
	putCtx.Request.Header.Set("Content-Type", "application/json")
	h.PutOAuthSettings(putCtx)
	if putRec.Code != 200 {
		t.Fatalf("PUT status = %d, want 200", putRec.Code)
	}

	getRec := httptest.NewRecorder()
	getCtx, _ := gin.CreateTestContext(getRec)
	getCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/oauth-settings", nil)
	h.GetOAuthSettings(getCtx)
	if getRec.Code != 200 {
		t.Fatalf("GET status = %d, want 200", getRec.Code)
	}
	var payload struct {
		OAuthSettings map[string][]config.OAuthModelSetting `json:"oauth-settings"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode GET payload: %v", err)
	}
	entries := payload.OAuthSettings["codex"]
	if len(entries) != 2 {
		t.Fatalf("codex entries = %d, want 2 (duplicates and whitespace-only names dropped): %+v", len(entries), entries)
	}
	// SanitizeOAuthSettings keeps the last occurrence of duplicate entries.
	if entries[0].Name != "gpt-6-sol" || entries[0].MaxContextLength != 1 {
		t.Fatalf("first entry = %+v, want trimmed name gpt-6-sol with later occurrence winning (1)", entries[0])
	}
	if entries[1].Name != "gpt-6-astra" {
		t.Fatalf("second entry = %+v, want gpt-6-astra", entries[1])
	}
}

func TestPatchOAuthSettings(t *testing.T) {
	configFile := writeTestConfigFile(t)
	cfg := &config.Config{}
	h := &Handler{cfg: cfg, configFilePath: configFile}

	patchRec := httptest.NewRecorder()
	patchCtx, _ := gin.CreateTestContext(patchRec)
	patchCtx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/oauth-settings",
		strings.NewReader(`{"channel":"Codex","settings":[{"name":"gpt-6-sol","alias":"sol","max-context-length":524288}]}`))
	patchCtx.Request.Header.Set("Content-Type", "application/json")
	h.PatchOAuthSettings(patchCtx)
	if patchRec.Code != 200 {
		t.Fatalf("PATCH status = %d, want 200", patchRec.Code)
	}
	if got := cfg.OAuthSettings["codex"]; len(got) != 1 || got[0].Name != "gpt-6-sol" || got[0].Alias != "sol" {
		t.Fatalf("codex settings after patch = %+v", got)
	}

	// Patching with an empty list removes the channel.
	clearRec := httptest.NewRecorder()
	clearCtx, _ := gin.CreateTestContext(clearRec)
	clearCtx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/oauth-settings",
		strings.NewReader(`{"channel":"codex","settings":[]}`))
	clearCtx.Request.Header.Set("Content-Type", "application/json")
	h.PatchOAuthSettings(clearCtx)
	if clearRec.Code != 200 {
		t.Fatalf("clearing PATCH status = %d, want 200", clearRec.Code)
	}
	if _, ok := cfg.OAuthSettings["codex"]; ok {
		t.Fatalf("codex channel should be removed after empty patch, got %+v", cfg.OAuthSettings)
	}
	if len(cfg.OAuthSettings) != 0 {
		t.Fatalf("settings map should be empty, got %+v", cfg.OAuthSettings)
	}

	// Patching an unknown channel with an empty list must not create a phantom entry.
	phantomRec := httptest.NewRecorder()
	phantomCtx, _ := gin.CreateTestContext(phantomRec)
	phantomCtx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/oauth-settings",
		strings.NewReader(`{"channel":"claude","settings":[]}`))
	phantomCtx.Request.Header.Set("Content-Type", "application/json")
	h.PatchOAuthSettings(phantomCtx)
	if phantomRec.Code != 200 {
		t.Fatalf("phantom PATCH status = %d, want 200", phantomRec.Code)
	}
	if _, ok := cfg.OAuthSettings["claude"]; ok {
		t.Fatalf("unknown channel should not be created by an empty patch, got %+v", cfg.OAuthSettings)
	}
}

func TestDeleteOAuthSettings(t *testing.T) {
	configFile := writeTestConfigFile(t)
	cfg := &config.Config{
		OAuthSettings: map[string][]config.OAuthModelSetting{
			"codex":  {{Name: "gpt-6-sol", MaxContextLength: 524288}},
			"claude": {{Name: "claude-sonnet-5-5", MaxContextLength: 1000}},
		},
	}
	h := &Handler{cfg: cfg, configFilePath: configFile}

	deleteRec := httptest.NewRecorder()
	deleteCtx, _ := gin.CreateTestContext(deleteRec)
	deleteCtx.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/oauth-settings?channel=codex", nil)
	h.DeleteOAuthSettings(deleteCtx)
	if deleteRec.Code != 200 {
		t.Fatalf("DELETE status = %d, want 200", deleteRec.Code)
	}
	if _, ok := cfg.OAuthSettings["codex"]; ok {
		t.Fatalf("codex channel should be deleted, got %+v", cfg.OAuthSettings)
	}
	if _, ok := cfg.OAuthSettings["claude"]; !ok {
		t.Fatalf("claude channel should be untouched, got %+v", cfg.OAuthSettings)
	}

	missingRec := httptest.NewRecorder()
	missingCtx, _ := gin.CreateTestContext(missingRec)
	missingCtx.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/oauth-settings?channel=codex", nil)
	h.DeleteOAuthSettings(missingCtx)
	if missingRec.Code != 404 {
		t.Fatalf("missing channel DELETE status = %d, want 404", missingRec.Code)
	}
}
