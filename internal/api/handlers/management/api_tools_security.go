package management

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type apiCallResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

var deniedAPICallPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

var apiCallTokenHostsByProvider = map[string]map[string]struct{}{
	"antigravity": {
		"cloudcode-pa.googleapis.com":               {},
		"daily-cloudcode-pa.googleapis.com":         {},
		"daily-cloudcode-pa.sandbox.googleapis.com": {},
	},
	"claude": {
		"api.anthropic.com": {},
	},
	"codex": {
		"api.openai.com": {},
		"chatgpt.com":    {},
	},
	"cursor": {
		"api2.cursor.sh": {},
		"cursor.com":     {},
	},
	"gemini": {
		"cloudcode-pa.googleapis.com":       {},
		"generativelanguage.googleapis.com": {},
	},
	"github-copilot": {
		"api.github.com":        {},
		"api.githubcopilot.com": {},
	},
	"kimi": {
		"api.kimi.com": {},
	},
	"xai": {
		"api.x.ai":                {},
		"cli-chat-proxy.grok.com": {},
	},
}

func (h *Handler) apiCallDNSResolver() apiCallResolver {
	if h != nil && h.apiCallResolver != nil {
		return h.apiCallResolver
	}
	return net.DefaultResolver
}

func validateAPICallURL(ctx context.Context, target *url.URL, resolver apiCallResolver) error {
	if target == nil {
		return errors.New("target URL is nil")
	}
	if !strings.EqualFold(strings.TrimSpace(target.Scheme), "https") {
		return errors.New("target URL must use HTTPS")
	}
	if target.User != nil {
		return errors.New("target URL userinfo is not allowed")
	}

	hostname := canonicalAPICallHostname(target.Hostname())
	if hostname == "" || strings.Contains(hostname, "%") {
		return errors.New("target URL hostname is invalid")
	}
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") {
		return errors.New("localhost targets are not allowed")
	}
	if port := target.Port(); port != "" {
		portNumber, errPort := strconv.Atoi(port)
		if errPort != nil || portNumber < 1 || portNumber > 65535 {
			return errors.New("target URL port is invalid")
		}
	}

	_, errResolve := resolvePublicAPICallIPs(ctx, hostname, resolver)
	return errResolve
}

func resolvePublicAPICallIPs(ctx context.Context, hostname string, resolver apiCallResolver) ([]net.IPAddr, error) {
	if ip := net.ParseIP(hostname); ip != nil {
		if isDeniedAPICallIP(ip) {
			return nil, errors.New("target IP address is not public")
		}
		return []net.IPAddr{{IP: ip}}, nil
	}
	if resolver == nil {
		return nil, errors.New("target resolver is unavailable")
	}

	addresses, errLookup := resolver.LookupIPAddr(ctx, hostname)
	if errLookup != nil {
		return nil, fmt.Errorf("resolve target hostname: %w", errLookup)
	}
	if len(addresses) == 0 {
		return nil, errors.New("target hostname has no IP addresses")
	}
	for _, address := range addresses {
		if address.IP == nil || isDeniedAPICallIP(address.IP) {
			return nil, errors.New("target hostname resolves to a non-public IP address")
		}
	}
	return addresses, nil
}

func isDeniedAPICallIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	address = address.Unmap()
	for _, prefix := range deniedAPICallPrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return !address.IsGlobalUnicast()
}

func canonicalAPICallHostname(hostname string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostname), "."))
}

func canonicalAPICallPort(target *url.URL) string {
	if target == nil {
		return ""
	}
	if port := target.Port(); port != "" {
		return port
	}
	if strings.EqualFold(target.Scheme, "https") {
		return "443"
	}
	return ""
}

func sameAPICallOrigin(left, right *url.URL) bool {
	if left == nil || right == nil {
		return false
	}
	return strings.EqualFold(left.Scheme, right.Scheme) &&
		canonicalAPICallHostname(left.Hostname()) == canonicalAPICallHostname(right.Hostname()) &&
		canonicalAPICallPort(left) == canonicalAPICallPort(right)
}

func (h *Handler) isTrustedAPICallTokenDestination(target *url.URL, auth *coreauth.Auth) bool {
	if target == nil || auth == nil || !strings.EqualFold(target.Scheme, "https") {
		return false
	}

	if baseURL := authBaseURL(auth); baseURL != nil && sameAPICallOrigin(target, baseURL) {
		return true
	}

	hostname := canonicalAPICallHostname(target.Hostname())
	if hostname == "" || canonicalAPICallPort(target) != "443" {
		return false
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	trustedHosts := apiCallTokenHostsByProvider[provider]
	_, ok := trustedHosts[hostname]
	return ok
}

func authBaseURL(auth *coreauth.Auth) *url.URL {
	if auth == nil {
		return nil
	}
	candidates := make([]string, 0, 2)
	if auth.Attributes != nil {
		candidates = append(candidates, auth.Attributes["base_url"])
	}
	if auth.Metadata != nil {
		if baseURL, ok := auth.Metadata["base_url"].(string); ok {
			candidates = append(candidates, baseURL)
		}
	}
	for _, candidate := range candidates {
		parsed, errParse := url.Parse(strings.TrimSpace(candidate))
		if errParse == nil && parsed.Hostname() != "" && strings.EqualFold(parsed.Scheme, "https") && parsed.User == nil {
			return parsed
		}
	}
	return nil
}

func newAPICallRedirectPolicy(initialURL *url.URL, tokenInjected bool, resolver apiCallResolver) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if req == nil || req.URL == nil {
			return errors.New("redirect URL is invalid")
		}
		if errValidate := validateAPICallURL(req.Context(), req.URL, resolver); errValidate != nil {
			return fmt.Errorf("redirect target is not allowed: %w", errValidate)
		}
		if tokenInjected && !sameAPICallOrigin(initialURL, req.URL) {
			return errors.New("credential-bearing redirects must remain on the original origin")
		}
		return nil
	}
}

type securedAPICallTransport struct {
	base     http.RoundTripper
	resolver apiCallResolver
}

func (t *securedAPICallTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, errors.New("api call request URL is invalid")
	}
	if errValidate := validateAPICallURL(req.Context(), req.URL, t.resolver); errValidate != nil {
		return nil, fmt.Errorf("api call target is not allowed: %w", errValidate)
	}
	if t.base == nil {
		return nil, errors.New("api call transport is unavailable")
	}
	return t.base.RoundTrip(req)
}
