package helps

import (
	"context"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// EnsureSessionContext is a stub that returns ctx unchanged.
// The full upstream implementation carries session identity for $CPA-SESSION-ID expansion.
// TODO: port the full session ID infrastructure from upstream if needed.
func EnsureSessionContext(ctx context.Context, opts cliproxyexecutor.Options, payload []byte) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return ctx
}
