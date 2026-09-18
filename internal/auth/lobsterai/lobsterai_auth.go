// Package lobsterai implements the LobsterAI (NetEase Youdao) credential flow:
// portal login URL construction, authorization-code exchange, access-token
// refresh, and account usage lookup.
//
// The wire contract mirrors the official desktop client: an auth envelope of
// {code, message, data}, a Bearer access token for API calls, and the
// X-LobsterAI-Client-* identity headers that gate server-side capabilities.
package lobsterai

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultServerBaseURL is the LobsterAI API host used by the official client.
	DefaultServerBaseURL = "https://lobsterai-server.youdao.com"
	// DefaultPortalURL is the browser portal that renders the login page.
	DefaultPortalURL = "https://lobsterai.youdao.com/portal"
	// ClientVersion mirrors the official desktop client release. The upstream
	// exposes model runtime profiles and thinking-level control only to client
	// identities it recognizes, so the version is sent verbatim.
	ClientVersion = "2026.9.18"
	// ClientCapabilities mirrors the official client capability header.
	ClientCapabilities = "kimi-k3-agentic-v1,thinking-level-control-v1"
	// ClientUserAgent mirrors the official client User-Agent.
	ClientUserAgent = "LobsterAI/" + ClientVersion
	// DefaultKeyfrom marks traffic as originating from the official distribution.
	DefaultKeyfrom = "official"
)

const (
	// oauthCallbackPath is the loopback redirect path the portal accepts. The
	// portal validates redirect_uri as http://127.0.0.1:<port>/auth/callback and
	// refuses any other host or path.
	oauthCallbackPath = "/auth/callback"
	// maxResponseBody caps upstream auth/usage response reads.
	maxResponseBody = 1 << 20
)

// envelope is the LobsterAI response wrapper. Both "message" (client contract)
// and "msg" (older contract) are accepted.
type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Msg     string          `json:"msg"`
	Data    json.RawMessage `json:"data"`
}

func (e envelope) errorText() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Msg
}

// Error is a classified upstream failure carrying the business code.
type Error struct {
	// Status is the HTTP status code, or 0 when the failure came from a
	// non-zero envelope code on an otherwise successful response.
	Status int
	// Code is the upstream business code (e.g. 40100 for an expired session).
	Code int
	// Message is the upstream error text.
	Message string
}

func (e *Error) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("lobsterai upstream error (http %d, code %d): %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("lobsterai upstream error (http %d): %s", e.Status, e.Message)
}

// SessionExpired reports whether the failure terminates the session and the
// account has to log in again.
func (e *Error) SessionExpired() bool {
	if e == nil {
		return false
	}
	if e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden {
		return true
	}
	switch e.Code {
	case 40100, 40101:
		return true
	default:
		return false
	}
}

// TokenPayload is the credential returned by exchange and refresh.
type TokenPayload struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	UID          string
	UserID       string
	Nickname     string
}

// CreditItem is one credit bucket from the profile summary (subscription, free
// grant, campaign bonus, invitation reward, ...).
type CreditItem struct {
	Type             string `json:"type"`
	Label            string `json:"label"`
	LabelEn          string `json:"labelEn"`
	CreditsRemaining float64
	// ExpiresAt is the bucket expiry in RFC3339, or empty when it never expires.
	ExpiresAt string
}

// Usage is the account credit snapshot returned by the user endpoints.
//
// The two upstream endpoints use different accounting bases and must not be
// conflated: profile-summary reports the credit ledger (the account total plus
// its per-bucket breakdown), while the quota endpoint reports plan cycle
// counters that exclude campaign grants. Cycle counters are therefore named
// separately from the ledger total.
type Usage struct {
	// CreditsRemaining is the ledger total (free plus campaign grants) and the
	// single authoritative balance.
	CreditsRemaining float64
	// CycleCreditsLimit and CycleCreditsUsed are the plan cycle counters from
	// the quota endpoint. They use a narrower base than CreditsRemaining, so
	// they describe consumption pace rather than a spendable balance.
	CycleCreditsLimit float64
	CycleCreditsUsed  float64
	PlanName          string
	Subscription      string
	// Items breaks the ledger total down per credit bucket.
	Items []CreditItem
}

// Service performs LobsterAI auth and usage calls with a caller-supplied HTTP
// client, so proxy settings and timeouts stay under caller control.
type Service struct {
	client     *http.Client
	serverBase string
	portalURL  string
}

// NewService builds a LobsterAI auth service. A nil client falls back to a
// default client with a 30 second timeout.
func NewService(client *http.Client) *Service {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Service{
		client:     client,
		serverBase: DefaultServerBaseURL,
		portalURL:  DefaultPortalURL,
	}
}

// SetServerBaseURL overrides the API host after validation. Invalid values are
// rejected so a malformed auth file cannot redirect credential traffic.
func (s *Service) SetServerBaseURL(raw string) error {
	validated, errValidate := ValidateServerBaseURL(raw)
	if errValidate != nil {
		return errValidate
	}
	s.serverBase = validated
	return nil
}

// SetValidatedServerBaseURL stores a base URL that the caller already validated
// with ValidateServerBaseURL. Callers must not pass raw user input here.
func (s *Service) SetValidatedServerBaseURL(validated string) {
	s.serverBase = strings.TrimRight(strings.TrimSpace(validated), "/")
}

// SetPortalURL overrides the login portal after validation.
func (s *Service) SetPortalURL(raw string) error {
	validated, errValidate := ValidatePortalURL(raw)
	if errValidate != nil {
		return errValidate
	}
	s.portalURL = validated
	return nil
}

// ServerBase returns the active API host.
func (s *Service) ServerBase() string {
	if s == nil || s.serverBase == "" {
		return DefaultServerBaseURL
	}
	return s.serverBase
}

// BuildLoginURL renders the portal login URL for a loopback callback login.
// The portal requires source=electron with an http://127.0.0.1:<port>/auth/callback
// redirect target; it navigates back to that target with ?code=&state=.
func (s *Service) BuildLoginURL(redirectURI, state string) string {
	portal := DefaultPortalURL
	if s != nil && s.portalURL != "" {
		portal = s.portalURL
	}
	params := url.Values{}
	params.Set("source", "electron")
	if trimmed := strings.TrimSpace(redirectURI); trimmed != "" {
		params.Set("redirect_uri", trimmed)
	}
	if trimmed := strings.TrimSpace(state); trimmed != "" {
		params.Set("state", trimmed)
	}
	return portal + "/#/login?" + params.Encode()
}

// ExchangeCode swaps an authorization code for tokens.
func (s *Service) ExchangeCode(ctx context.Context, code, installationUUID string) (*TokenPayload, error) {
	if strings.TrimSpace(code) == "" {
		return nil, errors.New("lobsterai: authorization code is required")
	}
	body := keyfromBody()
	body["authCode"] = strings.TrimSpace(code)
	if installationUUID != "" {
		body["uuid"] = installationUUID
	}
	return s.tokenRequest(ctx, "/api/auth/exchange", body)
}

// RefreshAccessToken rotates the access token using the stored refresh token.
func (s *Service) RefreshAccessToken(ctx context.Context, refreshToken, installationUUID, userID string) (*TokenPayload, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, errors.New("lobsterai: refresh token is required")
	}
	body := keyfromBody()
	body["refreshToken"] = strings.TrimSpace(refreshToken)
	if installationUUID != "" {
		body["uuid"] = installationUUID
	}
	if userID != "" {
		body["userId"] = userID
	}
	return s.tokenRequest(ctx, "/api/auth/refresh", body)
}

// FetchUsage reads the account credit snapshot. The profile summary carries the
// total remaining credits (free plus campaign grants) while the quota endpoint
// carries the plan name and the free/monthly counters.
func (s *Service) FetchUsage(ctx context.Context, accessToken string) (*Usage, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, errors.New("lobsterai: access token is required")
	}
	usage := &Usage{}
	summaryData, errSummary := s.getData(ctx, accessToken, "/api/user/profile-summary")
	if errSummary != nil {
		return nil, errSummary
	}
	var summary struct {
		TotalCreditsRemaining float64 `json:"totalCreditsRemaining"`
		CreditItems           []struct {
			Type             string  `json:"type"`
			Label            string  `json:"label"`
			LabelEn          string  `json:"labelEn"`
			CreditsRemaining float64 `json:"creditsRemaining"`
			ExpiresAt        string  `json:"expiresAt"`
		} `json:"creditItems"`
	}
	if errUnmarshal := json.Unmarshal(summaryData, &summary); errUnmarshal != nil {
		return nil, fmt.Errorf("lobsterai: parse profile summary: %w", errUnmarshal)
	}
	usage.CreditsRemaining = summary.TotalCreditsRemaining
	for _, item := range summary.CreditItems {
		usage.Items = append(usage.Items, CreditItem{
			Type:             strings.TrimSpace(item.Type),
			Label:            strings.TrimSpace(item.Label),
			LabelEn:          strings.TrimSpace(item.LabelEn),
			CreditsRemaining: item.CreditsRemaining,
			ExpiresAt:        strings.TrimSpace(item.ExpiresAt),
		})
	}

	quotaData, errQuota := s.getData(ctx, accessToken, "/api/user/quota")
	if errQuota != nil {
		return nil, errQuota
	}
	var quota struct {
		FreeCreditsTotal    float64 `json:"freeCreditsTotal"`
		FreeCreditsUsed     float64 `json:"freeCreditsUsed"`
		CreditsLimit        float64 `json:"creditsLimit"`
		CreditsUsed         float64 `json:"creditsUsed"`
		MonthlyCreditsLimit float64 `json:"monthlyCreditsLimit"`
		MonthlyCreditsUsed  float64 `json:"monthlyCreditsUsed"`
		DailyCreditsLimit   float64 `json:"dailyCreditsLimit"`
		DailyCreditsUsed    float64 `json:"dailyCreditsUsed"`
		PlanName            string  `json:"planName"`
		SubscriptionStatus  string  `json:"subscriptionStatus"`
	}
	if errUnmarshal := json.Unmarshal(quotaData, &quota); errUnmarshal != nil {
		return nil, fmt.Errorf("lobsterai: parse quota: %w", errUnmarshal)
	}
	usage.PlanName = strings.TrimSpace(quota.PlanName)
	usage.Subscription = strings.TrimSpace(quota.SubscriptionStatus)
	// Pick the cycle counter the account is actually on, most specific first.
	switch {
	case quota.MonthlyCreditsLimit > 0:
		usage.CycleCreditsLimit = quota.MonthlyCreditsLimit
		usage.CycleCreditsUsed = quota.MonthlyCreditsUsed
	case quota.CreditsLimit > 0:
		usage.CycleCreditsLimit = quota.CreditsLimit
		usage.CycleCreditsUsed = quota.CreditsUsed
	case quota.FreeCreditsTotal > 0:
		usage.CycleCreditsLimit = quota.FreeCreditsTotal
		usage.CycleCreditsUsed = quota.FreeCreditsUsed
	case quota.DailyCreditsLimit > 0:
		usage.CycleCreditsLimit = quota.DailyCreditsLimit
		usage.CycleCreditsUsed = quota.DailyCreditsUsed
	}
	return usage, nil
}

// CatalogModel is one entry from the upstream model catalog. The upstream
// returns the official casing, which is authoritative for the registered
// model identifier.
type CatalogModel struct {
	ID        string
	Name      string
	Provider  string
	APIFormat string
}

// FetchCatalog reads the account's available model list from
// /api/models/available, the same endpoint the official client uses to render
// its model picker.
func (s *Service) FetchCatalog(ctx context.Context, accessToken string) ([]CatalogModel, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, errors.New("lobsterai: access token is required")
	}
	data, errData := s.getData(ctx, accessToken, "/api/models/available")
	if errData != nil {
		return nil, errData
	}
	var entries []struct {
		ModelID   string `json:"modelId"`
		ModelName string `json:"modelName"`
		Provider  string `json:"provider"`
		APIFormat string `json:"apiFormat"`
	}
	if errUnmarshal := json.Unmarshal(data, &entries); errUnmarshal != nil {
		return nil, fmt.Errorf("lobsterai: parse model catalog: %w", errUnmarshal)
	}
	models := make([]CatalogModel, 0, len(entries))
	for _, entry := range entries {
		id := strings.TrimSpace(entry.ModelID)
		if id == "" {
			continue
		}
		models = append(models, CatalogModel{
			ID:        id,
			Name:      strings.TrimSpace(entry.ModelName),
			Provider:  strings.TrimSpace(entry.Provider),
			APIFormat: strings.TrimSpace(entry.APIFormat),
		})
	}
	if len(models) == 0 {
		return nil, errors.New("lobsterai: model catalog is empty")
	}
	return models, nil
}

// tokenRequest posts a credential request and normalizes its data payload.
func (s *Service) tokenRequest(ctx context.Context, path string, body map[string]any) (*TokenPayload, error) {
	data, errData := s.postData(ctx, "", path, body)
	if errData != nil {
		return nil, errData
	}
	var raw struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int64  `json:"expiresIn"`
		User         struct {
			ID       string `json:"id"`
			Yid      string `json:"yid"`
			UserId   string `json:"userId"`
			Nickname string `json:"nickname"`
		} `json:"user"`
	}
	if errUnmarshal := json.Unmarshal(data, &raw); errUnmarshal != nil {
		return nil, fmt.Errorf("lobsterai: parse token response: %w", errUnmarshal)
	}
	if strings.TrimSpace(raw.AccessToken) == "" {
		return nil, errors.New("lobsterai: token response missing accessToken")
	}
	payload := &TokenPayload{
		AccessToken:  strings.TrimSpace(raw.AccessToken),
		RefreshToken: strings.TrimSpace(raw.RefreshToken),
		UID:          firstNonEmpty(raw.User.ID, raw.User.UserId, raw.User.Yid),
		UserID:       strings.TrimSpace(raw.User.UserId),
		Nickname:     strings.TrimSpace(raw.User.Nickname),
	}
	switch {
	case raw.ExpiresIn > 0:
		payload.ExpiresAt = time.Now().Add(time.Duration(raw.ExpiresIn) * time.Second)
	default:
		// The access token is a JWT; its exp claim is the authoritative fallback
		// when the response omits expiresIn.
		if exp := jwtExpiry(payload.AccessToken); exp > 0 {
			payload.ExpiresAt = time.Unix(exp, 0)
		}
	}
	return payload, nil
}

// keyfromBody builds the shared keyfrom attribution payload sent with
// credential requests. The official client reports its build keyfrom, which is
// "official" for public releases.
func keyfromBody() map[string]any {
	return map[string]any{
		"firstKeyfrom":  DefaultKeyfrom,
		"latestKeyfrom": DefaultKeyfrom,
		"version":       ClientVersion,
	}
}

// postData performs a POST and unwraps the envelope.
func (s *Service) postData(ctx context.Context, accessToken, path string, body map[string]any) (json.RawMessage, error) {
	raw, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		return nil, fmt.Errorf("lobsterai: encode request: %w", errMarshal)
	}
	req, errNew := http.NewRequestWithContext(ctx, http.MethodPost, s.ServerBase()+path, bytes.NewReader(raw))
	if errNew != nil {
		return nil, fmt.Errorf("lobsterai: build request: %w", errNew)
	}
	req.Header.Set("Content-Type", "application/json")
	applyClientHeaders(req.Header, accessToken)
	return s.doEnvelope(req)
}

// getData performs a GET with the keyfrom query and unwraps the envelope.
func (s *Service) getData(ctx context.Context, accessToken, path string) (json.RawMessage, error) {
	endpoint := s.ServerBase() + path + "?" + keyfromQuery()
	req, errNew := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if errNew != nil {
		return nil, fmt.Errorf("lobsterai: build request: %w", errNew)
	}
	req.Header.Set("Accept", "application/json")
	applyClientHeaders(req.Header, accessToken)
	return s.doEnvelope(req)
}

// doEnvelope executes a request and classifies HTTP and business failures.
func (s *Service) doEnvelope(req *http.Request) (json.RawMessage, error) {
	client := s.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("lobsterai: request failed: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			_ = errClose
		}
	}()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if errRead != nil {
		return nil, fmt.Errorf("lobsterai: read response: %w", errRead)
	}
	var env envelope
	hasEnvelope := json.Unmarshal(body, &env) == nil
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := snippet(body)
		code := 0
		if hasEnvelope {
			if text := strings.TrimSpace(env.errorText()); text != "" {
				msg = text
			}
			code = env.Code
		}
		return nil, &Error{Status: resp.StatusCode, Code: code, Message: msg}
	}
	if !hasEnvelope {
		return nil, &Error{Status: resp.StatusCode, Message: "unrecognized response body: " + snippet(body)}
	}
	if env.Code != 0 {
		return nil, &Error{Status: resp.StatusCode, Code: env.Code, Message: env.errorText()}
	}
	return env.Data, nil
}

// applyClientHeaders sets the client identity headers the upstream expects.
func applyClientHeaders(header http.Header, accessToken string) {
	if token := strings.TrimSpace(accessToken); token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	header.Set("User-Agent", ClientUserAgent)
	header.Set("X-LobsterAI-Client-Capabilities", ClientCapabilities)
	header.Set("X-LobsterAI-Client-Version", ClientVersion)
}

// ClientHeaders returns a copy of the client identity headers for executors.
func ClientHeaders() http.Header {
	header := make(http.Header)
	header.Set("User-Agent", ClientUserAgent)
	header.Set("X-LobsterAI-Client-Capabilities", ClientCapabilities)
	header.Set("X-LobsterAI-Client-Version", ClientVersion)
	return header
}

// keyfromQuery renders the keyfrom attribution as a query string.
func keyfromQuery() string {
	params := url.Values{}
	params.Set("firstKeyfrom", DefaultKeyfrom)
	params.Set("latestKeyfrom", DefaultKeyfrom)
	params.Set("version", ClientVersion)
	return params.Encode()
}

// CallbackPath returns the loopback redirect path the portal accepts.
func CallbackPath() string { return oauthCallbackPath }

// NewInstallationUUID generates the installation identifier the official client
// persists and returns with every credential request.
func NewInstallationUUID() (string, error) {
	raw := make([]byte, 16)
	if _, errRead := rand.Read(raw); errRead != nil {
		return "", fmt.Errorf("lobsterai: generate installation uuid: %w", errRead)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), nil
}

// jwtExpiry decodes the exp claim (Unix seconds) from a JWT, or returns 0.
func jwtExpiry(token string) int64 {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return 0
	}
	payload, errDecode := base64.RawURLEncoding.DecodeString(parts[1])
	if errDecode != nil {
		return 0
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp <= 0 {
		return 0
	}
	return claims.Exp
}

// FormatExpiresAt renders an expiry for the auth file. The token store reads
// metadata["expires_at"] as RFC3339 to schedule proactive refresh.
func FormatExpiresAt(expiresAt time.Time) string {
	if expiresAt.IsZero() {
		return ""
	}
	return expiresAt.UTC().Format(time.RFC3339)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func snippet(body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return "empty body"
	}
	if len(text) > 200 {
		return text[:200] + "...[truncated]"
	}
	return text
}

// ValidateServerBaseURL enforces the SSRF boundary for caller-supplied API
// hosts: http/https only, and never loopback, private, or otherwise reserved
// addresses.
func ValidateServerBaseURL(raw string) (string, error) {
	return validateBaseURL(raw, DefaultServerBaseURL)
}

// ValidatePortalURL enforces the same SSRF boundary for the login portal.
func ValidatePortalURL(raw string) (string, error) {
	return validateBaseURL(raw, DefaultPortalURL)
}

func validateBaseURL(raw, fallback string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fallback, nil
	}
	parsed, errParse := url.Parse(trimmed)
	if errParse != nil {
		return "", fmt.Errorf("lobsterai: invalid url: %w", errParse)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("lobsterai: url scheme must be http or https: %q", parsed.Scheme)
	}
	host := parsed.Hostname()
	if host == "" {
		return "", errors.New("lobsterai: url missing host")
	}
	if validateHost(host) {
		return "", fmt.Errorf("lobsterai: url host is not allowed: %q", host)
	}
	return strings.TrimRight(trimmed, "/"), nil
}

// validateHost is a package-level seam so tests can point the service at an
// httptest loopback server without weakening production validation.
var validateHost = isForbiddenHost

func isForbiddenHost(host string) bool {
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
		octets := addr.As4()
		// 100.64.0.0/10 carrier-grade NAT and 0.0.0.0/8 / broadcast are reserved.
		if octets[0] == 100 && octets[1] >= 64 && octets[1] <= 127 {
			return true
		}
		if octets[0] == 0 || octets[0] == 255 {
			return true
		}
	}
	return false
}

// ParseExpiresAt reads a persisted expires_at value in RFC3339 or unix-seconds
// form. Expiry metadata is required for proactive refresh, so unparsable values
// report ok=false and callers fall back to a refresh token based refresh.
func ParseExpiresAt(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return time.Time{}, false
		}
		if parsed, errParse := time.Parse(time.RFC3339, trimmed); errParse == nil {
			return parsed.UTC(), true
		}
		if seconds, errParse := strconv.ParseInt(trimmed, 10, 64); errParse == nil && seconds > 0 {
			return time.Unix(seconds, 0).UTC(), true
		}
	case float64:
		if typed > 0 {
			return time.Unix(int64(typed), 0).UTC(), true
		}
	case int64:
		if typed > 0 {
			return time.Unix(typed, 0).UTC(), true
		}
	}
	return time.Time{}, false
}

// RandomHex returns n random bytes as lowercase hex, used for OAuth state when
// a caller needs a local value.
func RandomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, errRead := rand.Read(buf); errRead != nil {
		return "", fmt.Errorf("lobsterai: generate random value: %w", errRead)
	}
	return hex.EncodeToString(buf), nil
}
