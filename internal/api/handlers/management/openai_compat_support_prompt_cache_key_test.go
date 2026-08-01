package management

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestOpenAICompatibilityWithAuthIndex_IncludesSupportPromptCacheKey(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{
				{
					Name:                  "openrouter",
					BaseURL:               "https://openrouter.ai/api/v1",
					SupportPromptCacheKey: true,
				},
			},
		},
	}

	got := h.openAICompatibilityWithAuthIndex()
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if !got[0].SupportPromptCacheKey {
		t.Fatalf("SupportPromptCacheKey = false, want true")
	}
}
