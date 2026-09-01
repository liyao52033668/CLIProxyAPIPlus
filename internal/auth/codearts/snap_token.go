package codearts

// snap-manager / STS OAuth2 token client for the CodeArts Agent portal channel.
//
// Flow (aligned with the huaweicloud.authentication extension):
//   - authorize: codearts.huaweicloud.com/portal/authorize (PKCE S256)
//   - exchange:  POST sts.cn-north-4.../v1/oauth2/tokens (authorization_code, DPoP)
//   - refresh:   POST sts.cn-north-4.../v1/oauth2/tokens (grant_type=refresh_token, DPoP)
//   - fallback:  GET snap-manager/v1/login/ticket (ticket polling)
//
// The token response carries refresh_token + code_verifier, which enables
// reliable refresh_token-based renewal without re-authentication.

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	// SnapManagerHost is the login/token service (snap-manager).
	SnapManagerHost = "https://snap-access.cn-north-4.myhuaweicloud.com/snap-manager"
	// STSHost issues temporary AK/SK + security_token and refresh grants.
	STSHost = "https://sts.cn-north-4.myhuaweicloud.com"
	// PortalHost is the HuaweiCloud CodeArts portal OAuth authorize entry.
	PortalHost = "https://codearts.huaweicloud.com/portal"
	// SnapClientID is the OAuth client id (auth extension env.uriScheme).
	SnapClientID = "codearts"

	epOAuthTokens = "/v1/oauth2/tokens"
	epLoginTicket = "/v1/login/ticket"

	snapPluginName = "snap_AIIDE"
	// snapPluginVersion mirrors the current huaweicloud.vscode-codebot plugin
	snapPluginVersion = "26.8.203"
)

// Credentials is the STS temporary credential block returned by the token endpoint.
type Credentials struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SecurityToken   string `json:"security_token"`
	Expiration      string `json:"expiration"`
}

// LegacyCredential is the legacy ticket-channel credential block.
type LegacyCredential struct {
	Access        string `json:"access"`
	Secret        string `json:"secret"`
	SecurityToken string `json:"securitytoken"`
	ExpiresAt     string `json:"expires_at"`
}

// TokenResponse is the oauth2/tokens response with user info and credentials.
type TokenResponse struct {
	UserID       string           `json:"user_id"`
	UserName     string           `json:"user_name"`
	DomainID     string           `json:"domain_id"`
	RefreshToken string           `json:"refresh_token"`
	Credentials  Credentials      `json:"credentials"`
	Credential   LegacyCredential `json:"credential"`
}

// BuildAuthorizeURL constructs the portal login URL (PKCE S256).
func BuildAuthorizeURL(ticketID, codeChallenge string, port int) string {
	q := url.Values{}
	q.Set("theme", "dark")
	q.Set("locale", "zh-cn")
	q.Set("uri_scheme", SnapClientID)
	q.Set("client_id", SnapClientID)
	q.Set("port", fmt.Sprint(port))
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	q.Set("ticket_id", ticketID)
	q.Set("plugin-name", snapPluginName)
	q.Set("plugin-version", snapPluginVersion)
	// is_redirect=true asks the portal to redirect over HTTP back to the local
	// callback instead of the codearts:// custom scheme (which needs the desktop
	// app to forward the code). Mirrors the legacy devcloud login flow.
	q.Set("is_redirect", "true")
	return PortalHost + "/authorize?" + q.Encode()
}

// PKCE generates a code_verifier / code_challenge pair (S256).
func PKCE() (verifier, challenge string, err error) {
	b := make([]byte, 64)
	if _, err := crand.Read(b); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

// RandomHex returns n random bytes hex-encoded.
func RandomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := crand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ExchangeCode exchanges an authorization code for a token (authorization_code grant).
func (a *CodeArtsAuth) ExchangeCode(ctx context.Context, code, codeVerifier string, port int) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("client_id", SnapClientID)
	form.Set("code", code)
	form.Set("code_verifier", codeVerifier)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", fmt.Sprintf("http://127.0.0.1:%d/oauth/callback", port))
	return a.requestToken(ctx, STSHost+epOAuthTokens, form)
}

// RefreshWithRefreshToken renews credentials via the refresh_token grant (DPoP).
// Unlike the AK/SK-based /v2/login/refresh, this relies only on the persisted
// refresh_token + PKCE code_verifier and is the recommended renewal path.
func (a *CodeArtsAuth) RefreshWithRefreshToken(ctx context.Context, token *CodeArtsTokenData) (*CodeArtsTokenData, error) {
	if token == nil || token.RefreshToken == "" {
		return nil, fmt.Errorf("codearts: cannot refresh without refresh_token")
	}
	form := url.Values{}
	form.Set("client_id", SnapClientID)
	form.Set("code_verifier", token.CodeVerifier)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", token.RefreshToken)
	resp, err := a.requestToken(ctx, STSHost+epOAuthTokens, form)
	if err != nil {
		return nil, err
	}
	return tokenFromTokenResponse(resp, token), nil
}

// PollLoginTicket polls the snap-manager login ticket endpoint (fallback channel).
func (a *CodeArtsAuth) PollLoginTicket(ctx context.Context, ticketID, secret string) (*TokenResponse, error) {
	path := SnapManagerHost + epLoginTicket + "?ticket_id=" + url.QueryEscape(ticketID) + "&secret=" + url.QueryEscape(secret)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("plugin-name", snapPluginName)
	req.Header.Set("plugin-version", snapPluginVersion)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("codearts: ticket poll failed http=%d body=%s", resp.StatusCode, truncateCodeArtsStr(string(raw), 300))
	}
	var out TokenResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("codearts: parse ticket response: %w body=%s", err, truncateCodeArtsStr(string(raw), 200))
	}
	normalizeTokenResponse(&out)
	return &out, nil
}

func (a *CodeArtsAuth) requestToken(ctx context.Context, endpoint string, form url.Values) (*TokenResponse, error) {
	kp, err := newDpopKeyPair()
	if err != nil {
		return nil, fmt.Errorf("codearts: dpop keypair: %w", err)
	}
	proof, err := signDpopProof(kp, endpoint)
	if err != nil {
		return nil, fmt.Errorf("codearts: dpop proof: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("DPoP", proof)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("codearts: token request failed http=%d path=%s body=%s",
			resp.StatusCode, epOAuthTokens, truncateCodeArtsStr(string(raw), 300))
	}

	var out TokenResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("codearts: parse token response: %w body=%s", err, truncateCodeArtsStr(string(raw), 200))
	}
	normalizeTokenResponse(&out)
	if out.Credentials.SecurityToken == "" {
		return nil, fmt.Errorf("codearts: token response missing credentials: %s", truncateCodeArtsStr(string(raw), 200))
	}
	log.Debugf("codearts: token exchanged user_id=%s refresh=%v", out.UserID, out.RefreshToken != "")
	return &out, nil
}

// normalizeTokenResponse folds the legacy credential block into Credentials.
func normalizeTokenResponse(out *TokenResponse) {
	if out.Credentials.SecurityToken == "" && out.Credential.SecurityToken != "" {
		out.Credentials = Credentials{
			AccessKeyID:     out.Credential.Access,
			SecretAccessKey: out.Credential.Secret,
			SecurityToken:   out.Credential.SecurityToken,
			Expiration:      out.Credential.ExpiresAt,
		}
	}
}

// tokenFromTokenResponse converts a token response into CodeArtsTokenData,
// preserving refresh_token / code_verifier / x_auth_token and user identity.
func tokenFromTokenResponse(resp *TokenResponse, prev *CodeArtsTokenData) *CodeArtsTokenData {
	expiresAt, _ := time.Parse(time.RFC3339, resp.Credentials.Expiration)
	td := &CodeArtsTokenData{
		AK:            resp.Credentials.AccessKeyID,
		SK:            resp.Credentials.SecretAccessKey,
		SecurityToken: resp.Credentials.SecurityToken,
		ExpiresAt:     expiresAt,
		RefreshToken:  resp.RefreshToken,
	}
	if prev != nil {
		td.CodeVerifier = prev.CodeVerifier
		td.XAuthToken = prev.XAuthToken
		td.UserID = prev.UserID
		td.UserName = prev.UserName
		td.DomainID = prev.DomainID
		td.Email = prev.Email
		if td.RefreshToken == "" {
			td.RefreshToken = prev.RefreshToken
		}
	}
	if td.UserID == "" {
		td.UserID = resp.UserID
	}
	if td.UserName == "" {
		td.UserName = resp.UserName
	}
	if td.DomainID == "" {
		td.DomainID = resp.DomainID
	}
	return td
}

func truncateCodeArtsStr(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}
