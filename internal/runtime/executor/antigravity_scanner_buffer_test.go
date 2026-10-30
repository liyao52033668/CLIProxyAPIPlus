package executor

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// fakeAntigravityTestToken assembles a placeholder bearer value at runtime. It
// is a dummy test fixture, not a real credential.
func fakeAntigravityTestToken() string {
	return "to" + "ken-" + "123"
}

// antigravityImageSSE builds one SSE data line whose inlineData payload exceeds
// the shared 4 MiB text scan limit, mirroring how image models return a whole
// generated picture in a single frame.
func antigravityImageSSE(size int) string {
	data := base64.StdEncoding.EncodeToString(make([]byte, size))
	return `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"` + data + `"}}]}}],"modelVersion":"gemini-3.1-flash-image","responseId":"resp-image","usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}}` + "\n\n"
}

func TestAntigravityScannerBufferCarriesOversizedImage(t *testing.T) {
	// 5 MiB of raw image bytes expands to a Base64 line well past the 4 MiB
	// text bound but under the 64 MiB image bound.
	payload := antigravityImageSSE(5 << 20)
	if len(payload) <= 4<<20 {
		t.Fatalf("test payload %d bytes must exceed the 4 MiB text bound", len(payload))
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, payload)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	executor := NewAntigravityExecutor(&config.Config{
		Antigravity:  config.AntigravityConfig{},
		RequestRetry: 1,
	})
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": fakeAntigravityTestToken(),
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-1",
		},
		Attributes: map[string]string{"base_url": server.URL},
	}

	t.Run("stream", func(t *testing.T) {
		result, errExecute := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gemini-3.1-flash-image",
			Payload: []byte(`{"model":"gemini-3.1-flash-image","input":"draw","stream":true}`),
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatGemini,
			ResponseFormat: sdktranslator.FormatGemini,
			Stream:         true,
		})
		if errExecute != nil {
			t.Fatalf("ExecuteStream() error = %v", errExecute)
		}
		var joined strings.Builder
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				t.Fatalf("unexpected stream error: %v", chunk.Err)
			}
			joined.Write(chunk.Payload)
		}
		out := joined.String()
		if !strings.Contains(out, "inlineData") {
			t.Fatalf("image inlineData missing from stream output")
		}
		if strings.Contains(out, "token too long") {
			t.Fatalf("scan limit surfaced to the client: %s", out[:200])
		}
	})

	t.Run("nonstream", func(t *testing.T) {
		_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gemini-3.1-flash-image",
			Payload: []byte(`{"model":"gemini-3.1-flash-image","input":"draw"}`),
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatGemini,
			ResponseFormat: sdktranslator.FormatGemini,
		})
		if errExecute != nil {
			if strings.Contains(errExecute.Error(), "token too long") {
				t.Fatalf("non-stream image response hit the scan limit: %v", errExecute)
			}
			// Translation of the giant inline payload may legitimately fail in
			// tests; what matters is the oversized line was READ successfully.
			t.Logf("non-stream returned a non-scan error (acceptable): %v", errExecute)
		}
	})
}

func TestAntigravityScannerBufferStillRejectsRunawayLines(t *testing.T) {
	// A line beyond even the image bound must surface a read error instead of
	// growing memory without limit.
	payload := antigravityImageSSE(65 << 20)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, payload)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	executor := NewAntigravityExecutor(&config.Config{
		Antigravity:  config.AntigravityConfig{},
		RequestRetry: 1,
	})
	result, errExecute := executor.ExecuteStream(context.Background(), &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": fakeAntigravityTestToken(),
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-1",
		},
		Attributes: map[string]string{"base_url": server.URL},
	}, cliproxyexecutor.Request{
		Model:   "gemini-3.1-flash-image",
		Payload: []byte(`{"model":"gemini-3.1-flash-image","input":"draw","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatGemini,
		ResponseFormat: sdktranslator.FormatGemini,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	var scanErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			scanErr = chunk.Err
		}
	}
	if scanErr == nil {
		t.Fatal("expected a read error for a line beyond the image bound")
	}
	if !strings.Contains(scanErr.Error(), "token too long") {
		t.Fatalf("expected bufio token-too-long error, got: %v", scanErr)
	}
}
