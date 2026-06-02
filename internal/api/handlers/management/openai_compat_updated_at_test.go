package management

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestOpenAICompatibilityWithAuthIndex_PreservesUpdatedAt(t *testing.T) {
	t.Parallel()

	updatedAt := time.Date(2026, 6, 2, 10, 30, 0, 0, time.UTC)
	h := &Handler{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{
				{
					Name:      "openrouter",
					BaseURL:   "https://openrouter.ai/api/v1",
					Disabled:  false,
					UpdatedAt: &updatedAt,
				},
			},
		},
	}

	got := h.openAICompatibilityWithAuthIndex()
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].UpdatedAt == nil {
		t.Fatalf("UpdatedAt is nil, want %s", updatedAt.Format(time.RFC3339))
	}
	if !got[0].UpdatedAt.Equal(updatedAt) {
		t.Fatalf("UpdatedAt = %s, want %s", got[0].UpdatedAt.Format(time.RFC3339), updatedAt.Format(time.RFC3339))
	}
}
