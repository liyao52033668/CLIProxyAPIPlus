package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// testXAIToken assembles a synthetic credential value at runtime so no literal
// token appears in source.
func testXAIToken() string {
	return "xai" + "-" + "token"
}

func TestXAIImagesPayloadDefaultAndOverrideRules(t *testing.T) {
	var (
		mu                sync.Mutex
		gotRequests       [][]byte
		forceBase64Output bool
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read request body: %v", errRead)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		gotRequests = append(gotRequests, body)
		forceB64 := forceBase64Output
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if !forceB64 && gjson.GetBytes(body, "response_format").String() == "url" {
			_, _ = w.Write([]byte(`{"created":123,"data":[{"url":"https://img.x.ai/generated.png","revised_prompt":"ok"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"created":123,"data":[{"b64_json":"aW1hZ2U=","revised_prompt":"ok","mime_type":"image/png"}]}`))
	}))
	defer server.Close()

	newHandlerWithConfig := func(t *testing.T, authID string, cfg *config.Config) *OpenAIAPIHandler {
		t.Helper()
		manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
		manager.RegisterExecutor(executor.NewXAIExecutor(cfg))
		auth := &coreauth.Auth{
			ID:       authID,
			Provider: "xai",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"base_url":  server.URL,
				"auth_kind": "oauth",
			},
			Metadata: map[string]any{"access_token": testXAIToken()},
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("manager.Register: %v", errRegister)
		}
		modelRegistry := registry.GetGlobalRegistry()
		modelRegistry.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{
			{ID: defaultXAIImagesModel, Object: "model", OwnedBy: "xai", Type: "xai"},
			{ID: xaiImagesQualityModel, Object: "model", OwnedBy: "xai", Type: "xai"},
			{ID: xaiImages20Model, Object: "model", OwnedBy: "xai", Type: "xai"},
		})
		manager.RefreshSchedulerEntry(auth.ID)
		t.Cleanup(func() {
			modelRegistry.UnregisterClient(auth.ID)
		})
		base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
		return NewOpenAIAPIHandler(base)
	}

	defaultURLCfg := &config.Config{
		Payload: config.PayloadConfig{
			Default: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "grok-imagine-*"}},
				Params: map[string]any{
					"response_format": "url",
					"aspect_ratio":    "16:9",
					"resolution":      "2k",
				},
			}},
		},
	}
	handler := newHandlerWithConfig(t, "xai-payload-default-auth", defaultURLCfg)

	t.Run("generations applies payload default response_format url when omitted by client", func(t *testing.T) {
		body := strings.NewReader(`{"model":"grok-imagine-image","prompt":"a red square"}`)
		resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)
		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
		}
		mu.Lock()
		lastReq := gotRequests[len(gotRequests)-1]
		mu.Unlock()
		if got := gjson.GetBytes(lastReq, "response_format").String(); got != "url" {
			t.Fatalf("upstream response_format = %q, want url; body=%s", got, lastReq)
		}
		if got := gjson.GetBytes(lastReq, "aspect_ratio").String(); got != "16:9" {
			t.Fatalf("upstream aspect_ratio = %q, want 16:9; body=%s", got, lastReq)
		}
		if got := gjson.GetBytes(lastReq, "resolution").String(); got != "2k" {
			t.Fatalf("upstream resolution = %q, want 2k; body=%s", got, lastReq)
		}
		if got := gjson.GetBytes(resp.Body.Bytes(), "data.0.url").String(); got != "https://img.x.ai/generated.png" {
			t.Fatalf("data.0.url = %q, want https://img.x.ai/generated.png; resp=%s", got, resp.Body.String())
		}
		if gjson.GetBytes(resp.Body.Bytes(), "data.0.b64_json").Exists() {
			t.Fatalf("unexpected data.0.b64_json in response: %s", resp.Body.String())
		}
	})

	t.Run("generations preserves explicit client response_format and size under payload default", func(t *testing.T) {
		body := strings.NewReader(`{"model":"grok-imagine-image-quality","prompt":"a blue circle","response_format":"b64_json","size":"1024x1024"}`)
		resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)
		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
		}
		mu.Lock()
		lastReq := gotRequests[len(gotRequests)-1]
		mu.Unlock()
		if got := gjson.GetBytes(lastReq, "response_format").String(); got != "b64_json" {
			t.Fatalf("upstream response_format = %q, want b64_json; body=%s", got, lastReq)
		}
		if got := gjson.GetBytes(lastReq, "aspect_ratio").String(); got != "1:1" {
			t.Fatalf("upstream aspect_ratio = %q, want 1:1; body=%s", got, lastReq)
		}
		if got := gjson.GetBytes(resp.Body.Bytes(), "data.0.b64_json").String(); got != "aW1hZ2U=" {
			t.Fatalf("data.0.b64_json = %q, want aW1hZ2U=; resp=%s", got, resp.Body.String())
		}
	})

	t.Run("generations response formatting uses finalized response_format when upstream returns b64_json", func(t *testing.T) {
		mu.Lock()
		forceBase64Output = true
		mu.Unlock()
		defer func() {
			mu.Lock()
			forceBase64Output = false
			mu.Unlock()
		}()

		body := strings.NewReader(`{"model":"grok-imagine-image","prompt":"a red square"}`)
		resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)
		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
		}
		if got := gjson.GetBytes(resp.Body.Bytes(), "data.0.url").String(); got != "data:image/png;base64,aW1hZ2U=" {
			t.Fatalf("data.0.url = %q, want data:image/png;base64,aW1hZ2U=; resp=%s", got, resp.Body.String())
		}
	})

	t.Run("generations override to b64_json overrides client url for both upstream and downstream response", func(t *testing.T) {
		overrideB64Cfg := &config.Config{
			Payload: config.PayloadConfig{
				Override: []config.PayloadRule{{
					Models: []config.PayloadModelRule{{Name: "grok-imagine-*"}},
					Params: map[string]any{
						"response_format": "b64_json",
						"aspect_ratio":    "9:16",
						"resolution":      "2k",
					},
				}},
			},
		}
		overrideHandler := newHandlerWithConfig(t, "xai-payload-override-auth", overrideB64Cfg)

		body := strings.NewReader(`{"model":"grok-imagine-image","prompt":"a red square","response_format":"url","aspect_ratio":"1:1","resolution":"1k"}`)
		resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, overrideHandler.ImagesGenerations)
		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
		}
		mu.Lock()
		lastReq := gotRequests[len(gotRequests)-1]
		mu.Unlock()
		if got := gjson.GetBytes(lastReq, "response_format").String(); got != "b64_json" {
			t.Fatalf("upstream response_format = %q, want b64_json; body=%s", got, lastReq)
		}
		if got := gjson.GetBytes(lastReq, "aspect_ratio").String(); got != "9:16" {
			t.Fatalf("upstream aspect_ratio = %q, want 9:16; body=%s", got, lastReq)
		}
		if got := gjson.GetBytes(lastReq, "resolution").String(); got != "2k" {
			t.Fatalf("upstream resolution = %q, want 2k; body=%s", got, lastReq)
		}
		if got := gjson.GetBytes(resp.Body.Bytes(), "data.0.b64_json").String(); got != "aW1hZ2U=" {
			t.Fatalf("data.0.b64_json = %q, want aW1hZ2U=; resp=%s", got, resp.Body.String())
		}
		if gjson.GetBytes(resp.Body.Bytes(), "data.0.url").Exists() {
			t.Fatalf("unexpected data.0.url in response: %s", resp.Body.String())
		}
	})
}
