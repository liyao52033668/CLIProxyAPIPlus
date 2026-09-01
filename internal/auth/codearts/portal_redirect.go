package codearts

// Portal redirect handling.
//
// After the user authorizes, the portal sends the browser to the local callback
// with a `redirect` query param pointing back at HuaweiCloud. Following that
// redirect is what finalizes the login upstream and makes the snap-manager login
// ticket claimable. It must be followed by the user's own browser, which carries
// the portal session; a server-side request has no such session and would only
// risk consuming the one-shot token in that URL.
//
// So for remote deployments the server validates the redirect and hands it back
// to the user to open, rather than fetching it itself.

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// portalRedirectHostSuffixes limits the redirect we are willing to hand back to
// the user to HuaweiCloud hosts.
var portalRedirectHostSuffixes = []string{
	"huaweicloud.com",
	"myhuaweicloud.com",
}

// ValidatePortalRedirect checks that a portal redirect extracted from a pasted
// callback URL is safe to show to the user: plain HTTP(S) to a public HuaweiCloud
// host, never a loopback, private or otherwise reserved address.
func ValidatePortalRedirect(rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("codearts: portal redirect is empty")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("codearts: parse portal redirect: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("codearts: portal redirect scheme %q is not allowed", u.Scheme)
	}

	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("codearts: portal redirect has no host")
	}
	if !hasAllowedPortalHostSuffix(host) {
		return "", fmt.Errorf("codearts: portal redirect host %q is not a HuaweiCloud host", host)
	}
	if err := rejectNonPublicHost(host); err != nil {
		return "", err
	}
	return u.String(), nil
}

func hasAllowedPortalHostSuffix(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, suffix := range portalRedirectHostSuffixes {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

// rejectNonPublicHost resolves the host and refuses loopback, private, link-local
// and other reserved destinations.
func rejectNonPublicHost(host string) error {
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("codearts: portal redirect host %q is not routable", host)
	}

	var addrs []net.IP
	if ip := net.ParseIP(host); ip != nil {
		addrs = []net.IP{ip}
	} else {
		resolved, err := net.LookupIP(host)
		if err != nil {
			return fmt.Errorf("codearts: resolve portal redirect host %q: %w", host, err)
		}
		addrs = resolved
	}
	if len(addrs) == 0 {
		return fmt.Errorf("codearts: portal redirect host %q did not resolve", host)
	}
	for _, ip := range addrs {
		if !isPublicIP(ip) {
			return fmt.Errorf("codearts: portal redirect host %q resolves to a non-public address", host)
		}
	}
	return nil
}

func isPublicIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		switch {
		// 100.64.0.0/10 carrier-grade NAT.
		case v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127:
			return false
		// 192.0.0.0/24 and 192.0.2.0/24 special-purpose ranges.
		case v4[0] == 192 && v4[1] == 0 && (v4[2] == 0 || v4[2] == 2):
			return false
		// 198.51.100.0/24 documentation range.
		case v4[0] == 198 && v4[1] == 51 && v4[2] == 100:
			return false
		// 203.0.113.0/24 documentation range.
		case v4[0] == 203 && v4[1] == 0 && v4[2] == 113:
			return false
		// 198.18.0.0/15 benchmarking range.
		case v4[0] == 198 && (v4[1] == 18 || v4[1] == 19):
			return false
		// 240.0.0.0/4 reserved.
		case v4[0] >= 240:
			return false
		}
		return true
	}
	// Unique-local IPv6 (fc00::/7).
	if len(ip) == net.IPv6len && ip[0]&0xfe == 0xfc {
		return false
	}
	return true
}
