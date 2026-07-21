package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	internalwatcher "github.com/router-for-me/CLIProxyAPI/v7/internal/watcher"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestListAuthFilesIncludesInvalidWatcherEntries(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "valid.json",
		FileName: "valid.json",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path": "/tmp/valid.json",
		},
		Metadata: map[string]any{"type": "claude", "email": "valid@example.com"},
	}); err != nil {
		t.Fatalf("failed to register valid auth: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	h.SetInvalidAuthSnapshot(func() []internalwatcher.InvalidAuthEntry {
		return []internalwatcher.InvalidAuthEntry{{
			Name:          "broken.json",
			Path:          "/tmp/broken.json",
			Size:          12,
			ModTime:       time.Unix(1700000000, 0).UTC(),
			Source:        "file",
			Status:        "invalid",
			StatusMessage: "invalid auth file: unexpected end of JSON input",
		}}
	})

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
	h.ListAuthFiles(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload struct {
		Files []map[string]any `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(payload.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(payload.Files))
	}

	var invalid map[string]any
	for _, entry := range payload.Files {
		if entry["name"] == "broken.json" {
			invalid = entry
			break
		}
	}
	if invalid == nil {
		t.Fatal("expected invalid watcher entry to be returned")
	}
	if invalid["status"] != "invalid" {
		t.Fatalf("invalid status = %#v, want %q", invalid["status"], "invalid")
	}
	if invalid["status_message"] == "" {
		t.Fatal("expected invalid watcher entry to include status_message")
	}
	if invalid["unavailable"] != true {
		t.Fatalf("invalid unavailable = %#v, want true", invalid["unavailable"])
	}
	if invalid["source"] != "file" {
		t.Fatalf("invalid source = %#v, want %q", invalid["source"], "file")
	}
	if invalid["runtime_only"] != false {
		t.Fatalf("invalid runtime_only = %#v, want false", invalid["runtime_only"])
	}
}

func TestGetAuthFileModelsDeduplicatesAliasedModels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const authID = "test-copilot-alias-dedup.json"
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetOAuthModelAlias(map[string][]config.OAuthModelAlias{
		"github-copilot": {
			{Name: "claude-haiku-4.5", Alias: "claude-haiku-4-5", Fork: true},
		},
	})
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       authID,
		FileName: authID,
		Provider: "github-copilot",
		Status:   coreauth.StatusActive,
	}); err != nil {
		t.Fatalf("failed to register auth: %v", err)
	}

	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(authID, "github-copilot", []*registry.ModelInfo{
		{ID: "claude-haiku-4.5", DisplayName: "Claude Haiku 4.5"},
		{ID: "claude-haiku-4-5", DisplayName: "Claude Haiku 4.5"},
	})
	defer modelRegistry.UnregisterClient(authID)

	h := &Handler{authManager: manager}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/models?name="+authID, nil)
	h.GetAuthFileModels(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(payload.Models) != 1 {
		t.Fatalf("expected 1 deduplicated model, got %d: %s", len(payload.Models), rec.Body.String())
	}
	if payload.Models[0].ID != "claude-haiku-4-5" {
		t.Fatalf("model id = %q, want %q", payload.Models[0].ID, "claude-haiku-4-5")
	}
}
