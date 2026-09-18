package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	lobsterauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/lobsterai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// LobsterAIAuthenticator implements the interactive LobsterAI browser login.
type LobsterAIAuthenticator struct {
	CallbackPort int
}

// NewLobsterAIAuthenticator constructs a new LobsterAI authenticator instance.
func NewLobsterAIAuthenticator() *LobsterAIAuthenticator {
	return &LobsterAIAuthenticator{CallbackPort: 0}
}

// Provider returns the unique provider identifier.
func (a *LobsterAIAuthenticator) Provider() string { return lobsterauth.ProviderID }

// RefreshLead refreshes access tokens ahead of their expiry so long-running
// sessions never present an expired bearer token upstream.
func (a *LobsterAIAuthenticator) RefreshLead() *time.Duration {
	lead := lobsterauth.RefreshLead
	return &lead
}

// Login runs the browser-based login with a loopback callback listener.
func (a *LobsterAIAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	service := lobsterauth.NewService(util.SetProxy(&cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second}))
	state, errState := misc.GenerateRandomState()
	if errState != nil {
		return nil, fmt.Errorf("lobsterai: state generation failed: %w", errState)
	}
	installationUUID, errUUID := lobsterauth.NewInstallationUUID()
	if errUUID != nil {
		return nil, fmt.Errorf("lobsterai: installation uuid generation failed: %w", errUUID)
	}

	callbackPort := a.CallbackPort
	if opts.CallbackPort > 0 {
		callbackPort = opts.CallbackPort
	}
	listener, errListen := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", callbackPort))
	if errListen != nil {
		return nil, fmt.Errorf("lobsterai: failed to start callback server: %w", errListen)
	}
	actualPort := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d%s", actualPort, lobsterauth.CallbackPath())
	authURL := service.BuildLoginURL(redirectURI, state)

	fmt.Println("Opening browser for LobsterAI authentication...")
	if !browser.IsAvailable() {
		log.Warn("No browser available; please open the URL manually")
		util.PrintSSHTunnelInstructions(actualPort)
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
	} else if errOpen := browser.OpenURL(authURL); errOpen != nil {
		log.Warnf("Failed to open browser automatically: %v", errOpen)
		util.PrintSSHTunnelInstructions(actualPort)
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
	}

	code, errWait := waitForLobsterAICallback(ctx, listener, state)
	if errWait != nil {
		return nil, errWait
	}

	payload, errExchange := service.ExchangeCode(ctx, code, installationUUID)
	if errExchange != nil {
		return nil, fmt.Errorf("lobsterai: code exchange failed: %w", errExchange)
	}
	usage := lobsterauth.FetchUsage(ctx, service, payload.AccessToken)
	return lobsterauth.BuildAuthRecord(payload, installationUUID, "", usage), nil
}

// waitForLobsterAICallback serves the loopback callback and returns the code.
func waitForLobsterAICallback(ctx context.Context, listener net.Listener, state string) (string, error) {
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc(lobsterauth.CallbackPath(), func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		code := strings.TrimSpace(query.Get("code"))
		gotState := strings.TrimSpace(query.Get("state"))
		errText := strings.TrimSpace(query.Get("error"))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if errText != "" {
			_, _ = w.Write([]byte("<html><body><h2>LobsterAI 授权失败</h2></body></html>"))
			select {
			case errCh <- errors.New("authorization denied: " + errText):
			default:
			}
			return
		}
		if code == "" {
			_, _ = w.Write([]byte("<html><body><h2>缺少授权码</h2></body></html>"))
			select {
			case errCh <- errors.New("missing authorization code"):
			default:
			}
			return
		}
		if gotState != "" && gotState != state {
			_, _ = w.Write([]byte("<html><body><h2>状态校验失败</h2></body></html>"))
			select {
			case errCh <- errors.New("state mismatch"):
			default:
			}
			return
		}
		_, _ = w.Write([]byte("<html><body><h2>LobsterAI 登录成功，可以关闭此窗口了</h2></body></html>"))
		select {
		case codeCh <- code:
		default:
		}
	})

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	go func() {
		if errServe := server.Serve(listener); errServe != nil && !errors.Is(errServe, http.ErrServerClosed) {
			log.Debugf("lobsterai: callback server stopped: %v", errServe)
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	fmt.Println("Waiting for LobsterAI authentication callback...")
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case errCallback := <-errCh:
		return "", errCallback
	case code := <-codeCh:
		return code, nil
	case <-time.After(5 * time.Minute):
		return "", errors.New("lobsterai: authentication timed out")
	}
}
