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

// TranslateRequestPairWithCodexMultiAgentV2 translates the untouched baseline
// payload and the working payload that later stages mutate in place.
// Local stub: multi-agent v2 optimization is not enabled in this fork.
func TranslateRequestPairWithCodexMultiAgentV2(ctx context.Context, headers http.Header, cfg *config.Config, from, to sdktranslator.Format, model string, originalPayload, requestPayload []byte, stream bool) (original, working []byte) {
	return TranslateRequestPair(ctx, headers, cfg, from, to, model, originalPayload, requestPayload, stream)
}

// TranslateRequestWithCodexMultiAgentV2 normalizes official Codex multi-agent
// input before translating it to a non-Codex target protocol.
// Local stub: multi-agent v2 optimization is not enabled in this fork.
func TranslateRequestWithCodexMultiAgentV2(ctx context.Context, headers http.Header, cfg *config.Config, from, to sdktranslator.Format, model string, payload []byte, stream bool) []byte {
	return sdktranslator.TranslateRequest(from, to, model, payload, stream)
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
