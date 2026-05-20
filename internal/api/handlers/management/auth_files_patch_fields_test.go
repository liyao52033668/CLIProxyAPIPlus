package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestPatchAuthFileFields_MergeHeadersAndDeleteEmptyValues(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:       "test.json",
		FileName: "test.json",
		Provider: "claude",
		Attributes: map[string]string{
			"path":            "/tmp/test.json",
			"header:X-Old":    "old",
			"header:X-Remove": "gone",
		},
		Metadata: map[string]any{
			"type": "claude",
			"headers": map[string]any{
				"X-Old":    "old",
				"X-Remove": "gone",
			},
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	body := `{"name":"test.json","prefix":"p1","proxy_url":"http://proxy.local","headers":{"X-Old":"new","X-New":"v","X-Remove":"  ","X-Nope":""},"priority":3,"excluded_models":["MODEL-B","model-a","model-a"],"disable_cooling":true,"websockets":true,"note":"patched"}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchAuthFileFields(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	updated, ok := manager.GetByID("test.json")
	if !ok || updated == nil {
		t.Fatalf("expected auth record to exist after patch")
	}

	if updated.Prefix != "p1" {
		t.Fatalf("prefix = %q, want %q", updated.Prefix, "p1")
	}
	if updated.ProxyURL != "http://proxy.local" {
		t.Fatalf("proxy_url = %q, want %q", updated.ProxyURL, "http://proxy.local")
	}

	if updated.Metadata == nil {
		t.Fatalf("expected metadata to be non-nil")
	}
	if got, _ := updated.Metadata["prefix"].(string); got != "p1" {
		t.Fatalf("metadata.prefix = %q, want %q", got, "p1")
	}
	if got, _ := updated.Metadata["proxy_url"].(string); got != "http://proxy.local" {
		t.Fatalf("metadata.proxy_url = %q, want %q", got, "http://proxy.local")
	}

	headersMeta, ok := updated.Metadata["headers"].(map[string]any)
	if !ok {
		raw, _ := json.Marshal(updated.Metadata["headers"])
		t.Fatalf("metadata.headers = %T (%s), want map[string]any", updated.Metadata["headers"], string(raw))
	}
	if got := headersMeta["X-Old"]; got != "new" {
		t.Fatalf("metadata.headers.X-Old = %#v, want %q", got, "new")
	}
	if got := headersMeta["X-New"]; got != "v" {
		t.Fatalf("metadata.headers.X-New = %#v, want %q", got, "v")
	}
	if _, ok := headersMeta["X-Remove"]; ok {
		t.Fatalf("expected metadata.headers.X-Remove to be deleted")
	}
	if _, ok := headersMeta["X-Nope"]; ok {
		t.Fatalf("expected metadata.headers.X-Nope to be absent")
	}

	if got := updated.Attributes["header:X-Old"]; got != "new" {
		t.Fatalf("attrs header:X-Old = %q, want %q", got, "new")
	}
	if got := updated.Attributes["header:X-New"]; got != "v" {
		t.Fatalf("attrs header:X-New = %q, want %q", got, "v")
	}
	if _, ok := updated.Attributes["header:X-Remove"]; ok {
		t.Fatalf("expected attrs header:X-Remove to be deleted")
	}
	if _, ok := updated.Attributes["header:X-Nope"]; ok {
		t.Fatalf("expected attrs header:X-Nope to be absent")
	}
	if got := updated.Attributes["priority"]; got != "3" {
		t.Fatalf("attrs priority = %q, want %q", got, "3")
	}
	if got := updated.Attributes["excluded_models"]; got != "model-a,model-b" {
		t.Fatalf("attrs excluded_models = %q, want %q", got, "model-a,model-b")
	}
	if got := updated.Attributes["websockets"]; got != "true" {
		t.Fatalf("attrs websockets = %q, want %q", got, "true")
	}
	if got := updated.Attributes["note"]; got != "patched" {
		t.Fatalf("attrs note = %q, want %q", got, "patched")
	}
	if got := updated.Metadata["priority"]; got != 3 {
		t.Fatalf("metadata.priority = %#v, want %d", got, 3)
	}
	excludedModels, ok := updated.Metadata["excluded_models"].([]string)
	if !ok {
		t.Fatalf("metadata.excluded_models = %T, want []string", updated.Metadata["excluded_models"])
	}
	if got := strings.Join(excludedModels, ","); got != "model-a,model-b" {
		t.Fatalf("metadata.excluded_models = %q, want %q", got, "model-a,model-b")
	}
	if got, _ := updated.Metadata["disable_cooling"].(bool); !got {
		t.Fatalf("metadata.disable_cooling = %#v, want true", updated.Metadata["disable_cooling"])
	}
	if got, _ := updated.Metadata["websockets"].(bool); !got {
		t.Fatalf("metadata.websockets = %#v, want true", updated.Metadata["websockets"])
	}
	if got := updated.Metadata["note"]; got != "patched" {
		t.Fatalf("metadata.note = %#v, want %q", got, "patched")
	}
}

func TestPatchAuthFileFields_EmptyCollectionsAndNullDeleteFields(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:       "delete.json",
		FileName: "delete.json",
		Provider: "codex",
		Attributes: map[string]string{
			"path":            "/tmp/delete.json",
			"header:X-Remove": "gone",
			"priority":        "5",
			"excluded_models": "model-a",
			"websockets":      "true",
			"note":            "old",
		},
		Metadata: map[string]any{
			"type":            "codex",
			"headers":         map[string]any{"X-Remove": "gone"},
			"priority":        5,
			"excluded_models": []string{"model-a"},
			"disable_cooling": true,
			"disable-cooling": true,
			"websockets":      true,
			"note":            "old",
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	body := `{"name":"delete.json","headers":{},"priority":0,"excluded_models":[],"disable_cooling":null,"websockets":null,"note":""}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchAuthFileFields(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	updated, ok := manager.GetByID("delete.json")
	if !ok || updated == nil {
		t.Fatalf("expected auth record to exist after patch")
	}
	for _, key := range []string{"priority", "excluded_models", "websockets", "note", "header:X-Remove"} {
		if _, ok := updated.Attributes[key]; ok {
			t.Fatalf("expected attrs %s to be deleted", key)
		}
	}
	for _, key := range []string{"headers", "priority", "excluded_models", "excluded-models", "disable_cooling", "disable-cooling", "websockets", "note"} {
		if _, ok := updated.Metadata[key]; ok {
			t.Fatalf("expected metadata %s to be deleted", key)
		}
	}
}

func TestPatchAuthFileFields_OmittedHeadersIsNoop(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:       "noop.json",
		FileName: "noop.json",
		Provider: "claude",
		Attributes: map[string]string{
			"path":         "/tmp/noop.json",
			"header:X-Kee": "1",
		},
		Metadata: map[string]any{
			"type": "claude",
			"headers": map[string]any{
				"X-Kee": "1",
			},
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	body := `{"name":"noop.json","note":"hello"}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchAuthFileFields(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	updated, ok := manager.GetByID("noop.json")
	if !ok || updated == nil {
		t.Fatalf("expected auth record to exist after patch")
	}
	if got := updated.Attributes["header:X-Kee"]; got != "1" {
		t.Fatalf("attrs header:X-Kee = %q, want %q", got, "1")
	}
	headersMeta, ok := updated.Metadata["headers"].(map[string]any)
	if !ok {
		t.Fatalf("expected metadata.headers to remain a map, got %T", updated.Metadata["headers"])
	}
	if got := headersMeta["X-Kee"]; got != "1" {
		t.Fatalf("metadata.headers.X-Kee = %#v, want %q", got, "1")
	}
}
