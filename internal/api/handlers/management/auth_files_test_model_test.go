package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type probeStubExecutor struct {
	provider string
	err      error
	delay    time.Duration
}

func (e *probeStubExecutor) Identifier() string { return e.provider }

func (e *probeStubExecutor) Execute(ctx context.Context, _ *coreauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.delay > 0 {
		select {
		case <-ctx.Done():
			return cliproxyexecutor.Response{}, ctx.Err()
		case <-time.After(e.delay):
		}
	}
	if e.err != nil {
		return cliproxyexecutor.Response{}, e.err
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *probeStubExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *probeStubExecutor) Refresh(context.Context, *coreauth.Auth) (*coreauth.Auth, error) {
	return nil, nil
}

func (e *probeStubExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *probeStubExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func newAuthFileTestContext(t *testing.T, body any) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/test", bytes.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")
	return ctx, rec
}

func TestTestAuthFileModelRequiresModel(t *testing.T) {
	h := &Handler{authManager: coreauth.NewManager(nil, nil, nil)}
	ctx, rec := newAuthFileTestContext(t, map[string]any{"name": "a.json"})
	h.TestAuthFileModel(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestTestAuthFileModelAuthNotFound(t *testing.T) {
	h := &Handler{authManager: coreauth.NewManager(nil, nil, nil)}
	ctx, rec := newAuthFileTestContext(t, map[string]any{"name": "missing.json", "model": "gpt-test"})
	h.TestAuthFileModel(ctx)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestTestAuthFileModelDisabledAuth(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "auth-1",
		FileName: "disabled.json",
		Provider: "antigravity",
		Disabled: true,
		Status:   coreauth.StatusDisabled,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	manager.RegisterExecutor(&probeStubExecutor{provider: "antigravity"})

	h := &Handler{authManager: manager}
	ctx, rec := newAuthFileTestContext(t, map[string]any{"name": "disabled.json", "model": "claude-sonnet-4-6"})
	h.TestAuthFileModel(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp authFileTestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.OK || resp.Status != "disabled" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestTestAuthFileModelSuccess(t *testing.T) {
	const authID = "auth-ok"
	const modelID = "claude-sonnet-4-6"

	// Scheduler only schedules auths that currently advertise the requested model.
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       authID,
		FileName: "ok.json",
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	manager.RegisterExecutor(&probeStubExecutor{provider: "antigravity"})

	h := &Handler{authManager: manager}
	ctx, rec := newAuthFileTestContext(t, map[string]any{"name": "ok.json", "model": modelID})
	h.TestAuthFileModel(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp authFileTestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK || resp.Status != "success" || resp.Model != modelID {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.AuthID != authID || resp.Provider != "antigravity" {
		t.Fatalf("auth fields mismatch: %+v", resp)
	}
}

func TestTestAuthFileModelUnsupportedWithoutExecutor(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "auth-no-exec",
		FileName: "no-exec.json",
		Provider: "unknown-provider",
		Status:   coreauth.StatusActive,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	h := &Handler{authManager: manager}
	ctx, rec := newAuthFileTestContext(t, map[string]any{"name": "no-exec.json", "model": "m1"})
	h.TestAuthFileModel(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp authFileTestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.OK || resp.Status != "unsupported" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestTestAuthFileModelFailedAutoExcludesModel(t *testing.T) {
	const authID = "auth-fail-exclude"
	const modelID = "claude-sonnet-4-6"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       authID,
		FileName: "fail.json",
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"excluded_models": []string{"already-there"},
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	manager.RegisterExecutor(&probeStubExecutor{
		provider: "antigravity",
		err:      &coreauth.Error{HTTPStatus: http.StatusBadGateway, Message: "upstream boom"},
	})

	h := &Handler{authManager: manager}
	ctx, rec := newAuthFileTestContext(t, map[string]any{"name": "fail.json", "model": modelID})
	h.TestAuthFileModel(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp authFileTestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.OK || resp.Status != "failed" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if !resp.ExcludedAdded {
		t.Fatalf("expected excluded_added=true, got %+v", resp)
	}
	if len(resp.ExcludedModels) != 2 || resp.ExcludedModels[0] != "already-there" || resp.ExcludedModels[1] != modelID {
		t.Fatalf("excluded_models = %v, want [already-there %s]", resp.ExcludedModels, modelID)
	}

	fresh, ok := manager.GetByID(authID)
	if !ok || fresh == nil {
		t.Fatal("auth missing after probe")
	}
	gotMeta, ok := fresh.Metadata["excluded_models"].([]string)
	if !ok {
		t.Fatalf("metadata.excluded_models type = %T", fresh.Metadata["excluded_models"])
	}
	if len(gotMeta) != 2 || gotMeta[0] != "already-there" || gotMeta[1] != modelID {
		t.Fatalf("persisted metadata.excluded_models = %v", gotMeta)
	}
}

func TestTestAuthFileModelFailedIdempotentExclude(t *testing.T) {
	const authID = "auth-fail-idem"
	const modelID = "gpt-fail"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       authID,
		FileName: "idem.json",
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"excluded_models": []string{modelID},
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	manager.RegisterExecutor(&probeStubExecutor{
		provider: "antigravity",
		err:      &coreauth.Error{HTTPStatus: http.StatusServiceUnavailable, Message: "down"},
	})

	h := &Handler{authManager: manager}
	ctx, rec := newAuthFileTestContext(t, map[string]any{"name": "idem.json", "model": modelID})
	h.TestAuthFileModel(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp authFileTestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ExcludedAdded {
		t.Fatalf("expected excluded_added=false for already excluded model: %+v", resp)
	}
	if len(resp.ExcludedModels) != 1 || resp.ExcludedModels[0] != modelID {
		t.Fatalf("excluded_models = %v", resp.ExcludedModels)
	}
}

func TestTestAuthFileModelRejectsImageAndVideoModels(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "auth-media",
		FileName: "media.json",
		Provider: "xai",
		Status:   coreauth.StatusActive,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	// Executor is present so rejection must come from media-kind guard, not missing executor.
	manager.RegisterExecutor(&probeStubExecutor{provider: "xai"})
	h := &Handler{authManager: manager}

	cases := []struct {
		model string
		want  string
	}{
		{model: "grok-imagine-image", want: "image"},
		{model: "xai/grok-imagine-image-quality", want: "image"},
		{model: "gpt-image-2", want: "image"},
		{model: "gemini-3.1-flash-image", want: "image"},
		{model: "gemini-3-pro-image-preview", want: "image"},
		{model: "grok-imagine-video", want: "video"},
		{model: "xai/grok-imagine-video", want: "video"},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			ctx, rec := newAuthFileTestContext(t, map[string]any{"name": "media.json", "model": tc.model})
			h.TestAuthFileModel(ctx)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			var resp authFileTestResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.OK || resp.Status != "unsupported" {
				t.Fatalf("unexpected response: %+v", resp)
			}
			if resp.ExcludedAdded {
				t.Fatalf("media models must not auto-exclude: %+v", resp)
			}
			if !strings.Contains(strings.ToLower(resp.Error), tc.want) {
				t.Fatalf("error = %q, want containing %q", resp.Error, tc.want)
			}
		})
	}
}

func TestAuthFileProbeMediaKind(t *testing.T) {
	if kind, _ := authFileProbeMediaKind("claude-sonnet-4-6", "antigravity"); kind != "" {
		t.Fatalf("chat model kind = %q, want empty", kind)
	}
	if kind, _ := authFileProbeMediaKind("gpt-4o", "openai"); kind != "" {
		t.Fatalf("chat model kind = %q, want empty", kind)
	}
	if kind, _ := authFileProbeMediaKind("grok-imagine-image", "xai"); kind != "image" {
		t.Fatalf("kind = %q, want image", kind)
	}
	if kind, _ := authFileProbeMediaKind("grok-imagine-video", "xai"); kind != "video" {
		t.Fatalf("kind = %q, want video", kind)
	}
	if kind, _ := authFileProbeMediaKind("gemini-3-pro-image-preview", "gemini"); kind != "image" {
		t.Fatalf("gemini image kind = %q, want image", kind)
	}

	// OpenAI-compat image type via registry.
	reg := registry.GetGlobalRegistry()
	const clientID = "auth-compat-image-kind"
	reg.RegisterClient(clientID, "openai", []*registry.ModelInfo{{
		ID:   "my-custom-draw",
		Type: registry.OpenAIImageModelType,
	}})
	t.Cleanup(func() { reg.UnregisterClient(clientID) })
	if kind, _ := authFileProbeMediaKind("my-custom-draw", "openai"); kind != "image" {
		t.Fatalf("compat image kind = %q, want image", kind)
	}
}

func TestTruncateProbePreview(t *testing.T) {
	if got := truncateProbePreview("abc", 10); got != "abc" {
		t.Fatalf("got %q", got)
	}
	if got := truncateProbePreview("abcdefghij", 5); got != "abcde…" {
		t.Fatalf("got %q", got)
	}
}
