package helps

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type utlsClientRoundTripFunc func(*http.Request) (*http.Response, error)

func (f utlsClientRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func resetUtlsRoundTripperCacheForTest() {
	utlsRoundTripperCacheMutex.Lock()
	utlsRoundTripperCache = make(map[string]*utlsRoundTripper)
	utlsRoundTripperCacheMutex.Unlock()
}

func clientUtlsRoundTripper(t *testing.T, client *http.Client) *utlsRoundTripper {
	t.Helper()
	fallback, ok := client.Transport.(*fallbackRoundTripper)
	if !ok {
		t.Fatalf("transport type = %T, want *fallbackRoundTripper", client.Transport)
	}
	return fallback.utls
}

func TestNewUtlsHTTPClientReusesRoundTripper(t *testing.T) {
	resetUtlsRoundTripperCacheForTest()
	t.Cleanup(resetUtlsRoundTripperCacheForTest)

	first := NewUtlsHTTPClient(context.Background(), nil, nil, 0)
	second := NewUtlsHTTPClient(context.Background(), nil, nil, 0)

	if clientUtlsRoundTripper(t, first) != clientUtlsRoundTripper(t, second) {
		t.Fatal("expected uTLS round tripper to be reused")
	}
}

func TestNewUtlsHTTPClientTimeoutDoesNotPolluteCache(t *testing.T) {
	resetUtlsRoundTripperCacheForTest()
	t.Cleanup(resetUtlsRoundTripperCacheForTest)

	timed := NewUtlsHTTPClient(context.Background(), nil, nil, 15*time.Second)
	streaming := NewUtlsHTTPClient(context.Background(), nil, nil, 0)

	if timed.Timeout != 15*time.Second {
		t.Fatalf("timed client timeout = %v, want %v", timed.Timeout, 15*time.Second)
	}
	if streaming.Timeout != 0 {
		t.Fatalf("streaming client timeout = %v, want 0", streaming.Timeout)
	}
	if clientUtlsRoundTripper(t, timed) != clientUtlsRoundTripper(t, streaming) {
		t.Fatal("expected clients with different timeouts to reuse uTLS round tripper")
	}
}

func TestNewUtlsHTTPClientSeparatesProxyRoundTrippers(t *testing.T) {
	resetUtlsRoundTripperCacheForTest()
	t.Cleanup(resetUtlsRoundTripperCacheForTest)

	first := NewUtlsHTTPClient(nil, &cliproxyauth.Auth{ProxyURL: "http://proxy-one.example.com:8080"}, 0)
	second := NewUtlsHTTPClient(nil, &cliproxyauth.Auth{ProxyURL: "http://proxy-two.example.com:8080"}, 0)

	if clientUtlsRoundTripper(t, first) == clientUtlsRoundTripper(t, second) {
		t.Fatal("expected different proxies to use separate uTLS round trippers")
	}
}

func TestNewUtlsHTTPClientConcurrentCacheMissReusesRoundTripper(t *testing.T) {
	resetUtlsRoundTripperCacheForTest()
	t.Cleanup(resetUtlsRoundTripperCacheForTest)

	const workers = 32
	roundTrippers := make([]*utlsRoundTripper, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for i := 0; i < workers; i++ {
		go func(index int) {
			defer wait.Done()
			client := NewUtlsHTTPClient(context.Background(), nil, nil, 0)
			roundTrippers[index] = clientUtlsRoundTripper(t, client)
		}(i)
	}
	wait.Wait()

	for i := 1; i < len(roundTrippers); i++ {
		if roundTrippers[i] != roundTrippers[0] {
			t.Fatalf("uTLS round tripper %d was not shared", i)
		}
	}
}

func TestNewUtlsHTTPClientUsesContextRoundTripperForProtectedHost(t *testing.T) {
	t.Parallel()

	called := false
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", utlsClientRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		called = true
		if req.URL.Hostname() != "chatgpt.com" {
			t.Fatalf("hostname = %q, want chatgpt.com", req.URL.Hostname())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("{}")),
			Request:    req,
		}, nil
	}))

	client := NewUtlsHTTPClient(ctx, nil, nil, 0)
	resp, err := client.Get("https://chatgpt.com/backend-api/codex/responses")
	if err != nil {
		t.Fatalf("client.Get returned error: %v", err)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatalf("response body close returned error: %v", errClose)
	}
	if !called {
		t.Fatal("expected context RoundTripper to handle protected host request")
	}
}
