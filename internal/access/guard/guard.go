// Package guard provides a process-wide brute-force guard for API authentication.
// It tracks failed authentication attempts per client IP and temporarily bans
// clients that exceed the configured failure threshold.
package guard

import (
	"context"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	log "github.com/sirupsen/logrus"
)

const (
	defaultMaxFailures    = 10
	defaultBanDuration    = 30 * time.Minute
	defaultWindowDuration = 5 * time.Minute
	cleanupInterval       = 5 * time.Minute
)

type failureInfo struct {
	count     int
	banCount  int
	last      time.Time
	banUntil  time.Time
	escalated bool
}

// Guard tracks authentication failures per client IP and enforces temporary bans.
// The zero value is not usable; construct instances with New.
type Guard struct {
	mu                  sync.Mutex
	failures            map[string]*failureInfo
	maxFailures         int
	banDuration         time.Duration
	window              time.Duration
	escalationThreshold int
	escalationCallback  func(clientIP string) error
	stop                chan struct{}
	stopped             bool
}

// Entry describes the guard state of a single client IP for reporting.
type Entry struct {
	IP               string    `json:"ip"`
	Banned           bool      `json:"banned"`
	BanExpiresAt     time.Time `json:"ban_expires_at,omitempty"`
	RemainingSeconds int64     `json:"remaining_seconds,omitempty"`
	FailureCount     int       `json:"failure_count"`
	BanCount         int       `json:"ban_count"`
	Escalated        bool      `json:"escalated"`
	LastFailure      time.Time `json:"last_failure,omitempty"`
}

var (
	globalMu     sync.Mutex
	globalGuard  *Guard
	globalPolicy = policy{maxFailures: defaultMaxFailures, ban: defaultBanDuration, window: defaultWindowDuration}
)

type policy struct {
	maxFailures int
	ban         time.Duration
	window      time.Duration
	escalation  int
}

// New creates a guard with the given thresholds. Non-positive values fall back
// to the built-in defaults. Call Stop to release the background cleanup goroutine.
func New(maxFailures int, ban, window time.Duration) *Guard {
	g := &Guard{
		failures:    make(map[string]*failureInfo),
		maxFailures: intOrDefault(maxFailures, defaultMaxFailures),
		banDuration: durationOrDefault(ban, defaultBanDuration),
		window:      durationOrDefault(window, defaultWindowDuration),
		stop:        make(chan struct{}),
	}
	go g.cleanupLoop()
	return g
}

// Stop terminates the background cleanup goroutine. Further calls are no-ops.
func (g *Guard) Stop() {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.stopped {
		g.mu.Unlock()
		return
	}
	g.stopped = true
	close(g.stop)
	g.mu.Unlock()
}

func (g *Guard) cleanupLoop() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			g.cleanup()
		case <-g.stop:
			return
		}
	}
}

func (g *Guard) cleanup() {
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	for ip, info := range g.failures {
		if !info.banUntil.IsZero() && now.Before(info.banUntil) {
			continue
		}
		if info.banUntil.IsZero() && now.Sub(info.last) < g.window {
			continue
		}
		delete(g.failures, ip)
	}
}

// IsBanned reports whether the client IP is currently banned.
func (g *Guard) IsBanned(clientIP string) bool {
	if g == nil || clientIP == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	info := g.failures[clientIP]
	if info == nil || info.banUntil.IsZero() {
		return false
	}
	if time.Now().Before(info.banUntil) {
		return true
	}
	// Ban expired: reset the entry.
	info.banUntil = time.Time{}
	info.count = 0
	return false
}

// RecordFailure increments the failure counter for the client IP and bans it
// once the counter reaches the configured threshold. It returns true when the
// call triggered (or extended) a ban.
func (g *Guard) RecordFailure(clientIP string) bool {
	if g == nil || clientIP == "" {
		return false
	}
	now := time.Now()
	g.mu.Lock()
	info := g.failures[clientIP]
	if info == nil {
		info = &failureInfo{}
		g.failures[clientIP] = info
	}
	if !info.banUntil.IsZero() && now.Before(info.banUntil) {
		g.mu.Unlock()
		return false
	}
	if !info.banUntil.IsZero() || now.Sub(info.last) >= g.window {
		info.count = 0
		info.banUntil = time.Time{}
	}
	info.count++
	info.last = now
	if info.count < g.maxFailures {
		g.mu.Unlock()
		return false
	}
	info.banUntil = now.Add(g.banDuration)
	info.count = 0
	info.banCount++
	banCount := info.banCount
	threshold := g.escalationThreshold
	callback := g.escalationCallback
	shouldEscalate := threshold > 0 && callback != nil && banCount >= threshold && !info.escalated
	if shouldEscalate {
		info.escalated = true
	}
	g.mu.Unlock()
	log.Warnf("auth guard: banned IP %s for %s after %d failed authentication attempts (ban #%d)", clientIP, g.banDuration, g.maxFailures, banCount)
	if shouldEscalate {
		if err := callback(clientIP); err != nil {
			log.Errorf("auth guard: failed to escalate IP %s to the persistent blacklist: %v", clientIP, err)
			g.mu.Lock()
			if info := g.failures[clientIP]; info != nil {
				info.escalated = false
			}
			g.mu.Unlock()
		} else {
			log.Warnf("auth guard: escalated IP %s to the persistent blacklist after %d bans", clientIP, banCount)
		}
	}
	return true
}

// RecordSuccess clears any recorded failures for the client IP.
func (g *Guard) RecordSuccess(clientIP string) {
	if g == nil || clientIP == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if info := g.failures[clientIP]; info != nil {
		info.count = 0
		info.last = time.Time{}
	}
}

// Snapshot describes the full guard state for reporting endpoints.
type Snapshot struct {
	Entries []Entry `json:"entries"`
	Policy  Policy  `json:"policy"`
}

// Policy describes the active guard thresholds for reporting endpoints.
type Policy struct {
	MaxFailures         int `json:"max_failures"`
	BanSeconds          int `json:"ban_seconds"`
	WindowSeconds       int `json:"window_seconds"`
	EscalationThreshold int `json:"escalation_threshold"`
}

// Snapshot returns the current per-IP guard state and the active policy.
// Entries with no active ban and no recorded failures are omitted.
func (g *Guard) Snapshot() Snapshot {
	snap := Snapshot{Entries: []Entry{}}
	if g == nil {
		return snap
	}
	now := time.Now()
	g.mu.Lock()
	snap.Policy = Policy{
		MaxFailures:         g.maxFailures,
		BanSeconds:          int(g.banDuration / time.Second),
		WindowSeconds:       int(g.window / time.Second),
		EscalationThreshold: g.escalationThreshold,
	}
	for ip, info := range g.failures {
		banned := !info.banUntil.IsZero() && now.Before(info.banUntil)
		if !banned && info.count == 0 {
			continue
		}
		entry := Entry{
			IP:           ip,
			Banned:       banned,
			FailureCount: info.count,
			BanCount:     info.banCount,
			Escalated:    info.escalated,
			LastFailure:  info.last,
		}
		if banned {
			entry.BanExpiresAt = info.banUntil
			entry.RemainingSeconds = int64(info.banUntil.Sub(now).Seconds())
		}
		snap.Entries = append(snap.Entries, entry)
	}
	g.mu.Unlock()
	sort.Slice(snap.Entries, func(i, j int) bool {
		a, b := snap.Entries[i], snap.Entries[j]
		if a.Banned != b.Banned {
			return a.Banned
		}
		if a.Banned {
			return a.RemainingSeconds > b.RemainingSeconds
		}
		return a.LastFailure.After(b.LastFailure)
	})
	return snap
}

// SetEscalation configures repeat-offender escalation. When an IP accumulates
// threshold automatic bans, callback is invoked to persist the IP to the
// permanent blacklist. A threshold <= 0 disables escalation.
func (g *Guard) SetEscalation(threshold int, callback func(clientIP string) error) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.escalationThreshold = threshold
	g.escalationCallback = callback
	g.mu.Unlock()
}

func intOrDefault(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func durationOrDefault(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

// Configure installs the process-wide guard configuration. It is applied to the
// active global guard immediately (when one exists) and remembered for guards
// installed later. Non-positive values fall back to the built-in defaults; a
// non-positive escalation threshold disables repeat-offender escalation.
func Configure(maxFailures int, ban, window time.Duration, escalationThreshold int) {
	p := policy{
		maxFailures: intOrDefault(maxFailures, defaultMaxFailures),
		ban:         durationOrDefault(ban, defaultBanDuration),
		window:      durationOrDefault(window, defaultWindowDuration),
		escalation:  escalationThreshold,
	}
	if p.escalation < 0 {
		p.escalation = 0
	}
	globalMu.Lock()
	globalPolicy = p
	guard := globalGuard
	globalMu.Unlock()
	if guard != nil {
		applyPolicy(guard, p)
	}
}

// SetBlacklist installs the process-wide IP/CIDR blacklist shared by the API
// and management endpoints. Entries are pre-parsed once so request-time
// lookups are O(1) for exact IPs and a short loop over CIDRs.
func SetBlacklist(entries []string) {
	normalized := make([]string, 0, len(entries))
	for _, entry := range entries {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			normalized = append(normalized, trimmed)
		}
	}

	ipSet := make(map[string]struct{}, len(normalized))
	cidrs := make([]*net.IPNet, 0, len(normalized))
	for _, entry := range normalized {
		if strings.Contains(entry, "/") {
			if _, cidr, err := net.ParseCIDR(entry); err == nil {
				cidrs = append(cidrs, cidr)
			}
			continue
		}
		if ip := net.ParseIP(entry); ip != nil {
			// Canonical string form so IPv4-mapped and dotted-quad forms match.
			ipSet[ip.String()] = struct{}{}
		}
	}

	globalMu.Lock()
	blacklist = normalized
	blacklistIPs = ipSet
	blacklistCIDRs = cidrs
	globalMu.Unlock()
}

var (
	blacklist      []string
	blacklistIPs   map[string]struct{}
	blacklistCIDRs []*net.IPNet
)

// IsBlacklisted reports whether the client IP matches any configured blacklist
// entry (single IP or CIDR range).
func IsBlacklisted(clientIP string) bool {
	if clientIP == "" {
		return false
	}
	globalMu.Lock()
	ipSet := blacklistIPs
	cidrs := blacklistCIDRs
	globalMu.Unlock()
	if len(ipSet) == 0 && len(cidrs) == 0 {
		return false
	}
	ip := net.ParseIP(clientIP)
	if ip == nil {
		return false
	}
	if _, ok := ipSet[ip.String()]; ok {
		return true
	}
	for _, cidr := range cidrs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

func applyPolicy(g *Guard, p policy) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.maxFailures = p.maxFailures
	g.banDuration = p.ban
	g.window = p.window
	g.escalationThreshold = p.escalation
}

// InstallGlobal installs the process-wide guard and returns it. Calling it again
// replaces the previous guard (which is stopped and loses its ban state).
func InstallGlobal(maxFailures int, ban, window time.Duration, escalationThreshold int) *Guard {
	Configure(maxFailures, ban, window, escalationThreshold)
	globalMu.Lock()
	defer globalMu.Unlock()
	if globalGuard != nil {
		globalGuard.Stop()
	}
	p := globalPolicy
	globalGuard = New(p.maxFailures, p.ban, p.window)
	globalGuard.escalationThreshold = p.escalation
	return globalGuard
}

// Global returns the process-wide guard, or nil when none is installed.
func Global() *Guard {
	globalMu.Lock()
	defer globalMu.Unlock()
	return globalGuard
}

// Middleware rejects requests from banned or blacklisted client IPs before
// any authentication or request logging runs. Loopback clients are never
// blocked. A nil guard lets every request through.
func (g *Guard) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		clientIP := RequestClientIP(c.Request)
		if clientIP != "" && (g.IsBanned(clientIP) || IsBlacklisted(clientIP)) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "ip_blacklisted"})
			return
		}
		c.Next()
	}
}

// ParseClientIPAndLoopback extracts the canonical IP address string from a
// RemoteAddr value (handling IPv4:port, [IPv6]:port, or bare IP) and reports
// whether the address is a loopback address. Returns "", false if the address
// is invalid.
func ParseClientIPAndLoopback(remoteAddr string) (string, bool) {
	remoteAddr = strings.TrimSpace(remoteAddr)
	if remoteAddr == "" {
		return "", false
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return "", false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", false
	}
	return ip.String(), ip.IsLoopback()
}

// RequestClientIP extracts the TCP peer address from the request. It uses
// RemoteAddr rather than client-controlled headers such as X-Forwarded-For so
// remote clients cannot spoof the address. Loopback peers return an empty
// string so local traffic is never throttled or banned.
func RequestClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	ip, isLoop := ParseClientIPAndLoopback(r.RemoteAddr)
	if isLoop || ip == "" {
		return ""
	}
	return ip
}

// Provider wraps an access provider with brute-force protection: banned client
// IPs are rejected before authentication runs, and authentication outcomes
// update the per-IP failure counter.
type Provider struct {
	inner sdkaccess.Provider
	guard *Guard
}

var _ sdkaccess.Provider = (*Provider)(nil)

// NewProvider wraps inner with the given guard. A nil guard or nil inner
// returns nil.
func NewProvider(inner sdkaccess.Provider, g *Guard) *Provider {
	if inner == nil || g == nil {
		return nil
	}
	return &Provider{inner: inner, guard: g}
}

// WrapProvider wraps provider with the guard. Providers already wrapped with
// the same guard are returned unchanged to keep reconciliation stable and
// avoid double counting.
func WrapProvider(provider sdkaccess.Provider, g *Guard) sdkaccess.Provider {
	if provider == nil || g == nil {
		return provider
	}
	if wrapped, ok := provider.(*Provider); ok && wrapped.guard == g {
		return provider
	}
	return NewProvider(provider, g)
}

// WrapProviders wraps every provider with the guard. A nil guard returns the
// slice unchanged.
func WrapProviders(providers []sdkaccess.Provider, g *Guard) []sdkaccess.Provider {
	if g == nil {
		return providers
	}
	wrapped := make([]sdkaccess.Provider, len(providers))
	for i, provider := range providers {
		wrapped[i] = WrapProvider(provider, g)
	}
	return wrapped
}

// Unwrap returns the provider wrapped inside a guard provider, or the provider
// itself when it is not wrapped.
func Unwrap(provider sdkaccess.Provider) sdkaccess.Provider {
	if wrapped, ok := provider.(*Provider); ok && wrapped != nil {
		return wrapped.inner
	}
	return provider
}

// Identifier returns the wrapped provider's identifier.
func (p *Provider) Identifier() string {
	if p == nil || p.inner == nil {
		return ""
	}
	return p.inner.Identifier()
}

// Authenticate enforces the ban state, then delegates to the wrapped provider.
func (p *Provider) Authenticate(ctx context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if p == nil || p.inner == nil {
		return nil, sdkaccess.NewInternalAuthError("auth guard provider not initialized", nil)
	}
	clientIP := RequestClientIP(r)
	if clientIP != "" && p.guard.IsBanned(clientIP) {
		return nil, sdkaccess.NewBannedError()
	}
	result, authErr := p.inner.Authenticate(ctx, r)
	if clientIP != "" {
		if authErr == nil {
			p.guard.RecordSuccess(clientIP)
		} else if sdkaccess.IsAuthErrorCode(authErr, sdkaccess.AuthErrorCodeInvalidCredential) ||
			sdkaccess.IsAuthErrorCode(authErr, sdkaccess.AuthErrorCodeNoCredentials) {
			p.guard.RecordFailure(clientIP)
		}
	}
	return result, authErr
}
