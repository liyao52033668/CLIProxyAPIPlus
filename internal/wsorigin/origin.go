// Package wsorigin provides shared WebSocket origin validation helpers.
package wsorigin

import (
	"net/http"
	"net/url"
	"strings"
)

// SameOrigin allows non-browser clients without an Origin header and requires
// browser WebSocket requests to use the same authority as the HTTP request.
func SameOrigin(r *http.Request) bool {
	if r == nil {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, errParse := url.Parse(origin)
	if errParse != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}
