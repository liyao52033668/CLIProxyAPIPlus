package auth

import (
	"context"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codearts"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Login-mode metadata keys and values are owned by internal/auth/codearts so the
// management API and this authenticator write identical records.
const (
	codeArtsLoginModeMetadataKey = codearts.LoginModeMetadataKey
	codeArtsLoginModeOAuth       = codearts.LoginModeOAuth
	codeArtsLoginModeAKSK        = codearts.LoginModeAKSK
	codeArtsAccessKeyMetadataKey = codearts.AccessKeyMetadataKey
	codeArtsSecretKeyMetadataKey = codearts.SecretKeyMetadataKey
)

var codeartsRefreshLead = 4 * time.Hour

type CodeArtsAuthenticator struct{}

func NewCodeArtsAuthenticator() Authenticator { return &CodeArtsAuthenticator{} }

func (CodeArtsAuthenticator) Provider() string { return "codearts" }

func (CodeArtsAuthenticator) RefreshLead() *time.Duration {
	return &codeartsRefreshLead
}

type codeartsCallbackResult struct {
	Code     string
	Error    string
	Secret   string
	Redirect string
}

func (a CodeArtsAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	switch strings.ToLower(strings.TrimSpace(opts.Metadata[codeArtsLoginModeMetadataKey])) {
	case "", codeArtsLoginModeOAuth:
		return a.loginOAuth(ctx, cfg, opts)
	case codeArtsLoginModeAKSK:
		return a.loginAKSK(ctx, opts)
	default:
		return nil, fmt.Errorf("codearts auth: unsupported login mode %q", opts.Metadata[codeArtsLoginModeMetadataKey])
	}
}

// loginAKSK authorizes with permanent HuaweiCloud IAM access keys. These do not
// expire, so the resulting auth carries no expires_at and is never refreshed.
func (a CodeArtsAuthenticator) loginAKSK(ctx context.Context, opts *LoginOptions) (*coreauth.Auth, error) {
	ak, err := codeArtsRequireSecret(opts, codeArtsAccessKeyMetadataKey, codearts.EnvAccessKeyID, "Enter HuaweiCloud IAM Access Key ID (AK): ")
	if err != nil {
		return nil, err
	}
	sk, err := codeArtsRequireSecret(opts, codeArtsSecretKeyMetadataKey, codearts.EnvSecretAccessKey, "Enter HuaweiCloud IAM Secret Access Key (SK): ")
	if err != nil {
		return nil, err
	}

	fmt.Println("Verifying CodeArts AK/SK credentials...")
	codeartsAuth := codearts.NewCodeArtsAuth(nil)
	info, err := codeartsAuth.VerifyAKSK(ctx, ak, sk)
	if err != nil {
		return nil, err
	}

	label := codearts.AKSKLabel(info)
	fileName := codearts.AKSKFileName(label)

	// No expires_at: permanent keys must not be scheduled for refresh.
	metadata := codearts.BuildAKSKMetadata(ak, sk, info)

	fmt.Println("CodeArts AK/SK authentication successful")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: "codearts",
		FileName: fileName,
		Label:    label + " (AK/SK)",
		Metadata: metadata,
	}, nil
}

// codeArtsRequireSecret resolves a credential from login metadata, the
// environment, or an interactive prompt, in that order.
func codeArtsRequireSecret(opts *LoginOptions, metadataKey, envKey, prompt string) (string, error) {
	if opts != nil && opts.Metadata != nil {
		if value := strings.TrimSpace(opts.Metadata[metadataKey]); value != "" {
			return value, nil
		}
	}
	if raw, ok := os.LookupEnv(envKey); ok {
		if value := strings.TrimSpace(raw); value != "" {
			return value, nil
		}
	}
	if opts != nil && opts.Prompt != nil {
		value, err := opts.Prompt(prompt)
		if err != nil {
			return "", err
		}
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed, nil
		}
	}
	return "", fmt.Errorf("codearts auth: missing %s (set %s or provide it interactively)", metadataKey, envKey)
}

func (a CodeArtsAuthenticator) loginOAuth(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	ticketID, err := codearts.RandomHex(16)
	if err != nil {
		return nil, fmt.Errorf("codearts: generate ticket id: %w", err)
	}
	secret, err := codearts.RandomHex(16)
	if err != nil {
		return nil, fmt.Errorf("codearts: generate ticket secret: %w", err)
	}
	verifier, challenge, err := codearts.PKCE()
	if err != nil {
		return nil, fmt.Errorf("codearts: generate PKCE pair: %w", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("codearts: failed to find free port: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	cbChan := make(chan codeartsCallbackResult, 4)

	// The portal issues its own ticket secret and delivers it to the local
	// callback via the `secret` query param after the user authorizes; the
	// locally generated secret is never seen by the portal. Swap it in once the
	// callback arrives so ticket polling can succeed.
	var pollSecret atomic.Value
	pollSecret.Store(secret)

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/callback", func(w http.ResponseWriter, r *http.Request) {
		cb := codeartsCallbackResult{
			Code:     r.URL.Query().Get("code"),
			Error:    r.URL.Query().Get("error"),
			Secret:   r.URL.Query().Get("secret"),
			Redirect: r.URL.Query().Get("redirect"),
		}
		// Compatibility: some portal variants POST the payload.
		if cb.Code == "" && cb.Secret == "" && cb.Redirect == "" && r.Method == http.MethodPost {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
			if vals, errForm := url.ParseQuery(string(body)); errForm == nil {
				cb.Code = vals.Get("code")
				cb.Error = vals.Get("error")
				cb.Secret = vals.Get("secret")
				cb.Redirect = vals.Get("redirect")
			}
		}
		if cb.Secret != "" {
			pollSecret.Store(cb.Secret)
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if cb.Redirect != "" && cb.Code == "" {
			// Continue the portal flow: send the browser back so the login
			// finalizes while we keep polling the ticket endpoint.
			_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>CodeArts Login</title>` +
				`<meta http-equiv="refresh" content="0; url=` + html.EscapeString(cb.Redirect) + `">` +
				`</head><body style="display:flex;justify-content:center;align-items:center;height:100vh;` +
				`font-family:system-ui;background:#1a1a2e;color:#e0e0e0">` +
				`<div style="text-align:center">` +
				`<h1 style="color:#FFA500">Completing login...</h1>` +
				`<p>Redirecting to Huawei Cloud to finish. If nothing happens, ` +
				`<a href="` + html.EscapeString(cb.Redirect) + `" style="color:#4CAF50">click here</a>.</p>` +
				`</div></body></html>`))
		} else {
			_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>CodeArts Login</title></head>` +
				`<body style="display:flex;justify-content:center;align-items:center;height:100vh;` +
				`font-family:system-ui;background:#1a1a2e;color:#e0e0e0">` +
				`<div style="text-align:center">` +
				`<h1 style="color:#4CAF50">&#10003; Login Successful</h1>` +
				`<p>You can close this window and return to the terminal.</p>` +
				`</div></body></html>`))
		}

		select {
		case cbChan <- cb:
		default:
		}
	})

	srv := &http.Server{Handler: mux}
	go func() {
		if errServe := srv.Serve(listener); errServe != nil && !strings.Contains(errServe.Error(), "Server closed") {
			log.Warnf("codearts callback server error: %v", errServe)
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	codeartsAuth := codearts.NewCodeArtsAuth(nil)
	authURL := codearts.BuildAuthorizeURL(ticketID, challenge, port)

	if !opts.NoBrowser {
		fmt.Println("Opening browser for CodeArts authentication")
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the URL manually")
			util.PrintSSHTunnelInstructions(port)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		} else if errOpen := browser.OpenURL(authURL); errOpen != nil {
			log.Warnf("Failed to open browser automatically: %v", errOpen)
			util.PrintSSHTunnelInstructions(port)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		}
	} else {
		util.PrintSSHTunnelInstructions(port)
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
	}

	fmt.Println("Waiting for CodeArts authentication callback...")

	// Ticket polling (fallback channel), using the portal-issued secret once
	// the callback delivers it.
	ticketChan := make(chan *codearts.TokenResponse, 1)
	pollCtx, pollCancel := context.WithCancel(ctx)
	defer pollCancel()
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
				curSecret, _ := pollSecret.Load().(string)
				tr, errPoll := codeartsAuth.PollLoginTicket(pollCtx, ticketID, curSecret)
				if errPoll != nil {
					log.Debugf("codearts: ticket poll error: %v", errPoll)
					continue
				}
				if tr == nil || tr.Credentials.SecurityToken == "" {
					continue
				}
				ticketChan <- tr
				return
			}
		}
	}()

	timeoutTimer := time.NewTimer(5 * time.Minute)
	defer timeoutTimer.Stop()

	var tr *codearts.TokenResponse
loginLoop:
	for {
		select {
		case cb := <-cbChan:
			if cb.Error != "" {
				return nil, fmt.Errorf("codearts: authentication failed: %s", cb.Error)
			}
			if cb.Code != "" {
				fmt.Println("Callback received, exchanging authorization code...")
				tr, err = codeartsAuth.ExchangeCode(ctx, cb.Code, verifier, port)
				if err != nil {
					return nil, fmt.Errorf("codearts: %w", err)
				}
				break loginLoop
			}
			// Secret-only callback: ticket polling continues with the portal secret.
		case tk := <-ticketChan:
			tr = tk
			break loginLoop
		case <-timeoutTimer.C:
			return nil, fmt.Errorf("codearts: authentication timed out")
		}
	}

	if tr == nil || tr.Credentials.SecurityToken == "" {
		return nil, fmt.Errorf("codearts: no credentials obtained from login")
	}

	label := tr.UserName
	if label == "" {
		label = "codearts"
	}

	fmt.Println("CodeArts authentication successful")

	metadata := map[string]any{
		"type":           "codearts",
		"auth_kind":      codeArtsLoginModeOAuth,
		"ak":             tr.Credentials.AccessKeyID,
		"sk":             tr.Credentials.SecretAccessKey,
		"security_token": tr.Credentials.SecurityToken,
		"expires_at":     tr.Credentials.Expiration,
		"refresh_token":  tr.RefreshToken,
		"code_verifier":  verifier,
		"user_id":        tr.UserID,
		"user_name":      tr.UserName,
		"domain_id":      tr.DomainID,
	}

	return &coreauth.Auth{
		ID:       fmt.Sprintf("codearts-%s.json", tr.UserName),
		Provider: "codearts",
		FileName: fmt.Sprintf("codearts-%s.json", tr.UserName),
		Label:    label,
		Metadata: metadata,
	}, nil
}
