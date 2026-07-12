package helps

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDoJSON_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test" {
			t.Fatalf("Authorization = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"ok":true}` {
			t.Fatalf("body = %s", body)
		}
		w.Header().Set("X-Test", "1")
		_, _ = w.Write([]byte(`{"usage":{"prompt_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)

	headers := make(http.Header)
	headers.Set("Authorization", "Bearer test")
	headers.Set("Content-Type", "application/json")

	status, body, respHeaders, err := DoJSON(context.Background(), nil, UpstreamRequest{
		Provider: "test",
		Method:   http.MethodPost,
		URL:      srv.URL,
		Headers:  headers,
		Body:     []byte(`{"ok":true}`),
		Client:   srv.Client(),
	})
	if err != nil {
		t.Fatalf("DoJSON: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(string(body), "prompt_tokens") {
		t.Fatalf("body = %s", body)
	}
	if respHeaders.Get("X-Test") != "1" {
		t.Fatalf("headers missing X-Test")
	}
}

func TestDoJSON_Non2xxReturnsUpstreamStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	_, _, _, err := DoJSON(context.Background(), nil, UpstreamRequest{
		Provider: "test",
		Method:   http.MethodPost,
		URL:      srv.URL,
		Body:     []byte(`{}`),
		Client:   srv.Client(),
	})
	if err == nil {
		t.Fatal("expected error")
	}
	ue, ok := err.(UpstreamStatusError)
	if !ok {
		t.Fatalf("err type = %T, want UpstreamStatusError", err)
	}
	if ue.StatusCode() != http.StatusBadGateway {
		t.Fatalf("StatusCode = %d", ue.StatusCode())
	}
	if !strings.Contains(ue.Error(), "nope") {
		t.Fatalf("msg = %q", ue.Error())
	}
}

func TestDoStream_SuccessLeavesBodyOpen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: hi\n"))
	}))
	t.Cleanup(srv.Close)

	resp, err := DoStream(context.Background(), nil, UpstreamRequest{
		Provider: "test",
		Method:   http.MethodPost,
		URL:      srv.URL,
		Body:     []byte(`{}`),
		Client:   srv.Client(),
	})
	if err != nil {
		t.Fatalf("DoStream: %v", err)
	}
	defer resp.Body.Close()
	data, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		t.Fatalf("read: %v", errRead)
	}
	if !strings.Contains(string(data), "data: hi") {
		t.Fatalf("body = %s", data)
	}
}
