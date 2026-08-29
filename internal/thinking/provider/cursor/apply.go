// Package cursor implements thinking configuration for Cursor models.
//
// Cursor models take reasoning knobs as RequestedModel parameters
// (thinking=true/false, effort/reasoning=<level>) that differ per model
// family and are validated against the account's model catalog. The canonical
// ThinkingConfig is therefore normalized into reasoning_effort on the
// OpenAI-shaped request body here; the cursor executor resolves the actual
// wire parameters against the catalog's declared options at request time.
package cursor

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Applier implements thinking.ProviderApplier for Cursor models.
type Applier struct{}

var _ thinking.ProviderApplier = (*Applier)(nil)

// NewApplier creates a new Cursor thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("cursor", NewApplier())
}

// Apply applies thinking configuration to the OpenAI-shaped request body by
// writing reasoning_effort ("none" disables thinking, other levels map onto
// the model's published effort/reasoning parameter values).
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if modelInfo != nil && modelInfo.Thinking == nil {
		// The model declares no reasoning surface; the pipeline has already
		// stripped the config for unsupported models.
		return body, nil
	}

	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	switch config.Mode {
	case thinking.ModeNone:
		return sjson.SetBytes(body, "reasoning_effort", string(thinking.LevelNone))
	case thinking.ModeLevel:
		if config.Level == "" {
			return body, nil
		}
		return sjson.SetBytes(body, "reasoning_effort", string(config.Level))
	case thinking.ModeBudget:
		level, ok := thinking.ConvertBudgetToLevel(config.Budget)
		if !ok {
			return body, nil
		}
		return sjson.SetBytes(body, "reasoning_effort", level)
	default:
		// ModeAuto: keep upstream defaults (most Cursor models think by
		// default; forcing it off triples time-to-first-token only when the
		// caller asked for it).
		return body, nil
	}
}
