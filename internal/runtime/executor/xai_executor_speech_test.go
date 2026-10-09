package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestXAISpeechRequestURLStaysOnOfficialAPI(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  xaiauth.CLIChatProxyBaseURL,
		},
	}
	got := xaiSpeechRequestURL(auth)
	want := strings.TrimSuffix(xaiauth.DefaultAPIBaseURL, "/") + xaiTTSPath
	if got != want {
		t.Fatalf("xaiSpeechRequestURL() = %q, want %q", got, want)
	}
	if xaiIsCLIChatProxyBaseURL(got) {
		t.Fatalf("speech URL pinned to CLI chat proxy: %s", got)
	}

	custom := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://gateway.example/v1"}}
	if got := xaiSpeechRequestURL(custom); got != "https://gateway.example/v1/tts" {
		t.Fatalf("custom speech URL = %q", got)
	}
}

func TestXAIExecutorExecuteSpeechPostsAudioRequest(t *testing.T) {
	const body = `{"text":"hello","voice_id":"eve","language":"auto"}`
	var gotPath string
	var gotAuth string
	var gotAccept string
	var gotContentType string
	var gotClientVersion string
	var gotTokenAuth string
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotContentType = r.Header.Get("Content-Type")
		gotClientVersion = r.Header.Get(xaiClientVersionHeader)
		gotTokenAuth = r.Header.Get(xaiTokenAuthHeader)
		raw, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read body: %v", errRead)
		}
		gotBody = string(raw)
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("ID3audio"))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Attributes: map[string]string{
			"base_url":  server.URL + "/v1",
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{"access_token": "xai-token"},
	}
	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-tts",
		Payload: []byte(body),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-speech"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotPath != "/v1/tts" {
		t.Fatalf("path = %q, want /v1/tts", gotPath)
	}
	if gotAuth != "Bearer xai-token" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotAccept != "*/*" {
		t.Fatalf("Accept = %q, want */*", gotAccept)
	}
	if gotContentType != "application/json" {
		t.Fatalf("Content-Type = %q", gotContentType)
	}
	if gotClientVersion != "" || gotTokenAuth != "" {
		t.Fatalf("chat-proxy headers leaked: version=%q token-auth=%q", gotClientVersion, gotTokenAuth)
	}
	if gotBody != body {
		t.Fatalf("body = %s", gotBody)
	}
	if string(resp.Payload) != "ID3audio" {
		t.Fatalf("payload = %q", resp.Payload)
	}
	if resp.Headers.Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("response Content-Type = %q", resp.Headers.Get("Content-Type"))
	}
}

func TestXAIExecutorExecuteStreamRejectsSpeech(t *testing.T) {
	exec := NewXAIExecutor(&config.Config{})
	_, err := exec.ExecuteStream(context.Background(), &cliproxyauth.Auth{}, cliproxyexecutor.Request{}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString(xaiSpeechHandlerType),
	})
	if err == nil || !strings.Contains(err.Error(), "streaming not supported") {
		t.Fatalf("error = %v", err)
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusBadRequest {
		t.Fatalf("status error = %v", err)
	}
}
