package freebuff

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// DefaultLoginBaseURL is the upstream host that issues CLI login codes.
const DefaultLoginBaseURL = "https://www.codebuff.com"

// loginUserAgent matches the official CLI, which is a Bun binary.
const loginUserAgent = "Bun/1.3.11"

const (
	loginCodeTimeout   = 30 * time.Second
	loginStatusTimeout = 15 * time.Second
	loginVerifyTimeout = 15 * time.Second
)

// LoginCode is the payload returned by /api/auth/cli/code. The login URL is
// opened by the user in a browser to authorize this fingerprint.
type LoginCode struct {
	FingerprintID   string `json:"-"`
	LoginURL        string `json:"loginUrl"`
	FingerprintHash string `json:"fingerprintHash"`
	ExpiresAt       int64  `json:"expiresAt"`
}

// LoginUser is the authorized account returned by /api/auth/cli/status once
// the user finishes the browser login.
type LoginUser struct {
	Email     string `json:"email"`
	Name      string `json:"name"`
	AuthToken string `json:"authToken"`
}

// NewFingerprintID generates the client fingerprint id that identifies one
// login attempt, mirroring the official CLI shape ("fb-<16 hex>").
func NewFingerprintID() (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("freebuff login: generate fingerprint id: %w", err)
	}
	return "fb-" + hex.EncodeToString(raw), nil
}

// RequestLoginCode asks the upstream for a browser login URL for the given
// fingerprint. The caller supplies the HTTP client so proxy settings apply.
func RequestLoginCode(ctx context.Context, client *http.Client, baseURL, fingerprintID string) (*LoginCode, error) {
	endpoint, err := loginEndpoint(baseURL, "/api/auth/cli/code")
	if err != nil {
		return nil, err
	}
	payload, errMarshal := json.Marshal(map[string]string{"fingerprintId": fingerprintID})
	if errMarshal != nil {
		return nil, fmt.Errorf("freebuff login: encode request: %w", errMarshal)
	}
	req, errNew := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if errNew != nil {
		return nil, fmt.Errorf("freebuff login: build request: %w", errNew)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", loginUserAgent)

	body, status, errDo := doLoginRequest(ctx, client, req, loginCodeTimeout)
	if errDo != nil {
		return nil, errDo
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("freebuff login: code request failed with status %d: %s", status, snippet(body))
	}
	var code LoginCode
	if errUnmarshal := json.Unmarshal(body, &code); errUnmarshal != nil {
		return nil, fmt.Errorf("freebuff login: invalid code response: %w", errUnmarshal)
	}
	if code.LoginURL == "" || code.FingerprintHash == "" {
		return nil, errors.New("freebuff login: code response missing loginUrl or fingerprintHash")
	}
	code.FingerprintID = fingerprintID
	return &code, nil
}

// PollLoginStatus checks the upstream once for a completed browser login.
// pending is true while the user has not finished authorizing (upstream 401).
func PollLoginStatus(ctx context.Context, client *http.Client, baseURL string, code *LoginCode) (*LoginUser, bool, error) {
	if code == nil || code.FingerprintID == "" {
		return nil, false, errors.New("freebuff login: nil login code")
	}
	params := url.Values{}
	params.Set("fingerprintId", code.FingerprintID)
	params.Set("fingerprintHash", code.FingerprintHash)
	params.Set("expiresAt", fmt.Sprintf("%d", code.ExpiresAt))
	endpoint, err := loginEndpoint(baseURL, "/api/auth/cli/status")
	if err != nil {
		return nil, false, err
	}
	endpoint += "?" + params.Encode()

	req, errNew := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if errNew != nil {
		return nil, false, fmt.Errorf("freebuff login: build status request: %w", errNew)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", loginUserAgent)

	body, status, errDo := doLoginRequest(ctx, client, req, loginStatusTimeout)
	if errDo != nil {
		return nil, false, errDo
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return nil, true, nil
	}
	if status < 200 || status >= 300 {
		return nil, false, fmt.Errorf("freebuff login: status check failed with status %d: %s", status, snippet(body))
	}
	var wrapper struct {
		User *LoginUser `json:"user"`
	}
	if errUnmarshal := json.Unmarshal(body, &wrapper); errUnmarshal != nil {
		return nil, false, fmt.Errorf("freebuff login: invalid status response: %w", errUnmarshal)
	}
	if wrapper.User == nil || wrapper.User.AuthToken == "" {
		return nil, true, nil
	}
	return wrapper.User, false, nil
}

// VerifyToken checks that a token is accepted by the freebuff session
// endpoint. 401/403 mean the credential is rejected; other statuses are
// treated as accepted because the request only serves as an auth probe.
func VerifyToken(ctx context.Context, client *http.Client, baseURL, token string) error {
	endpoint, err := loginEndpoint(baseURL, "/api/v1/freebuff/session")
	if err != nil {
		return err
	}
	req, errNew := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if errNew != nil {
		return fmt.Errorf("freebuff login: build verify request: %w", errNew)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", loginUserAgent)

	body, status, errDo := doLoginRequest(ctx, client, req, loginVerifyTimeout)
	if errDo != nil {
		return errDo
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return fmt.Errorf("freebuff login: token rejected with status %d: %s", status, snippet(body))
	}
	return nil
}

// ValidateLoginBaseURL enforces the SSRF constraints for caller-supplied
// upstream base URLs: http/https only, and never loopback, private, or
// otherwise reserved hosts.
func ValidateLoginBaseURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return DefaultLoginBaseURL, nil
	}
	parsed, errParse := url.Parse(trimmed)
	if errParse != nil {
		return "", fmt.Errorf("freebuff login: invalid base url: %w", errParse)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("freebuff login: base url scheme must be http or https: %q", parsed.Scheme)
	}
	host := parsed.Hostname()
	if host == "" {
		return "", errors.New("freebuff login: base url missing host")
	}
	if validateLoginHost(host) {
		return "", fmt.Errorf("freebuff login: base url host is not allowed: %q", host)
	}
	return strings.TrimRight(trimmed, "/"), nil
}

// validateLoginHost is a package-level seam so tests can point the client at
// an httptest loopback server without weakening production validation.
var validateLoginHost = isForbiddenLoginHost

func isForbiddenLoginHost(host string) bool {
	normalized := strings.ToLower(strings.TrimSuffix(host, "."))
	if normalized == "localhost" || strings.HasSuffix(normalized, ".localhost") ||
		strings.HasSuffix(normalized, ".local") || strings.HasSuffix(normalized, ".internal") {
		return true
	}
	addr, errAddr := netip.ParseAddr(normalized)
	if errAddr != nil {
		return false
	}
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsUnspecified() || addr.IsMulticast() {
		return true
	}
	if addr.Is4() {
		if isCGNat(addr) || isReservedIPv4(addr) {
			return true
		}
	}
	return false
}

func isCGNat(addr netip.Addr) bool {
	octets := addr.As4()
	return octets[0] == 100 && octets[1] >= 64 && octets[1] <= 127
}

func isReservedIPv4(addr netip.Addr) bool {
	octets := addr.As4()
	if octets[0] == 0 {
		return true
	}
	if net.IP(octets[:]).Equal(net.IPv4bcast) {
		return true
	}
	return false
}

func loginEndpoint(baseURL, path string) (string, error) {
	validated, errValidate := ValidateLoginBaseURL(baseURL)
	if errValidate != nil {
		return "", errValidate
	}
	return validated + path, nil
}

func doLoginRequest(ctx context.Context, client *http.Client, req *http.Request, timeout time.Duration) ([]byte, int, error) {
	if client == nil {
		client = &http.Client{Timeout: timeout}
	} else {
		client = &http.Client{
			Transport: client.Transport,
			Timeout:   timeout,
		}
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, 0, fmt.Errorf("freebuff login: request failed: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			_ = errClose
		}
	}()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		return nil, resp.StatusCode, fmt.Errorf("freebuff login: read response: %w", errRead)
	}
	return body, resp.StatusCode, nil
}

func snippet(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 200 {
		return text[:200] + "...[truncated]"
	}
	if text == "" {
		return "empty body"
	}
	return text
}
