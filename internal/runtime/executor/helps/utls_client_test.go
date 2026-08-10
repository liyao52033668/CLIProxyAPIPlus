package helps

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
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

type trackedReadCloser struct {
	io.Reader
	closeCount int
	closeErr   error
	onClose    func()
}

func (r *trackedReadCloser) Close() error {
	r.closeCount++
	if r.onClose != nil {
		r.onClose()
	}
	return r.closeErr
}

type contextDialerFunc func(context.Context, string, string) (net.Conn, error)

func (f contextDialerFunc) Dial(network, addr string) (net.Conn, error) {
	return f(context.Background(), network, addr)
}

func (f contextDialerFunc) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return f(ctx, network, addr)
}

type trackedNetConn struct {
	net.Conn
	closeCount atomic.Int32
}

func (c *trackedNetConn) Close() error {
	c.closeCount.Add(1)
	return c.Conn.Close()
}

func TestCloseConnectionBodyClosesConnectionBeforeBodyOnce(t *testing.T) {
	bodyErr := errors.New("body close failed")
	connectionErr := errors.New("connection close failed")
	var closeOrder []string
	body := &trackedReadCloser{
		Reader:   strings.NewReader("response"),
		closeErr: bodyErr,
		onClose: func() {
			closeOrder = append(closeOrder, "body")
		},
	}
	connectionCloseCount := 0
	wrapped := &closeConnectionBody{
		ReadCloser: body,
		closeConnection: func() error {
			connectionCloseCount++
			closeOrder = append(closeOrder, "connection")
			return connectionErr
		},
	}

	payload, errRead := io.ReadAll(wrapped)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if got, want := string(payload), "response"; got != want {
		t.Fatalf("response body = %q, want %q", got, want)
	}

	errClose := wrapped.Close()
	if !errors.Is(errClose, bodyErr) {
		t.Fatalf("close error = %v, want body close error", errClose)
	}
	if !errors.Is(errClose, connectionErr) {
		t.Fatalf("close error = %v, want connection close error", errClose)
	}
	if errCloseAgain := wrapped.Close(); errCloseAgain != errClose {
		t.Fatalf("second close error = %v, want %v", errCloseAgain, errClose)
	}
	if body.closeCount != 1 {
		t.Fatalf("body close count = %d, want 1", body.closeCount)
	}
	if connectionCloseCount != 1 {
		t.Fatalf("connection close count = %d, want 1", connectionCloseCount)
	}
	if want := []string{"connection", "body"}; !reflect.DeepEqual(closeOrder, want) {
		t.Fatalf("close order = %v, want %v", closeOrder, want)
	}
}

func TestUtlsRoundTripperDialUsesRequestContext(t *testing.T) {
	dialStarted := make(chan struct{})
	roundTripper := &utlsRoundTripper{dialer: contextDialerFunc(func(ctx context.Context, _, _ string) (net.Conn, error) {
		close(dialStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	})}
	ctx, cancel := context.WithCancel(t.Context())
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/backend-api/codex/responses", nil)
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	roundTripDone := make(chan error, 1)
	go func() {
		resp, errRoundTrip := roundTripper.RoundTrip(req)
		if resp != nil && resp.Body != nil {
			errRoundTrip = errors.Join(errRoundTrip, resp.Body.Close())
		}
		roundTripDone <- errRoundTrip
	}()

	select {
	case <-dialStarted:
	case <-time.After(time.Second):
		t.Fatal("dial did not start")
	}
	cancel()
	select {
	case errRoundTrip := <-roundTripDone:
		if !errors.Is(errRoundTrip, context.Canceled) {
			t.Fatalf("RoundTrip error = %v, want context canceled", errRoundTrip)
		}
	case <-time.After(time.Second):
		t.Fatal("RoundTrip did not stop after context cancellation")
	}
}

func TestUtlsRoundTripperHandshakeUsesRequestContext(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		if errClose := clientConn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) && !errors.Is(errClose, io.ErrClosedPipe) {
			t.Errorf("close client connection: %v", errClose)
		}
		if errClose := serverConn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) && !errors.Is(errClose, io.ErrClosedPipe) {
			t.Errorf("close server connection: %v", errClose)
		}
	})

	trackedConn := &trackedNetConn{Conn: clientConn}
	dialDone := make(chan struct{})
	roundTripper := &utlsRoundTripper{dialer: contextDialerFunc(func(context.Context, string, string) (net.Conn, error) {
		close(dialDone)
		return trackedConn, nil
	})}
	ctx, cancel := context.WithCancel(t.Context())
	connectionDone := make(chan error, 1)
	go func() {
		h2Conn, errConnect := roundTripper.createConnection(ctx, "chatgpt.com", "chatgpt.com:443")
		if h2Conn != nil {
			errConnect = errors.Join(errConnect, h2Conn.Close())
		}
		connectionDone <- errConnect
	}()

	select {
	case <-dialDone:
	case <-time.After(time.Second):
		t.Fatal("dial did not complete")
	}
	cancel()
	select {
	case errConnect := <-connectionDone:
		if !errors.Is(errConnect, context.Canceled) {
			t.Fatalf("createConnection error = %v, want context canceled", errConnect)
		}
	case <-time.After(time.Second):
		t.Fatal("TLS handshake did not stop after context cancellation")
	}
	if got := trackedConn.closeCount.Load(); got != 1 {
		t.Fatalf("connection close count = %d, want 1", got)
	}
}

type claudeCodeTLSFingerprintFixture struct {
	ClientHelloLength   int
	JA3                 string
	JA3MD5              string
	ALPN                []string
	HTTPVersion         string
	CipherSuites        []uint16
	ExtensionTypes      []uint16
	ExtensionLengths    [][2]int
	SupportedGroups     []uint16
	PointFormats        []uint8
	SignatureAlgorithms []uint16
	SupportedVersions   []uint16
	KeyShareGroups      []uint16
}

func clientUtlsRoundTripper(t *testing.T, client *http.Client) *utlsRoundTripper {
	t.Helper()
	fallback, ok := client.Transport.(*fallbackRoundTripper)
	if !ok {
		t.Fatalf("transport type = %T, want *fallbackRoundTripper", client.Transport)
	}
	return fallback.utls
}

func TestNewUtlsHTTPClientCanDisableUTLS(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{DisableUTLS: true}}
	client := NewUtlsHTTPClient(context.Background(), cfg, nil, 0)
	if _, ok := client.Transport.(*fallbackRoundTripper); ok {
		t.Fatal("expected standard transport when uTLS is disabled")
	}
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
