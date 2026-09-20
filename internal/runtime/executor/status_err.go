package executor

import (
	"fmt"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
)

// statusErr is the package-local upstream HTTP error used by executors that
// have not yet migrated fully to helps.DoJSON/DoStream.
//
// It intentionally mirrors helps.UpstreamStatusError so auth conductor can
// inspect StatusCode()/RetryAfter() uniformly for both types.
type statusErr struct {
	code             int
	msg              string
	retryAfter       *time.Duration
	credentialScoped bool
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
		return statusErr{code: ue.Code, msg: ue.Msg, retryAfter: ue.RetryAfter()}
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
		se.retryAfter = helps.ParseClaudeRateLimitReset(headers, time.Now())
	}
	if ue.Code == http.StatusTooManyRequests {
		if !modelLevelCooling && helps.ClaudeHeadersIndicateUnifiedRateLimitRejection(headers) {
			se.credentialScoped = true
		}
	}
	return se
}
