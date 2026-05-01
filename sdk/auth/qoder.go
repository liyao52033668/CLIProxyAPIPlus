// Package auth 提供 Qoder 的 PKCE + URI-scheme 登录认证功能。
package auth

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// qoderRefreshLead is the duration before token expiry when refresh should occur.
var qoderRefreshLead = 5 * time.Minute

// QoderAuthenticator implements the PKCE + URI-scheme login for the Qoder provider.
type QoderAuthenticator struct{}

// NewQoderAuthenticator constructs a new authenticator instance.
func NewQoderAuthenticator() Authenticator { return &QoderAuthenticator{} }

// Provider returns the provider key for qoder.
func (QoderAuthenticator) Provider() string { return "qoder" }

// RefreshLead instructs the manager to refresh five minutes before expiry.
func (QoderAuthenticator) RefreshLead() *time.Duration {
	return &qoderRefreshLead
}

// Login launches the browser for Qoder login, polls the device endpoint,
// and retrieves the authentication token.
func (a QoderAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}

	if opts == nil {
		opts = &LoginOptions{}
	}

	// Start local HTTP callback server first (for Windows with VBS handler)
	callbackPort := qoder.CallbackPort
	if opts.CallbackPort > 0 {
		callbackPort = opts.CallbackPort
	}

	srv, _, cbChan, errServer := startQoderCallbackServer(callbackPort)
	if errServer != nil {
		return nil, fmt.Errorf("qoder: failed to start callback server: %w", errServer)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	cleanupURIHandler := qoder.RegisterURIHandler(callbackPort)
	defer cleanupURIHandler()

	// Generate PKCE + machine ID
	nonce, challenge, verifier, err := qoder.GeneratePKCE()
	if err != nil {
		return nil, fmt.Errorf("qoder: %w", err)
	}

	machineID := qoder.GenerateMachineID("cliproxy", "00:00:00:00:00:00", "server", "x86_64")
	_ = verifier
	_ = nonce

	// Use qoder:// redirect URI (required by Qoder server)
	authURL := qoder.BuildAuthURL(nonce, challenge, machineID)

	if !opts.NoBrowser {
		fmt.Println("Opening browser for Qoder authentication")
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the URL manually")
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		} else if errOpen := browser.OpenURL(authURL); errOpen != nil {
			log.Warnf("Failed to open browser automatically: %v", errOpen)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		}
	} else {
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
	}

	fmt.Println("Waiting for Qoder authentication callback...")
	fmt.Printf("Local callback server running at: http://localhost:%d/\n", callbackPort)
	fmt.Printf("Manual completion page: http://localhost:%d/complete\n", callbackPort)
	fmt.Println("If using Windows, the callback will happen automatically.")
	fmt.Println("If using Linux/macOS:")
	fmt.Println("  1. After login success, copy the 'qoder://' callback URL from browser DevTools Network tab")
	fmt.Println("  2. Paste the URL in the terminal below, OR")
	fmt.Println("  3. Forward the callback to the local server using VBS script:")
	fmt.Printf("     http://localhost:%d/forward?url=qoder://...\n", callbackPort)
	fmt.Printf("  3. Open http://localhost:%d/complete in browser and paste the URL\n", callbackPort)
	fmt.Println()
	fmt.Println("Paste the qoder:// callback URL here, or press Enter to keep waiting...")

	var tokenString string
	var authField string
	timeoutTimer := time.NewTimer(5 * time.Minute)
	defer timeoutTimer.Stop()

	manualInputCh := make(chan string, 1)
	go func() {
		var input string
		fmt.Scanln(&input)
		input = strings.TrimSpace(input)
		manualInputCh <- input
	}()

	select {
	case cbData := <-cbChan:
		tokenString = cbData["token"]
		authField = cbData["auth"]
	case input := <-manualInputCh:
		if input == "" {
			fmt.Println("Manual input cancelled. Continuing to wait for automatic callback...")
			select {
			case cbData := <-cbChan:
				tokenString = cbData["token"]
				authField = cbData["auth"]
			case <-timeoutTimer.C:
				return nil, fmt.Errorf("qoder: authentication timed out")
			}
		} else if strings.Contains(input, "tokenString=") || strings.Contains(input, "token=") {
			qs := input
			if _, after, ok := strings.Cut(input, "?"); ok {
				qs = after
			}
			parsed, errParse := url.ParseQuery(qs)
			if errParse == nil {
				for _, k := range []string{"tokenString", "token"} {
					if v := parsed.Get(k); v != "" {
						tokenString = v
						authField = parsed.Get("auth")
						break
					}
				}
			}
		}
		if tokenString == "" {
			fmt.Println("Invalid URL format. Continuing to wait for automatic callback...")
			select {
			case cbData := <-cbChan:
				tokenString = cbData["token"]
				authField = cbData["auth"]
			case <-timeoutTimer.C:
				return nil, fmt.Errorf("qoder: authentication timed out")
			}
		}
	case <-timeoutTimer.C:
		return nil, fmt.Errorf("qoder: authentication timed out")
	}

	if tokenString == "" {
		return nil, fmt.Errorf("qoder: missing token in callback")
	}

	fmt.Printf("Token received: %s...\n", tokenString[:min(40, len(tokenString))])

	authSvc := qoder.NewQoderAuth(nil)

	// Decode auth field to get UID
	uid := ""
	name := ""
	email := ""
	if authField != "" {
		authInfo, errDecode := qoder.DecodeAuthField(authField)
		if errDecode != nil {
			log.Warnf("qoder: failed to decode auth field: %v", errDecode)
		} else {
			if v, ok := authInfo["uid"].(string); ok {
				uid = v
			}
			if v, ok := authInfo["name"].(string); ok {
				name = v
			}
		}
	}

	// If UID not found via auth field, try the user status endpoint
	if uid == "" {
		user, errUser := authSvc.FetchUserStatus(tokenString)
		if errUser != nil {
			log.Warnf("qoder: user status probe failed: %v", errUser)
		} else {
			uid = user.ID
			name = user.Name
			email = user.Email
		}
	}

	if uid == "" {
		// Fallback: derive a stable UID from the token hash so we can still save credentials
		tokenHash := fmt.Sprintf("%x", sha256.Sum256([]byte(tokenString)))
		uid = tokenHash[:16]
		log.Warnf("qoder: using derived UID from token hash: %s", uid)
	}

	now := time.Now()
	metadata := map[string]any{
		"type":         "qoder",
		"access_token": tokenString,
		"auth":         authField,
		"nonce":        nonce,
		"verifier":     verifier,
		"machine_id":   machineID,
		"uid":          uid,
		"timestamp":    now.UnixMilli(),
	}
	if name != "" {
		metadata["name"] = name
	}
	if email != "" {
		metadata["email"] = email
	}

	fileName := qoder.CredentialFileName(uid, email)
	label := name
	if label == "" {
		label = uid
	}
	if label == "" {
		label = "qoder"
	}

	fmt.Println("Qoder authentication successful")
	return &coreauth.Auth{
		ID:       fileName,
		Provider: "qoder",
		FileName: fileName,
		Label:    label,
		Metadata: metadata,
	}, nil
}

func startQoderCallbackServer(port int) (*http.Server, int, <-chan map[string]string, error) {
	if port <= 0 {
		port = qoder.CallbackPort
	}
	addr := fmt.Sprintf(":%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, 0, nil, err
	}
	port = listener.Addr().(*net.TCPAddr).Port
	resultCh := make(chan map[string]string, 1)

	mux := http.NewServeMux()

	// Root path - show server status
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>Qoder Callback Server</title></head>` +
			`<body style="display:flex;justify-content:center;align-items:center;height:100vh;` +
			`font-family:system-ui;background:#1a1a2e;color:#e0e0e0">` +
			`<div style="text-align:center;max-width:600px;padding:20px">` +
			`<h1 style="color:#4CAF50">&#10003; Server Running</h1>` +
			`<p style="margin:20px 0">Qoder authentication callback server is running.</p>` +
			`<p><a href="/complete" style="color:#4CAF50">Go to manual completion page</a></p>` +
			`</div></body></html>`))
	})

	// Handle callback from qoder:// protocol (Windows)
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		handleQoderCallback(w, r, resultCh)
	})

	// Handle callback forwarded by VBS script (Windows URI handler)
	mux.HandleFunc("/forward", func(w http.ResponseWriter, r *http.Request) {
		handleQoderCallback(w, r, resultCh)
	})

	// Handle manual callback URL input via HTTP (Linux/macOS fallback)
	mux.HandleFunc("/login-success", func(w http.ResponseWriter, r *http.Request) {
		handleQoderCallback(w, r, resultCh)
	})

	// Manual completion page for users who can't get the qoder:// callback
	mux.HandleFunc("/complete", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>Qoder Login</title></head>` +
			`<body style="display:flex;justify-content:center;align-items:center;height:100vh;` +
			`font-family:system-ui;background:#1a1a2e;color:#e0e0e0">` +
			`<div style="text-align:center;max-width:600px;padding:20px">` +
			`<h1 style="color:#4CAF50">&#10003; Qoder Authentication</h1>` +
			`<p style="margin:20px 0">Please enter your callback URL from the Qoder login page:</p>` +
			`<form method="get" action="/login-success">` +
			`<input type="text" name="url" placeholder="Enter qoder:// callback URL..." ` +
			`style="width:100%;padding:12px;font-family:monospace;font-size:14px;` +
			`background:#2a2a4e;color:#e0e0e0;border:1px solid #3a3a5e;border-radius:4px;` +
			`box-sizing:border-box;" required>` +
			`<button type="submit" style="margin-top:16px;padding:12px 32px;background:#4CAF50;color:white;` +
			`border:none;border-radius:4px;cursor:pointer;font-size:16px;width:100%">` +
			`Complete Authentication</button>` +
			`</form>` +
			`<div style="margin-top:20px;padding:16px;background:#2a2a4e;border-radius:8px;text-align:left;">` +
			`<p style="color:#4CAF50;font-weight:bold;margin-bottom:8px">How to get the callback URL:</p>` +
			`<ol style="color:#aaa;font-size:14px;line-height:1.8;margin:0;padding-left:20px;">` +
			`<li>After logging in on Qoder, check your browser's address bar</li>` +
			`<li>You'll see a URL like: <code style="background:#1a1a2e;padding:2px 4px;border-radius:2px;">https://qoder.com?qoder://...</code></li>` +
			`<li>Copy <strong>the entire URL</strong> (including https://qoder.com?)</li>` +
			`<li>Paste it into the input box above</li>` +
			`</ol>` +
			`</div>` +
			`<p style="color:#aaa;font-size:14px;margin-top:16px">Alternatively, you can check browser DevTools Network tab for failed qoder:// requests.</p>` +
			`</div></body></html>`))
	})

	srv := &http.Server{Handler: mux}
	go func() {
		if errServe := srv.Serve(listener); errServe != nil && !strings.Contains(errServe.Error(), "Server closed") {
			log.Warnf("qoder callback server error: %v", errServe)
		}
	}()

	return srv, port, resultCh, nil
}

func handleQoderCallback(w http.ResponseWriter, r *http.Request, resultCh chan<- map[string]string) {
	// First, check if there's a URL parameter (for manual input)
	rawURL := r.URL.Query().Get("url")
	rawURL, _ = url.QueryUnescape(rawURL)

	qs := r.URL.RawQuery
	prefix := "qoder://aicoding.aicoding-agent/login-success?"

	// If manual URL input, extract query string from the qoder:// URL
	if strings.HasPrefix(rawURL, prefix) {
		qs = rawURL[len(prefix):]
	} else if strings.HasPrefix(rawURL, "https://qoder.com?") {
		// Handle the new format: https://qoder.com?qoder://xxx
		qoderPart := rawURL[len("https://qoder.com?"):]
		if strings.HasPrefix(qoderPart, prefix) {
			qs = qoderPart[len(prefix):]
		}
	}

	if qs != "" {
		parsed, errParse := url.ParseQuery(qs)
		if errParse == nil {
			token := ""
			for _, k := range []string{"tokenString", "token"} {
				if v := parsed.Get(k); v != "" {
					token = v
					break
				}
			}
			if token != "" {
				resultCh <- map[string]string{
					"token": token,
					"auth":  parsed.Get("auth"),
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
}
