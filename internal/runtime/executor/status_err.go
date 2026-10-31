package executor

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
)

// statusErr is the package-local upstream HTTP error used by executors that
// have not yet migrated fully to helps.DoJSON/DoStream.
//
// It intentionally mirrors helps.UpstreamStatusError so auth conductor can
// inspect StatusCode()/RetryAfter() uniformly for both types.
type statusErr struct {
	code               int
	msg                string
	retryAfter         *time.Duration
	credentialScoped   bool
	transientRateLimit bool
}

func (e statusErr) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return fmt.Sprintf("status %d", e.code)
}

func (e statusErr) StatusCode() int            { return e.code }
func (e statusErr) RetryAfter() *time.Duration { return e.retryAfter }

// IsCredentialScoped reports whether the error must cool down the entire
// credential instead of a single model (e.g. Codex usage_limit_reached).
func (e statusErr) IsCredentialScoped() bool { return e.credentialScoped }

// IsTransientRateLimit marks a transient per-model capacity/admission 429
// that must not be recorded as credential quota exhaustion.
func (e statusErr) IsTransientRateLimit() bool { return e.transientRateLimit }

// parseRetryAfterHeader parses an HTTP Retry-After header in either the
// delay-seconds or HTTP-date form. A present, valid value always yields a
// non-nil result so that an explicit immediate retry (0 seconds or a past
// date) stays distinguishable from an absent hint; missing or malformed
// values yield nil and let body-derived hints apply.
func parseRetryAfterHeader(headers http.Header, now time.Time) *time.Duration {
	if headers == nil {
		return nil
	}
	raw := strings.TrimSpace(headers.Get("Retry-After"))
	if raw == "" {
		return nil
	}
	if seconds, errParse := strconv.ParseFloat(raw, 64); errParse == nil && seconds >= 0 {
		delay := time.Duration(seconds * float64(time.Second))
		if seconds > float64(math.MaxInt64)/float64(time.Second) {
			// Out of representable range: clamp to the largest duration so
			// far-future deadlines only ever over-report recovery.
			delay = time.Duration(math.MaxInt64)
		}
		return &delay
	}
	if deadline, errParse := http.ParseTime(raw); errParse == nil {
	 delay := deadline.Sub(now)
		if delay < 0 {
			delay = 0
		}
		return &delay
	}
	return nil
}

// toStatusErr converts a helps.UpstreamStatusError into the local statusErr
// shape when a call site still needs the package-local type.
func toStatusErr(err error) error {
	if err == nil {
		return nil
	}
	if se, ok := err.(statusErr); ok {
		return se
	}
	if ue, ok := err.(helps.UpstreamStatusError); ok {
		return statusErr{
			code:               ue.Code,
			msg:                ue.Msg,
			retryAfter:         ue.RetryAfter(),
			transientRateLimit: false,
		}
	}
	return err
}

// classifyClaudeUpstreamError converts an upstream Claude error into a statusErr
// enriched with Anthropic rate-limit reset timing and credential-scoped flag.
// When modelLevelCooling is true, unified/shared-window rejections are scoped
// to the requested model rather than cooling the entire credential.
func classifyClaudeUpstreamError(err error, headers http.Header, modelLevelCooling bool) error {
	if err == nil {
		return nil
	}
	ue, ok := err.(helps.UpstreamStatusError)
	if !ok {
		return err
	}
	se := statusErr{code: ue.Code, msg: ue.Msg}
	if ue.Code == http.StatusTooManyRequests || (ue.Code >= 400 && ue.Code < 600) {
		// The body-aware parser narrows an explicit per-minute rate limit to its
		// short Retry-After instead of inheriting an unclassified unified reset.
		se.retryAfter = helps.ParseClaudeRateLimitResetForError(headers, []byte(ue.Msg), time.Now())
	}
	if ue.Code == http.StatusTooManyRequests {
		if !modelLevelCooling && helps.ClaudeHeadersIndicateUnifiedRateLimitRejection(headers) {
			se.credentialScoped = true
		}
	}
	return se
}
