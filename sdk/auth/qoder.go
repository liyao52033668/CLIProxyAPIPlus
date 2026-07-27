// Package auth provides Qoder PKCE device-flow authentication.
package auth

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// qoderRefreshLead is the duration before token expiry when refresh should occur.
var qoderRefreshLead = 5 * time.Minute

// QoderAuthenticator implements browser device-flow login for the Qoder provider.
type QoderAuthenticator struct{}

// NewQoderAuthenticator constructs a new authenticator instance.
func NewQoderAuthenticator() Authenticator { return &QoderAuthenticator{} }

// Provider returns the provider key for qoder.
func (QoderAuthenticator) Provider() string { return "qoder" }

// RefreshLead instructs the manager to refresh five minutes before expiry.
func (QoderAuthenticator) RefreshLead() *time.Duration {
	return &qoderRefreshLead
}

// Login opens the Qoder browser device-flow page, polls OpenAPI for the device
// token, and returns an auth record compatible with the COSY chat API.
func (a QoderAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if opts == nil {
		opts = &LoginOptions{}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	flow, err := qoder.StartDeviceFlow(qoder.GenerateBrowserMachineID())
	if err != nil {
		return nil, fmt.Errorf("qoder: start device flow: %w", err)
	}

	if !opts.NoBrowser {
		fmt.Println("Opening browser for Qoder authentication")
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the URL manually")
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", flow.AuthURL)
		} else if errOpen := browser.OpenURL(flow.AuthURL); errOpen != nil {
			log.Warnf("Failed to open browser automatically: %v", errOpen)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", flow.AuthURL)
		}
	} else {
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", flow.AuthURL)
	}

	fmt.Println("Waiting for Qoder browser authorization...")
	fmt.Printf("Polling OpenAPI device token endpoint (timeout %s)...\n", qoder.PollTimeout)

	authSvc := qoder.NewQoderAuth(nil)
	pollCtx, cancel := context.WithTimeout(ctx, qoder.PollTimeout)
	defer cancel()
	tokenResp, errPoll := authSvc.PollDeviceToken(pollCtx, flow.Nonce, flow.Verifier)
	if errPoll != nil {
		return nil, fmt.Errorf("qoder: device poll failed: %w", errPoll)
	}
	deviceToken := tokenResp.AccessToken()
	if deviceToken == "" {
		return nil, fmt.Errorf("qoder: device poll returned empty token")
	}

	uid := strings.TrimSpace(tokenResp.UserID)
	name := strings.TrimSpace(tokenResp.UserName)
	email := ""
	if profile := authSvc.ResolveUserProfile(deviceToken); profile != nil {
		if uid == "" {
			uid = strings.TrimSpace(profile.ID)
		}
		if name == "" {
			name = strings.TrimSpace(profile.Name)
		}
		email = strings.TrimSpace(profile.Email)
	}
	if uid == "" {
		tokenHash := fmt.Sprintf("%x", sha256.Sum256([]byte(deviceToken)))
		uid = tokenHash[:16]
		log.Warnf("qoder: using derived UID from token hash: %s", uid)
	}

	now := time.Now()
	metadata := map[string]any{
		"type":                 "qoder",
		"auth_method":          "oauth",
		"login_method":         "browser",
		"login_mode":           "browser",
		"refresh_strategy":     "device-token",
		"access_token":         deviceToken,
		"security_oauth_token": deviceToken,
		"refresh_token":        strings.TrimSpace(tokenResp.RefreshToken),
		"nonce":                flow.Nonce,
		"verifier":             flow.Verifier,
		"machine_id":           flow.MachineID,
		"client_id":            flow.ClientID,
		"uid":                  uid,
		"timestamp":            now.UnixMilli(),
	}
	if name != "" {
		metadata["name"] = name
	}
	if email != "" {
		metadata["email"] = email
	}
	if expireUnix := tokenResp.ExpireUnix(); expireUnix > 0 {
		metadata["expired"] = time.Unix(expireUnix, 0).UTC().Format(time.RFC3339)
		metadata["expires_at"] = expireUnix
	}
	if refreshExpireUnix := tokenResp.RefreshExpireUnix(); refreshExpireUnix > 0 {
		metadata["refresh_token_expired"] = time.Unix(refreshExpireUnix, 0).UTC().Format(time.RFC3339)
		metadata["refresh_token_expires_at"] = refreshExpireUnix
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
