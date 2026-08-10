package helps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	tls "github.com/refraction-networking/utls"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

// utlsRoundTripper implements http.RoundTripper using utls with Chrome fingerprint
// to bypass Cloudflare's TLS fingerprinting on Anthropic domains.
type utlsRoundTripper struct {
	dialer proxy.Dialer
}

type closeConnectionBody struct {
	io.ReadCloser
	closeConnection func() error
	once            sync.Once
	err             error
}

func (b *closeConnectionBody) Close() error {
	if b == nil {
		return nil
	}
	b.once.Do(func() {
		var errConnection error
		if b.closeConnection != nil {
			errConnection = b.closeConnection()
		}
		var errBody error
		if b.ReadCloser != nil {
			errBody = b.ReadCloser.Close()
		}
		b.err = errors.Join(errBody, errConnection)
	})
	return b.err
}

var (
	utlsRoundTripperCache      = make(map[string]*utlsRoundTripper)
	utlsRoundTripperCacheMutex sync.RWMutex
)

func cachedUtlsRoundTripper(proxyURL string) *utlsRoundTripper {
	utlsRoundTripperCacheMutex.RLock()
	cached := utlsRoundTripperCache[proxyURL]
	utlsRoundTripperCacheMutex.RUnlock()
	if cached != nil {
		return cached
	}

	utlsRoundTripperCacheMutex.Lock()
	defer utlsRoundTripperCacheMutex.Unlock()
	if cached = utlsRoundTripperCache[proxyURL]; cached != nil {
		return cached
	}
	cached = newUtlsRoundTripper(proxyURL)
	utlsRoundTripperCache[proxyURL] = cached
	return cached
}

func newUtlsRoundTripper(proxyURL string) *utlsRoundTripper {
	var dialer proxy.Dialer = proxy.Direct
	if proxyURL != "" {
		proxyDialer, mode, errBuild := proxyutil.BuildDialer(proxyURL)
		if errBuild != nil {
			log.Errorf("utls: failed to configure proxy dialer for %q: %v", proxyutil.Redact(proxyURL), errBuild)
		} else if mode != proxyutil.ModeInherit && proxyDialer != nil {
			dialer = proxyDialer
		}
	}
	return &utlsRoundTripper{dialer: dialer}
}

func (t *utlsRoundTripper) createConnection(ctx context.Context, host, addr string) (*http2.ClientConn, error) {
	contextDialer, ok := t.dialer.(proxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("utls: dialer does not support context cancellation")
	}
	conn, errDial := contextDialer.DialContext(ctx, "tcp", addr)
	if errDial != nil {
		return nil, fmt.Errorf("utls: dial upstream: %w", errDial)
	}

	tlsConfig := &tls.Config{ServerName: host}
	tlsConn := tls.UClient(conn, tlsConfig, tls.HelloChrome_Auto)

	if errHandshake := tlsConn.HandshakeContext(ctx); errHandshake != nil {
		if errors.Is(errHandshake, context.Canceled) || errors.Is(errHandshake, context.DeadlineExceeded) {
			return nil, fmt.Errorf("utls: TLS handshake: %w", errHandshake)
		}
		if errClose := conn.Close(); errClose != nil {
			return nil, fmt.Errorf("utls: TLS handshake: %w; close connection: %v", errHandshake, errClose)
		}
		return nil, fmt.Errorf("utls: TLS handshake: %w", errHandshake)
	}

	tr := &http2.Transport{}
	h2Conn, errClientConn := tr.NewClientConn(tlsConn)
	if errClientConn != nil {
		if errClose := tlsConn.Close(); errClose != nil {
			return nil, fmt.Errorf("utls: initialize HTTP/2 connection: %w; close TLS connection: %v", errClientConn, errClose)
		}
		return nil, fmt.Errorf("utls: initialize HTTP/2 connection: %w", errClientConn)
	}

	return h2Conn, nil
}

func (t *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	hostname := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(hostname, port)

	h2Conn, err := t.createConnection(req.Context(), hostname, addr)
	if err != nil {
		return nil, err
	}

	resp, err := h2Conn.RoundTrip(req)
	if err != nil {
		if errClose := h2Conn.Close(); errClose != nil {
			log.Debugf("utls: close connection after round trip failure: %v", errClose)
		}
		return nil, err
	}
	if resp == nil {
		if errClose := h2Conn.Close(); errClose != nil {
			log.Debugf("utls: close connection after empty response: %v", errClose)
		}
		return nil, fmt.Errorf("utls: upstream returned an empty response")
	}
	if resp.Body == nil {
		resp.Body = http.NoBody
	}
	resp.Body = &closeConnectionBody{
		ReadCloser:      resp.Body,
		closeConnection: h2Conn.Close,
	}
	return resp, nil
}

// anthropicHosts contains the hosts that should use utls Chrome TLS fingerprint.
var anthropicHosts = map[string]struct{}{
	"api.anthropic.com": {},
}

// fallbackRoundTripper uses utls for Anthropic HTTPS hosts and falls back to
// standard transport for all other requests (non-HTTPS or non-Anthropic hosts).
type fallbackRoundTripper struct {
	utls     *utlsRoundTripper
	fallback http.RoundTripper
}

func (f *fallbackRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme == "https" {
		if _, ok := anthropicHosts[strings.ToLower(req.URL.Hostname())]; ok {
			return f.utls.RoundTrip(req)
		}
	}
	return f.fallback.RoundTrip(req)
}

// NewUtlsHTTPClient creates an HTTP client using utls Chrome TLS fingerprint.
// Use this for Claude API requests to match real Claude Code's TLS behavior.
// Falls back to standard transport for non-HTTPS requests.
func NewUtlsHTTPClient(args ...any) *http.Client {
	ctx, cfg, auth, timeout := parseUtlsHTTPClientArgs(args)
	return newUtlsHTTPClient(ctx, cfg, auth, timeout)
}

func parseUtlsHTTPClientArgs(args []any) (context.Context, *config.Config, *cliproxyauth.Auth, time.Duration) {
	ctx := context.Background()
	var cfg *config.Config
	var auth *cliproxyauth.Auth
	var timeout time.Duration

	switch len(args) {
	case 4:
		if candidate, ok := args[0].(context.Context); ok && candidate != nil {
			ctx = candidate
		}
		cfg, _ = args[1].(*config.Config)
		auth, _ = args[2].(*cliproxyauth.Auth)
		timeout, _ = args[3].(time.Duration)
	case 3:
		cfg, _ = args[0].(*config.Config)
		auth, _ = args[1].(*cliproxyauth.Auth)
		timeout, _ = args[2].(time.Duration)
	}

	return ctx, cfg, auth, timeout
}

func newUtlsHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	var utlsRT *utlsRoundTripper
	if cfg == nil || !cfg.DisableUTLS {
		utlsRT = cachedUtlsRoundTripper(proxyURL)
	}

	var standardTransport http.RoundTripper = &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}
	if proxyURL != "" {
		if transport := buildProxyTransport(proxyURL); transport != nil {
			standardTransport = transport
		}
	} else if ctx != nil {
		if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
			standardTransport = rt
		}
	}

	var transport http.RoundTripper = &fallbackRoundTripper{
		utls:     utlsRT,
		fallback: standardTransport,
	}
	if cfg != nil && cfg.DisableUTLS {
		transport = standardTransport
	}

	client := &http.Client{Transport: transport}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return client
}
