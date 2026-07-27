// Package qoder provides OAuth2 authentication functionality for the Qoder provider.
package qoder

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

// UserStatusResponse represents the response from the user status endpoint.
type UserStatusResponse struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// DeviceTokenResponse is the token payload returned by device poll / refresh.
type DeviceTokenResponse struct {
	Token                  string `json:"token"`
	DeviceToken            string `json:"device_token"`
	RefreshToken           string `json:"refresh_token"`
	UserID                 string `json:"user_id"`
	UserName               string `json:"user_name"`
	ExpiresAt              string `json:"expires_at"`
	ExpiresIn              int64  `json:"expires_in"`
	RefreshTokenExpiresAt  string `json:"refresh_token_expires_at"`
	RefreshTokenExpiresIn  int64  `json:"refresh_token_expires_in"`
	ExpireTime             int64  `json:"expire_time"`
	RefreshTokenExpireTime int64  `json:"refresh_token_expire_time"`
}

// AccessToken returns the primary device access token.
func (r *DeviceTokenResponse) AccessToken() string {
	if r == nil {
		return ""
	}
	if token := strings.TrimSpace(r.Token); token != "" {
		return token
	}
	return strings.TrimSpace(r.DeviceToken)
}

// ExpireUnix returns the access-token expiry as unix seconds when known.
func (r *DeviceTokenResponse) ExpireUnix() int64 {
	if r == nil {
		return 0
	}
	if ts := parseMaybeUnixOrRFC3339(r.ExpiresAt); ts > 0 {
		return ts
	}
	if r.ExpireTime > 0 {
		if r.ExpireTime > 1_000_000_000_000 {
			return r.ExpireTime / 1000
		}
		return r.ExpireTime
	}
	if r.ExpiresIn > 0 {
		return time.Now().Unix() + r.ExpiresIn
	}
	return 0
}

// RefreshExpireUnix returns the refresh-token expiry as unix seconds when known.
func (r *DeviceTokenResponse) RefreshExpireUnix() int64 {
	if r == nil {
		return 0
	}
	if ts := parseMaybeUnixOrRFC3339(r.RefreshTokenExpiresAt); ts > 0 {
		return ts
	}
	if r.RefreshTokenExpireTime > 0 {
		if r.RefreshTokenExpireTime > 1_000_000_000_000 {
			return r.RefreshTokenExpireTime / 1000
		}
		return r.RefreshTokenExpireTime
	}
	if r.RefreshTokenExpiresIn > 0 {
		return time.Now().Unix() + r.RefreshTokenExpiresIn
	}
	return 0
}

// QoderAuth handles Qoder PKCE device-flow authentication.
type QoderAuth struct {
	httpClient *http.Client
}

// NewQoderAuth creates a new Qoder auth service.
func NewQoderAuth(httpClient *http.Client) *QoderAuth {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &QoderAuth{httpClient: httpClient}
}

// GeneratePKCE generates a PKCE verifier/challenge pair and a nonce.
// Matches qodercli: verifier length 43-128 from the unreserved charset.
func GeneratePKCE() (nonce, challenge, verifier string, err error) {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	length := 43 + int(randomUint32()%86)
	raw := make([]byte, length)
	if _, err = rand.Read(raw); err != nil {
		return "", "", "", fmt.Errorf("qoder: generate verifier: %w", err)
	}
	var b strings.Builder
	b.Grow(length)
	for _, v := range raw {
		b.WriteByte(charset[int(v)%len(charset)])
	}
	verifier = b.String()

	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	nonce = uuid.NewString()
	return nonce, challenge, verifier, nil
}

// BuildAuthURL constructs the Qoder login URL for browser-based authentication.
// Prefer BuildDeviceAuthURL for the qodercli-compatible device flow.
func BuildAuthURL(nonce, challenge, machineID string) string {
	return BuildDeviceAuthURL(nonce, challenge, machineID, ClientIDCLI)
}

// BuildAuthURLWithRedirect constructs the Qoder login URL with a custom redirect URI.
// Deprecated: qodercli device flow uses client_id instead of redirect_uri.
func BuildAuthURLWithRedirect(nonce, challenge, machineID, redirectURI string) string {
	params := url.Values{}
	params.Set("nonce", nonce)
	params.Set("challenge", challenge)
	params.Set("challenge_method", "S256")
	params.Set("redirect_uri", redirectURI)
	params.Set("machine_id", machineID)
	return AuthBase + SelectAccountsPath + "?" + params.Encode()
}

// BuildAuthURLWithRedirectAndState constructs the Qoder login URL with redirect URI and state.
// Deprecated: qodercli device flow uses client_id instead of redirect_uri.
func BuildAuthURLWithRedirectAndState(nonce, challenge, machineID, redirectURI, state string) string {
	params := url.Values{}
	params.Set("nonce", nonce)
	params.Set("challenge", challenge)
	params.Set("challenge_method", "S256")
	params.Set("redirect_uri", redirectURI)
	params.Set("machine_id", machineID)
	if state != "" {
		params.Set("state", state)
	}
	return AuthBase + SelectAccountsPath + "?" + params.Encode()
}

// BuildDeviceAuthURL constructs the qodercli-compatible device login URL.
func BuildDeviceAuthURL(nonce, challenge, machineID, clientID string) string {
	if strings.TrimSpace(clientID) == "" {
		clientID = ClientIDCLI
	}
	params := url.Values{}
	params.Set("challenge", challenge)
	params.Set("challenge_method", "S256")
	params.Set("nonce", nonce)
	params.Set("machine_id", machineID)
	params.Set("client_id", clientID)
	return AuthBase + SelectAccountsPath + "?" + params.Encode()
}

// DeviceFlow holds the state needed to complete a browser device login.
type DeviceFlow struct {
	AuthURL   string
	Nonce     string
	Challenge string
	Verifier  string
	MachineID string
	ClientID  string
}

// StartDeviceFlow creates a qodercli-compatible browser device login session.
func StartDeviceFlow(machineID string) (*DeviceFlow, error) {
	nonce, challenge, verifier, err := GeneratePKCE()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(machineID) == "" {
		machineID = GenerateMachineID("cliproxy", uuid.NewString(), "server", "x86_64")
	}
	return &DeviceFlow{
		AuthURL:   BuildDeviceAuthURL(nonce, challenge, machineID, ClientIDCLI),
		Nonce:     nonce,
		Challenge: challenge,
		Verifier:  verifier,
		MachineID: machineID,
		ClientID:  ClientIDCLI,
	}, nil
}

// PollDeviceToken polls OpenAPI until the user completes browser login.
func (o *QoderAuth) PollDeviceToken(ctx context.Context, nonce, verifier string) (*DeviceTokenResponse, error) {
	return o.PollDeviceTokenWithBaseURL(ctx, OpenAPIBase, nonce, verifier)
}

// PollDeviceTokenWithBaseURL polls the provided OpenAPI base for a device token.
func (o *QoderAuth) PollDeviceTokenWithBaseURL(ctx context.Context, baseURL, nonce, verifier string) (*DeviceTokenResponse, error) {
	nonce = strings.TrimSpace(nonce)
	verifier = strings.TrimSpace(verifier)
	if nonce == "" || verifier == "" {
		return nil, fmt.Errorf("qoder device poll: nonce and verifier are required")
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = OpenAPIBase
	}
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(PollTimeout)
	params := url.Values{}
	params.Set("nonce", nonce)
	params.Set("verifier", verifier)
	params.Set("challenge_method", "S256")
	reqURL := baseURL + DeviceTokenPollPath + "?" + params.Encode()

	client := o.httpClient
	if client == nil {
		client = &http.Client{}
	}
	consecutiveErrors := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("qoder device poll: timed out after %s", PollTimeout)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, fmt.Errorf("qoder device poll: create request: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "qoder/"+GetCosyVersion())

		resp, errDo := client.Do(req)
		if errDo != nil {
			consecutiveErrors++
			if consecutiveErrors >= MaxConsecutiveErrors {
				return nil, fmt.Errorf("qoder device poll: too many consecutive errors: %w", errDo)
			}
			if errSleep := sleepWithContext(ctx, PollInterval); errSleep != nil {
				return nil, errSleep
			}
			continue
		}

		body, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if errRead != nil {
			return nil, fmt.Errorf("qoder device poll: read response: %w", errRead)
		}

		switch {
		case resp.StatusCode == http.StatusNotFound:
			consecutiveErrors = 0
			if errSleep := sleepWithContext(ctx, PollInterval); errSleep != nil {
				return nil, errSleep
			}
			continue
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			consecutiveErrors = 0
			var pollResp DeviceTokenResponse
			if err := json.Unmarshal(body, &pollResp); err != nil {
				return nil, fmt.Errorf("qoder device poll: parse response: %w", err)
			}
			if pollResp.AccessToken() == "" {
				if errSleep := sleepWithContext(ctx, PollInterval); errSleep != nil {
					return nil, errSleep
				}
				continue
			}
			return &pollResp, nil
		default:
			return nil, fmt.Errorf("qoder device poll: request failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
	}
}

// RefreshDeviceToken refreshes a browser device token via OpenAPI.
func (o *QoderAuth) RefreshDeviceToken(ctx context.Context, refreshToken string) (*DeviceTokenResponse, error) {
	return o.RefreshDeviceTokenWithBaseURL(ctx, OpenAPIBase, refreshToken)
}

// RefreshDeviceTokenWithBaseURL refreshes a browser device token against the provided base URL.
func (o *QoderAuth) RefreshDeviceTokenWithBaseURL(ctx context.Context, baseURL, refreshToken string) (*DeviceTokenResponse, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, fmt.Errorf("qoder device refresh: missing refresh token")
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = OpenAPIBase
	}
	if ctx == nil {
		ctx = context.Background()
	}
	payload, err := json.Marshal(map[string]string{"refresh_token": refreshToken})
	if err != nil {
		return nil, fmt.Errorf("qoder device refresh: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+DeviceTokenRefreshPath, strings.NewReader(string(payload)))
	if err != nil {
		return nil, fmt.Errorf("qoder device refresh: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "qoder/"+GetCosyVersion())

	client := o.httpClient
	if client == nil {
		client = &http.Client{}
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("qoder device refresh: execute request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("qoder device refresh: close body error: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		return nil, fmt.Errorf("qoder device refresh: read response: %w", errRead)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("qoder device refresh: request failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tokenResp DeviceTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("qoder device refresh: parse response: %w", err)
	}
	if tokenResp.AccessToken() == "" {
		return nil, fmt.Errorf("qoder device refresh: missing device token in response")
	}
	return &tokenResp, nil
}

// FetchUserInfo retrieves user profile via OpenAPI /api/v1/userinfo.
func (o *QoderAuth) FetchUserInfo(deviceToken string) (*UserStatusResponse, error) {
	return o.FetchUserInfoWithBaseURL(OpenAPIBase, deviceToken)
}

// FetchUserInfoWithBaseURL retrieves user profile against the provided base URL.
func (o *QoderAuth) FetchUserInfoWithBaseURL(baseURL, deviceToken string) (*UserStatusResponse, error) {
	deviceToken = strings.TrimSpace(deviceToken)
	if deviceToken == "" {
		return nil, fmt.Errorf("qoder userinfo: missing device token")
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = OpenAPIBase
	}
	req, err := http.NewRequest(http.MethodGet, baseURL+UserInfoPath, nil)
	if err != nil {
		return nil, fmt.Errorf("qoder userinfo: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+deviceToken)
	req.Header.Set("User-Agent", "qoder/"+GetCosyVersion())

	resp, errDo := o.httpClient.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("qoder userinfo: execute request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("qoder userinfo: close body error: %v", errClose)
		}
	}()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		bodyBytes, errRead := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		if errRead != nil {
			return nil, fmt.Errorf("qoder userinfo: read response: %w", errRead)
		}
		body := strings.TrimSpace(string(bodyBytes))
		if body == "" {
			return nil, fmt.Errorf("qoder userinfo: request failed: status %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("qoder userinfo: request failed: status %d: %s", resp.StatusCode, body)
	}
	var raw map[string]any
	if errDecode := json.NewDecoder(resp.Body).Decode(&raw); errDecode != nil {
		return nil, fmt.Errorf("qoder userinfo: decode response: %w", errDecode)
	}
	user := &UserStatusResponse{
		ID:    firstString(raw, "id", "user_id", "uid"),
		Name:  firstString(raw, "name", "username", "user_name"),
		Email: firstString(raw, "email"),
	}
	if user.ID == "" {
		return nil, fmt.Errorf("qoder userinfo: response missing id")
	}
	return user, nil
}

// FetchUserStatus retrieves user info using a device token.
func (o *QoderAuth) FetchUserStatus(deviceToken string) (*UserStatusResponse, error) {
	return o.FetchUserStatusWithBaseURL(OpenAPIBase, deviceToken)
}

// FetchUserStatusWithBaseURL retrieves user info using a device token against the provided base URL.
func (o *QoderAuth) FetchUserStatusWithBaseURL(baseURL, deviceToken string) (*UserStatusResponse, error) {
	deviceToken = strings.TrimSpace(deviceToken)
	if deviceToken == "" {
		return nil, fmt.Errorf("qoder user status: missing device token")
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = OpenAPIBase
	}
	reqURL := baseURL + UserStatusPath
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("qoder user status: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+deviceToken)
	req.Header.Set("Cosy-Version", GetCosyVersion())
	req.Header.Set("Cosy-Clienttype", "0")

	resp, errDo := o.httpClient.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("qoder user status: execute request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("qoder user status: close body error: %v", errClose)
		}
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		bodyBytes, errRead := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		if errRead != nil {
			return nil, fmt.Errorf("qoder user status: read response: %w", errRead)
		}
		body := strings.TrimSpace(string(bodyBytes))
		if body == "" {
			return nil, fmt.Errorf("qoder user status: request failed: status %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("qoder user status: request failed: status %d: %s", resp.StatusCode, body)
	}

	var user UserStatusResponse
	if errDecode := json.NewDecoder(resp.Body).Decode(&user); errDecode != nil {
		return nil, fmt.Errorf("qoder user status: decode response: %w", errDecode)
	}
	return &user, nil
}

// ResolveUserProfile prefers OpenAPI userinfo and falls back to legacy status.
func (o *QoderAuth) ResolveUserProfile(deviceToken string) *UserStatusResponse {
	if user, err := o.FetchUserInfo(deviceToken); err == nil && user != nil && strings.TrimSpace(user.ID) != "" {
		return user
	} else if err != nil {
		log.Debugf("qoder: userinfo probe failed: %v", err)
	}
	if user, err := o.FetchUserStatus(deviceToken); err == nil && user != nil {
		return user
	} else if err != nil {
		log.Debugf("qoder: user status probe failed: %v", err)
	}
	return nil
}

// DecodeAuthField decodes the obfuscated auth callback field to extract user info.
func DecodeAuthField(authStr string) ([]byte, error) {
	if strings.TrimSpace(authStr) == "" {
		return nil, fmt.Errorf("qoder: empty auth field")
	}

	// Reverse custom alphabet to standard base64
	var stdBuilder strings.Builder
	for _, c := range authStr {
		if c == CustomPad {
			stdBuilder.WriteByte('=')
		} else {
			idx := strings.Index(CustomAlphabet, string(c))
			if idx >= 0 {
				stdBuilder.WriteByte(StdAlphabet[idx])
			} else {
				return nil, fmt.Errorf("qoder: char out of custom alphabet: %c", c)
			}
		}
	}

	stdB64 := stdBuilder.String()
	n := len(stdB64)
	if n == 0 {
		return nil, fmt.Errorf("qoder: empty after alphabet conversion")
	}

	// Reverse the rearrangement: original = last_third + middle_third + first_third
	// So to recover: first_third + middle_third + last_third
	a := n / 3
	if a == 0 {
		return nil, fmt.Errorf("qoder: string too short to decode")
	}

	// reconstructed = stdB64[n-a:] + stdB64[a:n-a] + stdB64[:a]
	rearranged := stdB64[n-a:] + stdB64[:a] + stdB64[a:n-a]

	return base64.StdEncoding.DecodeString(rearranged)
}

// DecodeAuthFieldToJSON decodes the auth field and parses the result as JSON.
func DecodeAuthFieldToJSON(authStr string) (map[string]any, error) {
	raw, err := DecodeAuthField(authStr)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("qoder: failed to parse auth JSON: %w", err)
	}
	return result, nil
}

// GenerateMachineID creates a deterministic machine identifier.
func GenerateMachineID(hostname, macAddr, system, machine string) string {
	raw := fmt.Sprintf("%s-%s-%s-%s", hostname, macAddr, system, machine)
	digest := sha256.Sum256([]byte(raw))
	encoded := base64.RawURLEncoding.EncodeToString(digest[:])
	var parts []string
	for i := 0; i < len(encoded); i += 22 {
		end := min(i+22, len(encoded))
		parts = append(parts, encoded[i:end])
	}
	return strings.Join(parts, "-")
}

// GeneratePATMachineID creates a stable machine identifier isolated per PAT.
func GeneratePATMachineID(personalAccessToken string) string {
	personalAccessToken = strings.TrimSpace(personalAccessToken)
	if decoded, err := url.QueryUnescape(personalAccessToken); err == nil {
		personalAccessToken = decoded
	}
	return GenerateMachineID("cliproxy-pat", personalAccessToken, "server", "x86_64")
}

// GenerateBrowserMachineID creates a unique machine identifier for a browser login.
func GenerateBrowserMachineID() string {
	return GenerateMachineID("cliproxy-browser", uuid.NewString(), "server", "x86_64")
}

// PollResponse represents the response from the legacy poll endpoint.
type PollResponse struct {
	Token  string `json:"token"`
	Auth   string `json:"auth"`
	Status string `json:"status"`
	Error  string `json:"error"`
}

// PollForToken polls the legacy center device endpoint until authentication completes.
// Deprecated: use PollDeviceToken which matches qodercli OpenAPI device flow.
func PollForToken(ctx context.Context, machineID, challenge string) (*PollResponse, error) {
	delay := PollBaseDelay
	consecutiveErrors := 0

	client := &http.Client{Timeout: 10 * time.Second}

	for range PollMaxAttempts {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}

		params := url.Values{}
		params.Set("machine_id", machineID)
		params.Set("challenge", challenge)

		reqURL := CenterBase + "/device/token?" + params.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, fmt.Errorf("qoder poll: create request: %w", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			consecutiveErrors++
			if consecutiveErrors >= MaxConsecutiveErrors {
				return nil, fmt.Errorf("qoder poll: too many consecutive errors: %w", err)
			}
			delay = min(time.Duration(float64(delay)*PollBackoffMultiply), PollMaxDelay)
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnauthorized {
			consecutiveErrors = 0
			delay = min(time.Duration(float64(delay)*PollBackoffMultiply), PollMaxDelay)
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			var pollResp PollResponse
			if err := json.Unmarshal(body, &pollResp); err != nil {
				return nil, fmt.Errorf("qoder poll: parse response: %w", err)
			}
			if pollResp.Status == "pending" {
				consecutiveErrors = 0
				delay = min(time.Duration(float64(delay)*PollBackoffMultiply), PollMaxDelay)
				continue
			}
			if pollResp.Token != "" {
				return &pollResp, nil
			}
			if pollResp.Error != "" {
				return nil, fmt.Errorf("qoder poll: %s", pollResp.Error)
			}
			return nil, fmt.Errorf("qoder poll: unexpected response")
		}

		return nil, fmt.Errorf("qoder poll: request failed: status %d: %s", resp.StatusCode, string(body))
	}

	return nil, fmt.Errorf("qoder poll: max attempts reached")
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func parseMaybeUnixOrRFC3339(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if ts, err := time.Parse(time.RFC3339, raw); err == nil {
		return ts.Unix()
	}
	if ts, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return ts.Unix()
	}
	return 0
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func randomUint32() uint32 {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return uint32(time.Now().UnixNano())
	}
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
