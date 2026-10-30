package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func newCodexOpenAIImageTestAuth(serverURL string) *cliproxyauth.Auth {
	// Assemble a synthetic credential value at runtime so no literal token appears in source.
	token := "codex" + "-" + "token"
	return &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"base_url": serverURL,
			"api_key":  token,
		},
	}
}

func codexOpenAIImageTestOptions(path string, stream bool) cliproxyexecutor.Options {
	return cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString(codexOpenAIImageSourceFormat),
		Stream:       stream,
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: path,
		},
	}
}

func TestCodexImagePayloadPreservesResolvedAlias(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			req := cliproxyexecutor.Request{
				Model:   "openai/gpt-image-2-cpr",
				Payload: []byte(`{"model":"gpt-image-2","prompt":"diagnostic"}`),
			}
			body, _, _, errPrepare := NewCodexExecutor(&config.Config{}).prepareDirectOpenAIImageBody(nil, req, codexOpenAIImageTestOptions(codexImagesGenerationsPath, stream), stream)
			if errPrepare != nil {
				t.Fatal(errPrepare)
			}
			if got := gjson.GetBytes(body, "model").String(); got != "openai/gpt-image-2-cpr" {
				t.Fatalf("upstream model = %q, want %q", got, "openai/gpt-image-2-cpr")
			}
		})
	}
}

func TestCodexExecutorDirectOpenAIImageGenerationUsesImagesEndpoint(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotUA string
	var gotBody []byte
	upstreamBody := []byte(`{"created":1713833628,"data":[{"b64_json":"AA=="}],"usage":{"total_tokens":100,"input_tokens":50,"output_tokens":50}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(upstreamBody)
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	resp, errExecute := executor.Execute(contextWithGinHeaders(nil), newCodexOpenAIImageTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "codex/gpt-image-1.5",
		Payload: []byte(`{"model":"codex/gpt-image-1.5","prompt":"A cute baby sea otter","n":1,"size":"1024x1024","quality":"high","background":"opaque","output_format":"jpeg","output_compression":70,"moderation":"low","extra":{"preserve":true},"stream":false}`),
	}, codexOpenAIImageTestOptions(codexImagesGenerationsPath, false))
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	if gotPath != "/images/generations" {
		t.Fatalf("path = %q, want /images/generations", gotPath)
	}
	if want := "Bearer " + "codex" + "-" + "token"; gotAuth != want {
		t.Fatalf("Authorization = %q, want %q", gotAuth, want)
	}
	if gotUA == "downstream-client/9.9" {
		t.Fatalf("User-Agent should not be forwarded from the downstream client, got %q", gotUA)
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != "gpt-image-1.5" {
		t.Fatalf("model = %q, want gpt-image-1.5; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "extra.preserve").Bool(); !got {
		t.Fatalf("extra.preserve missing from body: %s", string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "output_compression").Int(); got != 70 {
		t.Fatalf("output_compression = %d, want 70; body=%s", got, string(gotBody))
	}
	if gjson.GetBytes(gotBody, "stream").Exists() {
		t.Fatalf("stream should be removed for non-stream execution: %s", string(gotBody))
	}
	if !bytes.Equal(resp.Payload, upstreamBody) {
		t.Fatalf("payload = %s, want %s", string(resp.Payload), string(upstreamBody))
	}
}

func TestCodexExecutorDirectOpenAIImageGenerationStreamsImagesEndpoint(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: image_generation.partial_image\ndata: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"AA==\",\"partial_image_index\":0}\n\n"))
		_, _ = w.Write([]byte("event: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"BB==\",\"usage\":{\"total_tokens\":10,\"input_tokens\":4,\"output_tokens\":6}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	stream, errStream := executor.ExecuteStream(context.Background(), newCodexOpenAIImageTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-image-2",
		Payload: []byte(`{"model":"gpt-image-2","prompt":"A cute baby sea otter","partial_images":2}`),
	}, codexOpenAIImageTestOptions(codexImagesGenerationsPath, true))
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}

	var combined bytes.Buffer
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		combined.Write(chunk.Payload)
	}

	if gotPath != "/images/generations" {
		t.Fatalf("path = %q, want /images/generations", gotPath)
	}
	if !gjson.GetBytes(gotBody, "stream").Bool() {
		t.Fatalf("stream flag missing from upstream body: %s", string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "partial_images").Int(); got != 2 {
		t.Fatalf("partial_images = %d, want 2; body=%s", got, string(gotBody))
	}
	out := combined.String()
	if !strings.Contains(out, "image_generation.partial_image") || !strings.Contains(out, "image_generation.completed") {
		t.Fatalf("stream output missing image events: %q", out)
	}
}

func TestCodexExecutorDirectOpenAIImageEditUsesImagesEditEndpointForJSON(t *testing.T) {
	var gotPath string
	var gotBody []byte
	upstreamBody := []byte(`{"created":1713833628,"data":[{"b64_json":"AA=="}]}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(upstreamBody)
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	resp, errExecute := executor.Execute(context.Background(), newCodexOpenAIImageTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-image-2",
		Payload: []byte(`{"model":"gpt-image-2","prompt":"make it night","images":[{"image_url":"data:image/png;base64,AA=="}]}`),
	}, codexOpenAIImageTestOptions(codexImagesEditsPath, false))
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	if gotPath != "/images/edits" {
		t.Fatalf("path = %q, want /images/edits", gotPath)
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != "gpt-image-2" {
		t.Fatalf("model = %q, want gpt-image-2; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "images.0.image_url").String(); got != "data:image/png;base64,AA==" {
		t.Fatalf("images.0.image_url = %q, want data URL preserved; body=%s", got, string(gotBody))
	}
	if !bytes.Equal(resp.Payload, upstreamBody) {
		t.Fatalf("payload = %s, want %s", string(resp.Payload), string(upstreamBody))
	}
}

func TestCodexExecutorDirectOpenAIImageEditUsesImagesEditEndpointForMultipart(t *testing.T) {
	var gotPath string
	var gotContentType string
	var gotBody []byte
	upstreamBody := []byte(`{"created":1713833628,"data":[{"b64_json":"AA=="}]}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(upstreamBody)
	}))
	defer server.Close()

	var formBuffer bytes.Buffer
	writer := multipart.NewWriter(&formBuffer)
	if errWriteField := writer.WriteField("model", "gpt-image-2"); errWriteField != nil {
		t.Fatalf("write model field: %v", errWriteField)
	}
	if errWriteField := writer.WriteField("prompt", "make it night"); errWriteField != nil {
		t.Fatalf("write prompt field: %v", errWriteField)
	}
	if errWriteField := writer.WriteField("n", "2"); errWriteField != nil {
		t.Fatalf("write n field: %v", errWriteField)
	}
	part, errCreatePart := writer.CreateFormFile("image[]", "cat.png")
	if errCreatePart != nil {
		t.Fatalf("create form file: %v", errCreatePart)
	}
	if _, errWrite := part.Write([]byte("PNGDATA")); errWrite != nil {
		t.Fatalf("write file part: %v", errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("close writer: %v", errClose)
	}

	options := codexOpenAIImageTestOptions(codexImagesEditsPath, false)
	options.Headers = http.Header{"Content-Type": []string{writer.FormDataContentType()}}
	options.OriginalRequest = formBuffer.Bytes()

	executor := NewCodexExecutor(&config.Config{})
	resp, errExecute := executor.Execute(context.Background(), newCodexOpenAIImageTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-image-2",
		Payload: formBuffer.Bytes(),
	}, options)
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	if gotPath != "/images/edits" {
		t.Fatalf("path = %q, want /images/edits", gotPath)
	}
	if !strings.HasPrefix(gotContentType, "application/json") {
		t.Fatalf("Content-Type = %q, want rewritten application/json", gotContentType)
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != "gpt-image-2" {
		t.Fatalf("model = %q, want gpt-image-2; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "n").Int(); got != 2 {
		t.Fatalf("n = %d, want numeric 2; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "images.0.image_url").String(); !strings.HasPrefix(got, "data:application/octet-stream;base64,") {
		t.Fatalf("images.0.image_url = %q, want data URL; body=%s", got, string(gotBody))
	}
	if !bytes.Equal(resp.Payload, upstreamBody) {
		t.Fatalf("payload = %s, want %s", string(resp.Payload), string(upstreamBody))
	}
}

func TestCodexExecutorDirectOpenAIImage25Models(t *testing.T) {
	for _, model := range []string{codexGPTImage25Model, codexGPTImage25FlareModel, codexGPTImage25SunburstModel} {
		t.Run(model, func(t *testing.T) {
			var gotPath string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"created":1713833628,"data":[{"b64_json":"AA=="}]}`))
			}))
			defer server.Close()

			executor := NewCodexExecutor(&config.Config{})
			_, errExecute := executor.Execute(context.Background(), newCodexOpenAIImageTestAuth(server.URL), cliproxyexecutor.Request{
				Model:   model,
				Payload: []byte(fmt.Sprintf(`{"model":%q,"prompt":"otter"}`, model)),
			}, codexOpenAIImageTestOptions(codexImagesGenerationsPath, false))
			if errExecute != nil {
				t.Fatalf("Execute() error = %v", errExecute)
			}
			if gotPath != "/images/generations" {
				t.Fatalf("path = %q, want /images/generations", gotPath)
			}
		})
	}
}

func TestResolveGPTImage2BaseModel(t *testing.T) {
	t.Run("default when unset", func(t *testing.T) {
		if got := NewCodexExecutor(&config.Config{}).resolveGPTImage2BaseModel(); got != codexOpenAIImagesMainModel {
			t.Fatalf("base model = %q, want %q", got, codexOpenAIImagesMainModel)
		}
	})
	t.Run("configured override", func(t *testing.T) {
		if got := NewCodexExecutor(&config.Config{SDKConfig: config.SDKConfig{GPTImage2BaseModel: "gpt-5.6-terra"}}).resolveGPTImage2BaseModel(); got != "gpt-5.6-terra" {
			t.Fatalf("base model = %q, want gpt-5.6-terra", got)
		}
	})
	t.Run("invalid value falls back", func(t *testing.T) {
		if got := NewCodexExecutor(&config.Config{SDKConfig: config.SDKConfig{GPTImage2BaseModel: "claude-x"}}).resolveGPTImage2BaseModel(); got != codexOpenAIImagesMainModel {
			t.Fatalf("base model = %q, want %q", got, codexOpenAIImagesMainModel)
		}
	})
	t.Run("case-insensitive gpt prefix accepted", func(t *testing.T) {
		if got := NewCodexExecutor(&config.Config{SDKConfig: config.SDKConfig{GPTImage2BaseModel: "GPT-5.6-Luna"}}).resolveGPTImage2BaseModel(); got != "GPT-5.6-Luna" {
			t.Fatalf("base model = %q, want GPT-5.6-Luna", got)
		}
	})
}
