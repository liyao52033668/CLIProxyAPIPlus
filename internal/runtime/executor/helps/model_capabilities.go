package helps

import (
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
