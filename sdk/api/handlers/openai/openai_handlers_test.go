package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

type delayedChatExecutor struct {
	delay time.Duration
}

func (e *delayedChatExecutor) Identifier() string { return "test-provider" }

func (e *delayedChatExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	select {
	case <-ctx.Done():
		return coreexecutor.Response{}, ctx.Err()
	case <-time.After(e.delay):
	}
	return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *delayedChatExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *delayedChatExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *delayedChatExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *delayedChatExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestOpenAIChatCompletionsNonStreamEmitsKeepAliveWhileWaiting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &delayedChatExecutor{delay: 1100 * time.Millisecond}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "auth-chat-keepalive", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{NonStreamKeepAliveInterval: 1}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/chat/completions", h.ChatCompletions)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if strings.TrimSpace(resp.Body.String()) != `{"ok":true}` {
		t.Fatalf("trimmed body = %q, want final JSON payload", strings.TrimSpace(resp.Body.String()))
	}
	if resp.Body.String() == strings.TrimSpace(resp.Body.String()) {
		t.Fatalf("body = %q, want keep-alive whitespace before final payload", resp.Body.String())
	}
}

func TestConvertChatCompletionsResponseToCompletions_ContentBlocksExtractText(t *testing.T) {
	response := []byte(`{"id":"chatcmpl_1","object":"chat.completion","created":1,"model":"model","choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"text","text":"hello"},{"type":"output_text","text":" world"},{"type":"image_url","image_url":{"url":"ignored"}}]},"finish_reason":"stop"}]}`)

	out := convertChatCompletionsResponseToCompletions(response)
	text := gjson.GetBytes(out, "choices.0.text").String()
	if text != "hello world" {
		t.Fatalf("completion text = %q, want hello world. Output=%s", text, string(out))
	}
	if strings.Contains(text, `"type":"text"`) {
		t.Fatalf("content blocks were serialized into text: %q", text)
	}
}

func TestConvertChatCompletionsStreamChunkToCompletions_ContentBlocksExtractText(t *testing.T) {
	chunk := []byte(`{"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"model","choices":[{"index":0,"delta":{"content":[{"type":"text","text":"hello"},{"type":"output_text","text":" world"}]},"finish_reason":null}]}`)

	out := convertChatCompletionsStreamChunkToCompletions(chunk)
	if len(out) == 0 {
		t.Fatal("expected converted stream chunk")
	}
	text := gjson.GetBytes(out, "choices.0.text").String()
	if text != "hello world" {
		t.Fatalf("completion text = %q, want hello world. Output=%s", text, string(out))
	}
	if strings.Contains(text, `"type":"text"`) {
		t.Fatalf("content blocks were serialized into text: %q", text)
	}
}
