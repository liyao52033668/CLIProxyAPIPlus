package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	// "github.com/router-for-me/CLIProxyAPI/v7/internal/browser"

	qoderauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/qoder"
	internalwatcher "github.com/router-for-me/CLIProxyAPI/v7/internal/watcher"
	log "github.com/sirupsen/logrus"
)

var lastRefreshKeys = []string{"last_refresh", "lastRefresh", "last_refreshed_at", "lastRefreshedAt"}

const (
	anthropicCallbackPort = 54545
	geminiCallbackPort    = 8085
	codexCallbackPort     = 1455
	geminiCLIEndpoint     = "https://cloudcode-pa.googleapis.com"
	geminiCLIVersion      = "v1internal"
	gitLabLoginModeOAuth  = "oauth"
	gitLabLoginModePAT    = "pat"
)

type callbackForwarder struct {
	provider string
	server   *http.Server
	done     chan struct{}
}

type qoderCallbackServer struct {
	server *http.Server
	done   chan struct{}
	state  string
}

var (
	callbackForwardersMu   sync.Mutex
	callbackForwarders     = make(map[int]*callbackForwarder)
	qoderCallbackServersMu sync.Mutex
	qoderCallbackServers   = make(map[int]*qoderCallbackServer)
	errAuthFileMustBeJSON  = errors.New("auth file must be .json")
	errAuthFileNotFound    = errors.New("auth file not found")
)

func extractLastRefreshTimestamp(meta map[string]any) (time.Time, bool) {
	if len(meta) == 0 {
		return time.Time{}, false
	}
	for _, key := range lastRefreshKeys {
		if val, ok := meta[key]; ok {
			if ts, ok1 := parseLastRefreshValue(val); ok1 {
				return ts, true
			}
		}
	}
	return time.Time{}, false
}

func parseLastRefreshValue(v any) (time.Time, bool) {
	switch val := v.(type) {
	case string:
		s := strings.TrimSpace(val)
		if s == "" {
			return time.Time{}, false
		}
		layouts := []string{time.RFC3339, time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05Z07:00"}
		for _, layout := range layouts {
			if ts, err := time.Parse(layout, s); err == nil {
				return ts.UTC(), true
			}
		}
		if unix, err := strconv.ParseInt(s, 10, 64); err == nil && unix > 0 {
			return time.Unix(unix, 0).UTC(), true
		}
	case float64:
		if val <= 0 {
			return time.Time{}, false
		}
		return time.Unix(int64(val), 0).UTC(), true
	case int64:
		if val <= 0 {
			return time.Time{}, false
		}
		return time.Unix(val, 0).UTC(), true
	case int:
		if val <= 0 {
			return time.Time{}, false
		}
		return time.Unix(int64(val), 0).UTC(), true
	case json.Number:
		if i, err := val.Int64(); err == nil && i > 0 {
			return time.Unix(i, 0).UTC(), true
		}
	}
	return time.Time{}, false
}

func isWebUIRequest(c *gin.Context) bool {
	raw := strings.TrimSpace(c.Query("is_webui"))
	if raw == "" {
		return false
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func startCallbackForwarder(port int, provider, targetBase string) (*callbackForwarder, error) {
	callbackForwardersMu.Lock()
	prev := callbackForwarders[port]
	if prev != nil {
		delete(callbackForwarders, port)
	}
	callbackForwardersMu.Unlock()

	if prev != nil {
		stopForwarderInstance(port, prev)
	}

	addr := fmt.Sprintf("0.0.0.0:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := targetBase
		if raw := r.URL.RawQuery; raw != "" {
			if strings.Contains(target, "?") {
				target = target + "&" + raw
			} else {
				target = target + "?" + raw
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, target, http.StatusFound)
	})

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      5 * time.Second,
	}
	done := make(chan struct{})

	go func() {
		if errServe := srv.Serve(ln); errServe != nil && !errors.Is(errServe, http.ErrServerClosed) {
			log.WithError(errServe).Warnf("callback forwarder for %s stopped unexpectedly", provider)
		}
		close(done)
	}()

	forwarder := &callbackForwarder{
		provider: provider,
		server:   srv,
		done:     done,
	}

	callbackForwardersMu.Lock()
	callbackForwarders[port] = forwarder
	callbackForwardersMu.Unlock()

	log.Infof("callback forwarder for %s listening on %s", provider, addr)

	return forwarder, nil
}

type qoderCallbackResult struct {
	TokenString string
	AuthField   string
}

func stopQoderCallbackServer(port int) {
	qoderCallbackServersMu.Lock()
	prev := qoderCallbackServers[port]
	if prev != nil {
		delete(qoderCallbackServers, port)
	}
	qoderCallbackServersMu.Unlock()

	if prev != nil {
		stopQoderCallbackServerInstance(port, prev)
	}
}

func stopQoderCallbackServerInstance(port int, srv *qoderCallbackServer) {
	if srv == nil || srv.server == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := srv.server.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.WithError(err).Warnf("failed to shut down qoder callback server on port %d", port)
	}

	select {
	case <-srv.done:
	case <-time.After(2 * time.Second):
	}

	log.Infof("qoder callback server on port %d stopped", port)
}

func startQoderCallbackServerWebUI(port int, state string) (*http.Server, <-chan qoderCallbackResult, error) {
	if port <= 0 {
		port = qoderauth.CallbackPort
	}

	stopQoderCallbackServer(port)

	addr := fmt.Sprintf(":%d", port)

	var listener net.Listener
	var err error

	for i := range 3 {
		listener, err = net.Listen("tcp", addr)
		if err == nil {
			break
		}
		log.Warnf("Failed to listen on %s (attempt %d/3): %v, retrying...", addr, i+1, err)
		time.Sleep(500 * time.Millisecond)
	}

	if err != nil {
		return nil, nil, fmt.Errorf("qoder callback server: failed to listen on %s: %w", addr, err)
	}

	resultCh := make(chan qoderCallbackResult, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/forward", func(w http.ResponseWriter, r *http.Request) {
		rawURL := r.URL.Query().Get("url")
		rawURL, _ = url.QueryUnescape(rawURL)
		prefix := "qoder://aicoding.aicoding-agent/login-success?"

		qs := ""
		if strings.HasPrefix(rawURL, prefix) {
			qs = rawURL[len(prefix):]
			parsed, errParse := url.ParseQuery(qs)
			if errParse == nil {
				stateParam := parsed.Get("state")
				if stateParam != state {
					log.Warnf("qoder callback: state mismatch (expected %s, got %s)", state, stateParam)
				}
				token := ""
				for _, k := range []string{"tokenString", "token"} {
					if v := parsed.Get(k); v != "" {
						token = v
						break
					}
				}
				if token != "" {
					resultCh <- qoderCallbackResult{
						TokenString: token,
						AuthField:   parsed.Get("auth"),
					}
				}
			}
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		callbackURL := "qoder://aicoding.aicoding-agent/login-success?" + qs
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>Qoder Login</title></head>` +
			`<body style="display:flex;justify-content:center;align-items:center;height:100vh;` +
			`font-family:system-ui;background:#1a1a2e;color:#e0e0e0">` +
			`<div style="text-align:center;max-width:600px;padding:20px">` +
			`<h1 style="color:#4CAF50">&#10003; Login Successful</h1>` +
			`<p style="margin:20px 0">Your authentication token has been received.</p>` +
			`<div style="background:#2a2a4e;border-radius:8px;padding:16px;margin:20px 0">` +
			`<p style="margin-bottom:8px;color:#aaa;font-size:14px">Callback URL:</p>` +
			`<input type="text" readonly value="` + callbackURL + `" ` +
			`style="width:100%;padding:12px;font-family:monospace;font-size:12px;` +
			`background:#1a1a2e;color:#4CAF50;border:1px solid #3a3a5e;border-radius:4px;` +
			`word-break:break-all;" id="callbackUrl">` +
			`<button onclick="copyUrl()" ` +
			`style="margin-top:12px;padding:10px 24px;background:#4CAF50;color:white;` +
			`border:none;border-radius:4px;cursor:pointer;font-size:14px;">` +
			`Copy URL</button>` +
			`<p id="copyMsg" style="margin-top:10px;color:#4CAF50;font-size:14px;display:none;">` +
			`Copied to clipboard!</p>` +
			`</div>` +
			`<p style="color:#aaa;font-size:14px">If you're on Linux/macOS and the terminal didn't receive the callback automatically, ` +
			`paste the URL above into the terminal window.</p>` +
			`<p>You can close this window now.</p>` +
			`<script>function copyUrl(){var e=document.getElementById("callbackUrl");e.select();navigator.clipboard.writeText(e.value);` +
			`document.getElementById("copyMsg").style.display="block";setTimeout(function(){document.getElementById("copyMsg").style.display="none"},2000)}</script>` +
			`</div></body></html>`))
	})

	srv := &http.Server{Handler: mux}
	done := make(chan struct{})
	go func() {
		if errServe := srv.Serve(listener); errServe != nil && !strings.Contains(errServe.Error(), "Server closed") {
			log.Warnf("qoder callback server error: %v", errServe)
		}
		close(done)
	}()

	callbackSrv := &qoderCallbackServer{
		server: srv,
		done:   done,
		state:  state,
	}

	qoderCallbackServersMu.Lock()
	qoderCallbackServers[port] = callbackSrv
	qoderCallbackServersMu.Unlock()

	log.Infof("qoder callback server listening on %s", addr)
	return srv, resultCh, nil
}

func stopCallbackForwarderInstance(port int, forwarder *callbackForwarder) {
	if forwarder == nil {
		return
	}
	callbackForwardersMu.Lock()
	if current := callbackForwarders[port]; current == forwarder {
		delete(callbackForwarders, port)
	}
	callbackForwardersMu.Unlock()

	stopForwarderInstance(port, forwarder)
}

func stopForwarderInstance(port int, forwarder *callbackForwarder) {
	if forwarder == nil || forwarder.server == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := forwarder.server.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.WithError(err).Warnf("failed to shut down callback forwarder on port %d", port)
	}

	select {
	case <-forwarder.done:
	case <-time.After(2 * time.Second):
	}

	log.Infof("callback forwarder on port %d stopped", port)
}

func (h *Handler) managementCallbackURL(path string) (string, error) {
	if h == nil || h.cfg == nil || h.cfg.Port <= 0 {
		return "", fmt.Errorf("server port is not configured")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	scheme := "http"
	if h.cfg.TLS.Enable {
		scheme = "https"
	}
	return fmt.Sprintf("%s://localhost:%d%s", scheme, h.cfg.Port, path), nil
}

func invalidWatcherAuthEntry(entry internalwatcher.InvalidAuthEntry) gin.H {
	result := gin.H{
		"id":             entry.Path,
		"name":           entry.Name,
		"path":           entry.Path,
		"source":         "file",
		"status":         "invalid",
		"status_message": entry.StatusMessage,
		"unavailable":    true,
		"runtime_only":   false,
		"disabled":       false,
		"size":           entry.Size,
	}
	if !entry.ModTime.IsZero() {
		result["modtime"] = entry.ModTime
		result["updated_at"] = entry.ModTime
	}
	if trimmed := strings.TrimSpace(entry.Type); trimmed != "" {
		result["type"] = trimmed
		result["provider"] = trimmed
	}
	if trimmed := strings.TrimSpace(entry.Email); trimmed != "" {
		result["email"] = trimmed
		result["label"] = trimmed
	}
	if _, ok := result["label"]; !ok {
		result["label"] = entry.Name
	}
	return result
}
