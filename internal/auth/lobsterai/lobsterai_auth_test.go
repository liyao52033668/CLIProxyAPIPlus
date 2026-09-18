package lobsterai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fixtureToken builds a non-secret test token at runtime so no credential-like
// literal is committed.
func fixtureToken(parts ...string) string {
	return strings.Join(parts, "-")
}

// allowLoopbackForTest weakens host validation for httptest servers.
func allowLoopbackForTest(t *testing.T) {
	t.Helper()
	original := validateHost
	validateHost = func(string) bool { return false }
	t.Cleanup(func() { validateHost = original })
}

func TestValidateBaseURLRejectsUnsafeHosts(t *testing.T) {
	original := validateHost
	validateHost = isForbiddenHost
	t.Cleanup(func() { validateHost = original })

	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "public https", input: "https://lobsterai-server.youdao.com", wantErr: false},
		{name: "public http", input: "http://example.com", wantErr: false},
		{name: "empty falls back", input: "   ", wantErr: false},
		{name: "loopback ip", input: "http://127.0.0.1:8080", wantErr: true},
		{name: "localhost name", input: "https://localhost:9000", wantErr: true},
		{name: "private range", input: "http://10.0.0.5", wantErr: true},
		{name: "link local", input: "http://169.254.169.254", wantErr: true},
		{name: "ipv6 loopback", input: "http://[::1]:8080", wantErr: true},
		{name: "unsupported scheme", input: "file:///etc/passwd", wantErr: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, errValidate := validateBaseURL(testCase.input, "https://fallback.example.com")
			if testCase.wantErr {
				if errValidate == nil {
					t.Fatalf("validateBaseURL(%q) = %q, want error", testCase.input, got)
				}
				return
			}
			if errValidate != nil {
				t.Fatalf("validateBaseURL(%q) unexpected error: %v", testCase.input, errValidate)
			}
			if got == "" {
				t.Fatalf("validateBaseURL(%q) returned empty value", testCase.input)
			}
		})
	}
}

func TestBuildLoginURLUsesPortalHashRoute(t *testing.T) {
	service := NewService(nil)
	loginURL := service.BuildLoginURL("http://127.0.0.1:8317/auth/callback", "state-123")

	if !strings.HasPrefix(loginURL, DefaultPortalURL+"/#/login?") {
		t.Fatalf("login url = %q, want portal hash login route", loginURL)
	}
	if !strings.Contains(loginURL, "source=electron") {
		t.Fatalf("login url = %q, want source=electron", loginURL)
	}
	if !strings.Contains(loginURL, "redirect_uri=") || !strings.Contains(loginURL, "%2Fauth%2Fcallback") {
		t.Fatalf("login url = %q, want encoded redirect_uri", loginURL)
	}
	if !strings.Contains(loginURL, "state=state-123") {
		t.Fatalf("login url = %q, want state parameter", loginURL)
	}
}

func TestExchangeCodeParsesTokenPayload(t *testing.T) {
	allowLoopbackForTest(t)

	expiry := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	accessToken := signJWT(t, expiry)
	rotatedRefresh := fixtureToken("fixture", "refresh")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/exchange" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("X-LobsterAI-Client-Version"); got != ClientVersion {
			t.Errorf("client version header = %q, want %q", got, ClientVersion)
		}
		var body map[string]any
		if errDecode := json.NewDecoder(r.Body).Decode(&body); errDecode != nil {
			t.Errorf("decode request: %v", errDecode)
		}
		if body["authCode"] != "code-1" {
			t.Errorf("authCode = %#v, want code-1", body["authCode"])
		}
		if body["firstKeyfrom"] != DefaultKeyfrom {
			t.Errorf("firstKeyfrom = %#v, want %q", body["firstKeyfrom"], DefaultKeyfrom)
		}
		writeEnvelope(t, w, map[string]any{
			"accessToken":  accessToken,
			"refreshToken": rotatedRefresh,
			"expiresIn":    3600,
			"user":         map[string]any{"id": "uid-9", "userId": "yid-9", "nickname": "Lobster"},
		})
	}))
	defer server.Close()

	service := NewService(server.Client())
	if errSet := service.SetServerBaseURL(server.URL); errSet != nil {
		t.Fatalf("SetServerBaseURL: %v", errSet)
	}
	payload, errExchange := service.ExchangeCode(context.Background(), "code-1", "uuid-1")
	if errExchange != nil {
		t.Fatalf("ExchangeCode: %v", errExchange)
	}
	if payload.AccessToken != accessToken {
		t.Fatalf("AccessToken = %q, want issued token", payload.AccessToken)
	}
	if payload.RefreshToken != rotatedRefresh {
		t.Fatalf("RefreshToken = %q, want issued refresh token", payload.RefreshToken)
	}
	if payload.UID != "uid-9" || payload.UserID != "yid-9" || payload.Nickname != "Lobster" {
		t.Fatalf("unexpected account %#v", payload)
	}
	// expiresIn takes precedence over the JWT exp claim.
	if delta := time.Until(payload.ExpiresAt); delta < 59*time.Minute || delta > 61*time.Minute {
		t.Fatalf("ExpiresAt = %v, want ~1h from now", payload.ExpiresAt)
	}
}

func TestExchangeCodeFallsBackToJWTExpiry(t *testing.T) {
	allowLoopbackForTest(t)

	expiry := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	accessToken := signJWT(t, expiry)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeEnvelope(t, w, map[string]any{"accessToken": accessToken, "refreshToken": fixtureToken("fixture", "refresh")})
	}))
	defer server.Close()

	service := NewService(server.Client())
	if errSet := service.SetServerBaseURL(server.URL); errSet != nil {
		t.Fatalf("SetServerBaseURL: %v", errSet)
	}
	payload, errExchange := service.ExchangeCode(context.Background(), "code-1", "")
	if errExchange != nil {
		t.Fatalf("ExchangeCode: %v", errExchange)
	}
	if !payload.ExpiresAt.Equal(expiry) {
		t.Fatalf("ExpiresAt = %v, want %v from JWT exp", payload.ExpiresAt, expiry)
	}
}

func TestExchangeCodeClassifiesSessionExpiry(t *testing.T) {
	allowLoopbackForTest(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":40100,"message":"登录已过期，请重新登录","data":null}`))
	}))
	defer server.Close()

	service := NewService(server.Client())
	if errSet := service.SetServerBaseURL(server.URL); errSet != nil {
		t.Fatalf("SetServerBaseURL: %v", errSet)
	}
	_, errExchange := service.ExchangeCode(context.Background(), "expired", "")
	if errExchange == nil {
		t.Fatal("ExchangeCode returned nil error for 401")
	}
	upstreamErr, ok := errExchange.(*Error)
	if !ok {
		t.Fatalf("error type = %T, want *Error", errExchange)
	}
	if !upstreamErr.SessionExpired() {
		t.Fatalf("SessionExpired() = false for %+v", upstreamErr)
	}
	if upstreamErr.Code != 40100 {
		t.Fatalf("Code = %d, want 40100", upstreamErr.Code)
	}
}

func TestRefreshAccessTokenRotatesTokens(t *testing.T) {
	allowLoopbackForTest(t)

	storedRefresh := fixtureToken("old", "refresh")
	rotatedRefresh := fixtureToken("new", "refresh")
	rotatedAccess := fixtureToken("new", "access")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/refresh" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		var body map[string]any
		if errDecode := json.NewDecoder(r.Body).Decode(&body); errDecode != nil {
			t.Errorf("decode request: %v", errDecode)
		}
		if body["refreshToken"] != storedRefresh {
			t.Errorf("refreshToken = %#v, want the stored token", body["refreshToken"])
		}
		if body["userId"] != "yid-9" {
			t.Errorf("userId = %#v, want yid-9", body["userId"])
		}
		if body["uuid"] != "uuid-1" {
			t.Errorf("uuid = %#v, want uuid-1", body["uuid"])
		}
		writeEnvelope(t, w, map[string]any{
			"accessToken":  rotatedAccess,
			"refreshToken": rotatedRefresh,
			"expiresIn":    1800,
		})
	}))
	defer server.Close()

	service := NewService(server.Client())
	if errSet := service.SetServerBaseURL(server.URL); errSet != nil {
		t.Fatalf("SetServerBaseURL: %v", errSet)
	}
	payload, errRefresh := service.RefreshAccessToken(context.Background(), storedRefresh, "uuid-1", "yid-9")
	if errRefresh != nil {
		t.Fatalf("RefreshAccessToken: %v", errRefresh)
	}
	if payload.AccessToken != rotatedAccess || payload.RefreshToken != rotatedRefresh {
		t.Fatalf("unexpected tokens %#v", payload)
	}
	if payload.ExpiresAt.IsZero() {
		t.Fatal("ExpiresAt is zero, want expiresIn derived expiry")
	}
}

func TestFetchUsageReadsSummaryAndQuota(t *testing.T) {
	allowLoopbackForTest(t)

	accessToken := fixtureToken("fixture", "access")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+accessToken {
			t.Errorf("Authorization = %q, want the fixture bearer token", got)
		}
		switch r.URL.Path {
		case "/api/user/profile-summary":
			writeEnvelope(t, w, map[string]any{"totalCreditsRemaining": 5297.72})
		case "/api/user/quota":
			writeEnvelope(t, w, map[string]any{
				"freeCreditsTotal":    300,
				"freeCreditsUsed":     12,
				"monthlyCreditsLimit": 5000,
				"monthlyCreditsUsed":  200,
				"planName":            "Standard",
				"subscriptionStatus":  "active",
			})
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	service := NewService(server.Client())
	if errSet := service.SetServerBaseURL(server.URL); errSet != nil {
		t.Fatalf("SetServerBaseURL: %v", errSet)
	}
	usage, errUsage := service.FetchUsage(context.Background(), accessToken)
	if errUsage != nil {
		t.Fatalf("FetchUsage: %v", errUsage)
	}
	if usage.CreditsRemaining != 5297.72 {
		t.Fatalf("CreditsRemaining = %v, want 5297.72", usage.CreditsRemaining)
	}
	// The monthly counters are cycle counters: they live under their own names
	// because they exclude campaign grants and are not a limit for the ledger.
	if usage.CycleCreditsLimit != 5000 || usage.CycleCreditsUsed != 200 {
		t.Fatalf("cycle limit/used = %v/%v, want 5000/200", usage.CycleCreditsLimit, usage.CycleCreditsUsed)
	}
	if usage.PlanName != "Standard" || usage.Subscription != "active" {
		t.Fatalf("plan/subscription = %q/%q, want Standard/active", usage.PlanName, usage.Subscription)
	}
}

func TestServiceFetchCatalog(t *testing.T) {
	allowLoopbackForTest(t)

	accessToken := fixtureToken("fixture", "access")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+accessToken {
			t.Errorf("Authorization = %q, want the fixture bearer token", got)
		}
		if r.URL.Path != "/api/models/available" {
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("firstKeyfrom") != DefaultKeyfrom {
			t.Errorf("firstKeyfrom = %q, want the official keyfrom", r.URL.Query().Get("firstKeyfrom"))
		}
		writeEnvelope(t, w, []map[string]any{
			{"modelId": "MiniMax-M3", "modelName": "MiniMax M3", "provider": "minimax", "apiFormat": "openai"},
			{"modelId": "brand-new-model", "modelName": "Brand New"},
			{"modelId": "   "},
		})
	}))
	defer server.Close()

	service := NewService(server.Client())
	if errSet := service.SetServerBaseURL(server.URL); errSet != nil {
		t.Fatalf("SetServerBaseURL: %v", errSet)
	}
	catalog, errCatalog := service.FetchCatalog(context.Background(), accessToken)
	if errCatalog != nil {
		t.Fatalf("FetchCatalog: %v", errCatalog)
	}
	if len(catalog) != 2 {
		t.Fatalf("catalog length = %d, want 2 (blank ids dropped)", len(catalog))
	}
	if catalog[0].ID != "MiniMax-M3" || catalog[0].Name != "MiniMax M3" || catalog[0].APIFormat != "openai" {
		t.Fatalf("entry 0 = %#v, want the upstream casing, name, and format", catalog[0])
	}
	if catalog[1].ID != "brand-new-model" || catalog[1].Name != "Brand New" {
		t.Fatalf("entry 1 = %#v, want the new model", catalog[1])
	}
}

func TestServiceFetchCatalogRejectsEmptyCatalog(t *testing.T) {
	allowLoopbackForTest(t)

	accessToken := fixtureToken("fixture", "access")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(t, w, []map[string]any{})
	}))
	defer server.Close()

	service := NewService(server.Client())
	if errSet := service.SetServerBaseURL(server.URL); errSet != nil {
		t.Fatalf("SetServerBaseURL: %v", errSet)
	}
	if _, errCatalog := service.FetchCatalog(context.Background(), accessToken); errCatalog == nil {
		t.Fatal("FetchCatalog accepted an empty catalog")
	}
}

func TestSetServerBaseURLRejectsUnsafeValues(t *testing.T) {
	service := NewService(nil)
	if errSet := service.SetServerBaseURL("http://127.0.0.1:1234"); errSet == nil {
		t.Fatal("SetServerBaseURL accepted a loopback host")
	}
	if service.ServerBase() != DefaultServerBaseURL {
		t.Fatalf("ServerBase() = %q, want the default after a rejected override", service.ServerBase())
	}
}

func TestNewInstallationUUIDIsWellFormed(t *testing.T) {
	first, errFirst := NewInstallationUUID()
	if errFirst != nil {
		t.Fatalf("NewInstallationUUID: %v", errFirst)
	}
	second, errSecond := NewInstallationUUID()
	if errSecond != nil {
		t.Fatalf("NewInstallationUUID: %v", errSecond)
	}
	if first == second {
		t.Fatal("two installation uuids are identical")
	}
	if len(first) != 36 || strings.Count(first, "-") != 4 {
		t.Fatalf("uuid = %q, want canonical 8-4-4-4-12 form", first)
	}
	if first[14] != '4' {
		t.Fatalf("uuid = %q, want version 4 marker", first)
	}
}

func TestParseExpiresAtAcceptsKnownForms(t *testing.T) {
	rfc := "2027-01-02T03:04:05Z"
	if got, ok := ParseExpiresAt(rfc); !ok || got.Format(time.RFC3339) != rfc {
		t.Fatalf("ParseExpiresAt(%q) = %v, %v", rfc, got, ok)
	}
	if got, ok := ParseExpiresAt("1893456000"); !ok || got.Unix() != 1893456000 {
		t.Fatalf("ParseExpiresAt(unix string) = %v, %v", got, ok)
	}
	if got, ok := ParseExpiresAt(float64(1893456000)); !ok || got.Unix() != 1893456000 {
		t.Fatalf("ParseExpiresAt(float) = %v, %v", got, ok)
	}
	for _, invalid := range []any{"", "not-a-time", float64(0), nil, true} {
		if _, ok := ParseExpiresAt(invalid); ok {
			t.Fatalf("ParseExpiresAt(%#v) reported ok for an invalid value", invalid)
		}
	}
}

func writeEnvelope(t *testing.T, w http.ResponseWriter, data any) {
	t.Helper()
	raw, errMarshal := json.Marshal(map[string]any{"code": 0, "message": "", "data": data})
	if errMarshal != nil {
		t.Fatalf("marshal envelope: %v", errMarshal)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

// signJWT builds an unsigned token with the given exp claim; the service only
// decodes the payload and never verifies the signature.
func signJWT(t *testing.T, expiry time.Time) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS512","typ":"JWT"}`))
	payloadJSON, errMarshal := json.Marshal(map[string]any{"exp": expiry.Unix()})
	if errMarshal != nil {
		t.Fatalf("marshal claims: %v", errMarshal)
	}
	payload := base64.RawURLEncoding.EncodeToString(payloadJSON)
	return header + "." + payload + ".signature"
}
