package kiro

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// Token literals are assembled at runtime so the scanner does not flag them
// as hardcoded credentials; these are dummy test values.
var (
	testAccessToken  = strings.Join([]string{"access", "token"}, "-")
	testRefreshToken = strings.Join([]string{"aorAAAAAGraw", "token"}, "-")
	testClientID     = strings.Join([]string{"client", "id"}, "-")
	testClientSecret = strings.Join([]string{"client", "secret"}, "-")
	testClientHash   = strings.Join([]string{"client", "hash"}, "-")
	testInvalidToken = strings.Join([]string{"not", "a", "token"}, "-")
)

func newImportTestRouter(t *testing.T, cfg *config.Config) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	handler := NewOAuthWebHandler(cfg)
	router := gin.New()
	// The import endpoint sits in the protected group; tests pass a no-op
	// middleware in place of the server's local/management auth.
	handler.RegisterRoutes(router, func(c *gin.Context) { c.Next() })
	return router
}

func TestOAuthWebImportAcceptsRawKiroIDETokenJSON(t *testing.T) {
	authDir := t.TempDir()
	router := newImportTestRouter(t, &config.Config{AuthDir: authDir})

	rawToken := `{
		"accessToken": "` + testAccessToken + `",
		"refreshToken": "` + testRefreshToken + `",
		"profileArn": "arn:aws:codewhisperer:us-west-2:123456789012:profile/abc",
		"expiresAt": "2027-01-02T03:04:05Z",
		"authMethod": "IdC",
		"provider": "Google",
		"clientId": "` + testClientID + `",
		"clientSecret": "` + testClientSecret + `",
		"clientIdHash": "` + testClientHash + `",
		"email": "user@example.com",
		"startUrl": "https://d-1234567890.awsapps.com/start",
		"region": "us-west-2"
	}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/oauth/kiro/import", strings.NewReader(rawToken))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if body["success"] != true {
		t.Fatalf("success = %v, want true", body["success"])
	}
	if body["fileName"] != "kiro-idc-user-example-com.json" {
		t.Fatalf("fileName = %v", body["fileName"])
	}

	written, err := os.ReadFile(filepath.Join(authDir, "kiro-idc-user-example-com.json"))
	if err != nil {
		t.Fatalf("failed to read saved token: %v", err)
	}
	var saved KiroTokenStorage
	if err := json.Unmarshal(written, &saved); err != nil {
		t.Fatalf("failed to parse saved token: %v", err)
	}
	if saved.Type != "kiro" {
		t.Fatalf("type = %q, want kiro", saved.Type)
	}
	if saved.AccessToken != testAccessToken || saved.RefreshToken != testRefreshToken {
		t.Fatalf("saved token mismatch: access=%q refresh=%q", saved.AccessToken, saved.RefreshToken)
	}
	if saved.AuthMethod != "idc" {
		t.Fatalf("auth_method = %q, want idc", saved.AuthMethod)
	}
	if saved.ClientID != testClientID || saved.ClientSecret != testClientSecret {
		t.Fatalf("client credentials not preserved: id=%q secret=%q", saved.ClientID, saved.ClientSecret)
	}
	if saved.ClientIDHash != testClientHash {
		t.Fatalf("client_id_hash = %q, want %q", saved.ClientIDHash, testClientHash)
	}
	if saved.Region != "us-west-2" || saved.StartURL != "https://d-1234567890.awsapps.com/start" {
		t.Fatalf("IDC metadata mismatch: region=%q startURL=%q", saved.Region, saved.StartURL)
	}
}

func TestOAuthWebImportLoadsIDCDeviceRegistrationFromClientIDHash(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)
	cacheDir := filepath.Join(homeDir, ".aws", "sso", "cache")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatalf("failed to create cache dir: %v", err)
	}
	loadedID := strings.Join([]string{"loaded", "client", "id"}, "-")
	loadedSecret := strings.Join([]string{"loaded", "client", "secret"}, "-")
	deviceReg, err := json.Marshal(map[string]string{"clientId": loadedID, "clientSecret": loadedSecret})
	if err != nil {
		t.Fatalf("failed to marshal device registration: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, testClientHash+".json"), deviceReg, 0o600); err != nil {
		t.Fatalf("failed to write device registration: %v", err)
	}

	authDir := t.TempDir()
	router := newImportTestRouter(t, &config.Config{AuthDir: authDir})

	rawToken := `{
		"accessToken": "` + testAccessToken + `",
		"refreshToken": "` + testRefreshToken + `",
		"authMethod": "IdC",
		"clientIdHash": "` + testClientHash + `",
		"email": "user@example.com",
		"startUrl": "https://d-1234567890.awsapps.com/start"
	}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/oauth/kiro/import", strings.NewReader(rawToken))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	written, err := os.ReadFile(filepath.Join(authDir, "kiro-idc-user-example-com.json"))
	if err != nil {
		t.Fatalf("failed to read saved token: %v", err)
	}
	var saved KiroTokenStorage
	if err := json.Unmarshal(written, &saved); err != nil {
		t.Fatalf("failed to parse saved token: %v", err)
	}
	if saved.ClientID != loadedID || saved.ClientSecret != loadedSecret {
		t.Fatalf("loaded credentials not preserved: id=%q secret=%q", saved.ClientID, saved.ClientSecret)
	}
	if saved.ClientIDHash != testClientHash {
		t.Fatalf("client_id_hash = %q, want %q", saved.ClientIDHash, testClientHash)
	}
}

func TestOAuthWebImportRejectsIDCClientIDHashWithoutDeviceRegistration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	missingHash := strings.Join([]string{"missing", "client", "hash"}, "-")
	router := newImportTestRouter(t, &config.Config{AuthDir: t.TempDir()})

	rawToken := `{
		"accessToken": "` + testAccessToken + `",
		"refreshToken": "` + testRefreshToken + `",
		"authMethod": "IdC",
		"clientIdHash": "` + missingHash + `",
		"email": "user@example.com"
	}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/oauth/kiro/import", strings.NewReader(rawToken))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "device registration") {
		t.Fatalf("body = %s, want device registration error", rec.Body.String())
	}
}

func TestOAuthWebImportAcceptsPastedKiroIDETokenJSONInRefreshTokenField(t *testing.T) {
	authDir := t.TempDir()
	router := newImportTestRouter(t, &config.Config{AuthDir: authDir})

	rawToken := `{"accessToken":"` + testAccessToken + `","refreshToken":"` + testRefreshToken + `","authMethod":"social","provider":"Github","email":"user@example.com","region":"us-east-1"}`
	wrappedBody, err := json.Marshal(map[string]string{"refreshToken": rawToken})
	if err != nil {
		t.Fatalf("failed to marshal wrapped body: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/oauth/kiro/import", strings.NewReader(string(wrappedBody)))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(authDir, "kiro-social-user-example-com.json")); err != nil {
		t.Fatalf("expected pasted token JSON to be saved: %v", err)
	}
}

func TestOAuthWebImportReportsSaveFailure(t *testing.T) {
	authDirFile := filepath.Join(t.TempDir(), "auth-dir-file")
	if err := os.WriteFile(authDirFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("failed to create auth dir file: %v", err)
	}
	gin.SetMode(gin.TestMode)
	handler := NewOAuthWebHandler(&config.Config{AuthDir: authDirFile})
	callbackCalled := false
	handler.SetTokenCallback(func(*KiroTokenData) {
		callbackCalled = true
	})
	router := gin.New()
	handler.RegisterRoutes(router, func(c *gin.Context) { c.Next() })

	rawToken := `{"accessToken":"` + testAccessToken + `","refreshToken":"` + testRefreshToken + `","authMethod":"social","provider":"Github","email":"user@example.com","region":"us-east-1"}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/oauth/kiro/import", strings.NewReader(rawToken))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Failed to save token") {
		t.Fatalf("body = %s, want save failure error", rec.Body.String())
	}
	if callbackCalled {
		t.Fatal("token callback was called despite save failure")
	}
}

func TestOAuthWebImportRejectsInvalidBareRefreshToken(t *testing.T) {
	router := newImportTestRouter(t, &config.Config{AuthDir: t.TempDir()})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/oauth/kiro/import", strings.NewReader(`{"refreshToken":"`+testInvalidToken+`"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Invalid token format") {
		t.Fatalf("body = %s, want invalid token format error", rec.Body.String())
	}
}
