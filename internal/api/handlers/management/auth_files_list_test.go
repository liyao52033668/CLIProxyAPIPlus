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
