package freebuff

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestLoginCodePostsFingerprintAndParsesResponse(t *testing.T) {
	defer allowLoopbackForTest(t)()
	var gotPath, gotUA, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUA = r.Header.Get("User-Agent")
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		_, _ = io.WriteString(w, `{"loginUrl":"https://www.codebuff.com/cli/authorize?code=abc","fingerprintHash":"hash-1","expiresAt":123456}`)
	}))
	defer server.Close()

	code, err := RequestLoginCode(context.Background(), server.Client(), server.URL, "fb-test")
	if err != nil {
		t.Fatalf("RequestLoginCode() error = %v", err)
	}
	if gotPath != "/api/auth/cli/code" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotUA != loginUserAgent {
		t.Fatalf("User-Agent = %q, want %q", gotUA, loginUserAgent)
	}
	if gotBody != `{"fingerprintId":"fb-test"}` {
		t.Fatalf("body = %q", gotBody)
	}
	if code.LoginURL == "" || code.FingerprintHash != "hash-1" || code.ExpiresAt != 123456 {
		t.Fatalf("code = %#v", code)
	}
	if code.FingerprintID != "fb-test" {
		t.Fatalf("FingerprintID = %q", code.FingerprintID)
	}
}

func TestPollLoginStatusPendingAndAuthorized(t *testing.T) {
	defer allowLoopbackForTest(t)()
	var gotQuery string
	status := http.StatusUnauthorized
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = io.WriteString(w, `{"user":{"email":"u@example.com","name":"U","authToken":"tok-1"}}`)
		}
	}))
	defer server.Close()
	client := server.Client()
	code := &LoginCode{FingerprintID: "fb-test", FingerprintHash: "hash-1", ExpiresAt: 42}

	if _, pending, err := PollLoginStatus(context.Background(), client, server.URL, code); err != nil || !pending {
		t.Fatalf("pending poll = (%v, %v)", pending, err)
	}
	for key, want := range map[string]string{"fingerprintId": "fb-test", "fingerprintHash": "hash-1", "expiresAt": "42"} {
		if !strings.Contains(gotQuery, key+"="+want) {
			t.Fatalf("query %q missing %s=%s", gotQuery, key, want)
		}
	}

	status = http.StatusOK
	user, pending, err := PollLoginStatus(context.Background(), client, server.URL, code)
	if err != nil || pending {
		t.Fatalf("authorized poll = (pending %v, err %v)", pending, err)
	}
	if user == nil || user.AuthToken != "tok-1" || user.Email != "u@example.com" {
		t.Fatalf("user = %#v", user)
	}
}

func TestVerifyTokenRejectsUnauthorized(t *testing.T) {
	defer allowLoopbackForTest(t)()
	status := http.StatusForbidden
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"error":"account_suspended"}`)
	}))
	defer server.Close()

	if err := VerifyToken(context.Background(), server.Client(), server.URL, "tok"); err == nil {
		t.Fatal("VerifyToken() = nil, want rejection error")
	}
	status = http.StatusOK
	if err := VerifyToken(context.Background(), server.Client(), server.URL, "tok"); err != nil {
		t.Fatalf("VerifyToken() error = %v", err)
	}
}

func TestValidateLoginBaseURL(t *testing.T) {
	if got, err := ValidateLoginBaseURL(""); err != nil || got != DefaultLoginBaseURL {
		t.Fatalf("empty = (%q, %v)", got, err)
	}
	if _, err := ValidateLoginBaseURL("https://www.codebuff.com/"); err != nil {
		t.Fatalf("public host rejected: %v", err)
	}
	for _, blocked := range []string{
		"http://127.0.0.1:9000",
		"https://localhost/x",
		"https://10.1.2.3",
		"https://192.168.1.1",
		"https://169.254.1.1",
		"https://[::1]/",
		"ftp://www.codebuff.com",
		"https://100.64.0.1",
	} {
		if _, err := ValidateLoginBaseURL(blocked); err == nil {
			t.Fatalf("ValidateLoginBaseURL(%q) = nil error, want rejection", blocked)
		}
	}
}

// allowLoopbackForTest points the host validator at a permissive stub so the
// httptest loopback server can receive login requests.
func allowLoopbackForTest(t *testing.T) func() {
	t.Helper()
	original := validateLoginHost
	validateLoginHost = func(string) bool { return false }
	return func() { validateLoginHost = original }
}
