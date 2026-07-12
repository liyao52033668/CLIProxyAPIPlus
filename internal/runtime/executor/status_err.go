package executor

import (
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
)

// statusErr is the package-local upstream HTTP error used by executors that
// have not yet migrated fully to helps.DoJSON/DoStream.
//
// It intentionally mirrors helps.UpstreamStatusError so auth conductor can
// inspect StatusCode()/RetryAfter() uniformly for both types.
type statusErr struct {
	code       int
	msg        string
	retryAfter *time.Duration
}

func (e statusErr) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return fmt.Sprintf("status %d", e.code)
}

func (e statusErr) StatusCode() int            { return e.code }
func (e statusErr) RetryAfter() *time.Duration { return e.retryAfter }

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
