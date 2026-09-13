package helps

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// APIKeyModelIsCompat reports whether the selected API-key model enables
// compatibility handling for Claude thinking blocks.
//
// The upstream is-compat model flag is not modeled in this fork: every API-key
// Claude request is treated as replay-compatible so multi-turn signed thinking
// persists. The API-key gate is enforced by the caller via the auth kind. Refine
// this if an explicit per-model gate is introduced.
func APIKeyModelIsCompat(req cliproxyexecutor.Request) bool {
	_ = req
	return true
}

// ApplyRequestThinking applies thinking configuration to a request body.
// Local fork: delegates to thinking.ApplyThinking without model info or summary plumbing.
func ApplyRequestThinking(body []byte, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, fromFormat, toFormat, provider string) ([]byte, error) {
	return thinking.ApplyThinking(body, req.Model, fromFormat, toFormat, provider)
}
