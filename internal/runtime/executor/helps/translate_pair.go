package helps

import (
	"context"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// TranslateRequestPair translates a baseline payload and a working payload once
// when both slices share the same backing storage.
func TranslateRequestPair(ctx context.Context, headers http.Header, cfg *config.Config, from, to sdktranslator.Format, model string, baseline, working []byte, stream bool) (original, translated []byte) {
	original = sdktranslator.TranslateRequest(from, to, model, baseline, stream)
	if sameByteSlice(baseline, working) {
		return original, append([]byte(nil), original...)
	}
	return original, sdktranslator.TranslateRequest(from, to, model, working, stream)
}

func sameByteSlice(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	return &a[0] == &b[0]
}
