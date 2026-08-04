package guard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

func TestGuardBansAfterThreshold(t *testing.T) {
	g := New(3, time.Minute, time.Minute)
	defer g.Stop()

	for i := 0; i < 2; i++ {
		if banned := g.RecordFailure("1.2.3.4"); banned {
			t.Fatalf("failure %d triggered ban too early", i+1)
		}
	}
	if banned := g.RecordFailure("1.2.3.4"); !banned {
		t.Fatal("third failure did not trigger ban")
	}
	if !g.IsBanned("1.2.3.4") {
		t.Fatal("expected IP to be banned")
	}
	if g.IsBanned("5.6.7.8") {
		t.Fatal("unexpected ban for unrelated IP")
	}
}

func TestGuardSuccessResetsFailures(t *testing.T) {
	g := New(2, time.Minute, time.Minute)
	defer g.Stop()

	g.RecordFailure("1.2.3.4")
	g.RecordSuccess("1.2.3.4")
	g.RecordFailure("1.2.3.4")
	if g.IsBanned("1.2.3.4") {
		t.Fatal("success should reset the failure counter")
	}
}

func TestGuardWindowResetsFailures(t *testing.T) {
	g := New(2, time.Minute, time.Nanosecond)
	defer g.Stop()

	g.RecordFailure("1.2.3.4")
	time.Sleep(2 * time.Millisecond)
	g.RecordFailure("1.2.3.4")
	if g.IsBanned("1.2.3.4") {
		t.Fatal("failure outside window should not count towards ban")
	}
}

func TestGuardBanExpires(t *testing.T) {
	g := New(1, time.Nanosecond, time.Minute)
	defer g.Stop()

	if banned := g.RecordFailure("1.2.3.4"); !banned {
		t.Fatal("first failure should trigger ban with threshold 1")
	}
	time.Sleep(2 * time.Millisecond)
	if g.IsBanned("1.2.3.4") {
		t.Fatal("ban should expire after ban duration")
	}
}

func TestIsBlacklisted(t *testing.T) {
	SetBlacklist([]string{"1.2.3.4", "10.0.0.0/8", " "})
	defer SetBlacklist(nil)

	for _, ip := range []string{"1.2.3.4", "10.1.2.3", "10.255.255.255"} {
		if !IsBlacklisted(ip) {
			t.Fatalf("expected %s to be blacklisted", ip)
		}
	}
	for _, ip := range []string{"5.6.7.8", "10.0.0.0/9", "not-an-ip", ""} {
		if IsBlacklisted(ip) {
			t.Fatalf("expected %s to not be blacklisted", ip)
		}
	}
}

type staticProvider struct {
	result *sdkaccess.Result
	err    *sdkaccess.AuthError
}

func (p *staticProvider) Identifier() string { return "static" }

func (p *staticProvider) Authenticate(_ context.Context, _ *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	return p.result, p.err
}

func newRemoteRequest() *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.RemoteAddr = "1.2.3.4:12345"
	return req
}

func TestProviderWrapperRecordsFailuresAndBans(t *testing.T) {
	g := New(2, time.Minute, time.Minute)
	defer g.Stop()
	failing := NewProvider(&staticProvider{err: sdkaccess.NewInvalidCredentialError()}, g)

	req := newRemoteRequest()
	for i := 0; i < 2; i++ {
		if _, err := failing.Authenticate(context.Background(), req); err == nil {
			t.Fatal("expected authentication error")
		}
	}

	_, err := failing.Authenticate(context.Background(), req)
	if err == nil || !sdkaccess.IsAuthErrorCode(err, sdkaccess.AuthErrorCodeBanned) {
		t.Fatalf("expected banned error, got %v", err)
	}
}

func TestProviderWrapperIgnoresLoopbackAndCountsSuccess(t *testing.T) {
	g := New(2, time.Minute, time.Minute)
	defer g.Stop()
	okProvider := NewProvider(&staticProvider{result: &sdkaccess.Result{Provider: "static"}}, g)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	if _, err := okProvider.Authenticate(context.Background(), req); err != nil {
		t.Fatalf("loopback request failed: %v", err)
	}

	req = newRemoteRequest()
	failing := NewProvider(&staticProvider{err: sdkaccess.NewInvalidCredentialError()}, g)
	if _, err := failing.Authenticate(context.Background(), req); err == nil {
		t.Fatal("expected authentication error")
	}
	if _, err := okProvider.Authenticate(context.Background(), req); err != nil {
		t.Fatalf("success request failed: %v", err)
	}
	if g.IsBanned("1.2.3.4") {
		t.Fatal("success should clear failures instead of banning")
	}
}

func TestMiddlewareRejectsBannedAndBlacklistedIPs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := New(1, time.Minute, time.Minute)
	defer g.Stop()

	router := gin.New()
	router.Use(g.Middleware())
	router.GET("/ping", func(c *gin.Context) { c.Status(http.StatusOK) })

	do := func(remoteAddr string) int {
		req := httptest.NewRequest(http.MethodGet, "/ping", nil)
		req.RemoteAddr = remoteAddr
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		return recorder.Code
	}

	if code := do("9.9.9.9:1000"); code != http.StatusOK {
		t.Fatalf("unbanned IP status = %d, want 200", code)
	}

	SetBlacklist([]string{"8.8.8.8"})
	defer SetBlacklist(nil)
	if code := do("8.8.8.8:1000"); code != http.StatusForbidden {
		t.Fatalf("blacklisted IP status = %d, want 403", code)
	}

	g.RecordFailure("7.7.7.7")
	if code := do("7.7.7.7:1000"); code != http.StatusForbidden {
		t.Fatalf("banned IP status = %d, want 403", code)
	}

	if code := do("127.0.0.1:1000"); code != http.StatusOK {
		t.Fatalf("loopback IP status = %d, want 200", code)
	}
}

func TestWrapProviderIdempotent(t *testing.T) {
	g := New(1, time.Minute, time.Minute)
	defer g.Stop()
	inner := &staticProvider{}

	wrapped := WrapProvider(inner, g)
	doubleWrapped := WrapProvider(wrapped, g)
	if wrapped != doubleWrapped {
		t.Fatal("wrapping an already wrapped provider must be idempotent")
	}
	if Unwrap(doubleWrapped) != inner {
		t.Fatal("Unwrap should return the original provider")
	}
	if WrapProvider(nil, g) != nil {
		t.Fatal("wrapping nil must return nil")
	}
	if WrapProvider(inner, nil) != inner {
		t.Fatal("nil guard must return provider unchanged")
	}
}

func TestSnapshotReportsBannedAndFailedEntries(t *testing.T) {
	g := New(2, time.Minute, time.Minute)
	defer g.Stop()

	g.RecordFailure("1.2.3.4") // 1 failure, not banned
	g.RecordFailure("5.6.7.8")
	g.RecordFailure("5.6.7.8") // banned

	snap := g.Snapshot()
	if snap.Policy.MaxFailures != 2 || snap.Policy.BanSeconds != 60 || snap.Policy.WindowSeconds != 60 {
		t.Fatalf("unexpected policy: %+v", snap.Policy)
	}
	if len(snap.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(snap.Entries))
	}
	// Banned entry sorts first.
	banned := snap.Entries[0]
	if banned.IP != "5.6.7.8" || !banned.Banned || banned.RemainingSeconds <= 0 {
		t.Fatalf("unexpected banned entry: %+v", banned)
	}
	pending := snap.Entries[1]
	if pending.IP != "1.2.3.4" || pending.Banned || pending.FailureCount != 1 {
		t.Fatalf("unexpected pending entry: %+v", pending)
	}
}

func TestEscalationFiresAfterThresholdBans(t *testing.T) {
	g := New(1, time.Nanosecond, time.Minute)
	defer g.Stop()

	var escalated []string
	g.SetEscalation(2, func(ip string) error {
		escalated = append(escalated, ip)
		return nil
	})

	// Two bans cross the threshold of 2.
	g.RecordFailure("1.2.3.4")
	time.Sleep(2 * time.Millisecond) // let first ban expire
	g.RecordFailure("1.2.3.4")
	if len(escalated) != 1 || escalated[0] != "1.2.3.4" {
		t.Fatalf("expected one escalation for 1.2.3.4, got %v", escalated)
	}
}

func TestRequestClientIP(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		want       string
	}{
		{"remote", "1.2.3.4:1234", "1.2.3.4"},
		{"loopback", "127.0.0.1:1234", ""},
		{"loopback6", "[::1]:1234", ""},
		{"remote6", "[2001:db8::1]:1234", "2001:db8::1"},
		{"no-port", "1.2.3.4", "1.2.3.4"},
		{"invalid", "not-an-ip:1234", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.remoteAddr
			if got := RequestClientIP(req); got != tc.want {
				t.Fatalf("RequestClientIP(%q) = %q, want %q", tc.remoteAddr, got, tc.want)
			}
		})
	}
}
