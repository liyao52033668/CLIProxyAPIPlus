package helps

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// AntigravityTOSViolation captures the parsed state of a 403 PERMISSION_DENIED
// carrying an ErrorInfo reason=TOS_VIOLATION. This is a terminal, service-scoped
// credential suspension: the OAuth token and model catalog keep working while
// inference is refused, so the credential must be deactivated rather than
// merely cooled down.
type AntigravityTOSViolation struct {
	AppealURL     string
	AppealURLText string
}

// ParseAntigravityTOSViolation extracts the TOS_VIOLATION signature from a
// Google RPC error body. It returns nil for any other shape, including
// transient 429 quota errors and non-TOS 403s.
func ParseAntigravityTOSViolation(body []byte) *AntigravityTOSViolation {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return nil
	}
	if code := int(gjson.GetBytes(body, "error.code").Int()); code != http.StatusForbidden {
		return nil
	}
	if status := strings.TrimSpace(gjson.GetBytes(body, "error.status").String()); !strings.EqualFold(status, "PERMISSION_DENIED") {
		return nil
	}
	details := gjson.GetBytes(body, "error.details")
	if !details.Exists() || !details.IsArray() {
		return nil
	}
	for _, detail := range details.Array() {
		if detail.Get("@type").String() != "type.googleapis.com/google.rpc.ErrorInfo" {
			continue
		}
		if reason := strings.TrimSpace(detail.Get("reason").String()); !strings.EqualFold(reason, "TOS_VIOLATION") {
			continue
		}
		meta := detail.Get("metadata")
		return &AntigravityTOSViolation{
			AppealURL:     strings.TrimSpace(meta.Get("appeal_url").String()),
			AppealURLText: strings.TrimSpace(meta.Get("appeal_url_link_text").String()),
		}
	}
	return nil
}
