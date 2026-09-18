// Package lobsterai implements thinking configuration for LobsterAI models.
//
// LobsterAI serves OpenAI-compatible chat completions and controls reasoning
// through the discrete reasoning_effort levels advertised by the official
// client (minimal/low/medium/high/xhigh/max); there is no numeric budget.
package lobsterai

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Applier implements thinking.ProviderApplier for LobsterAI models.
type Applier struct{}

var _ thinking.ProviderApplier = (*Applier)(nil)

// NewApplier creates a new LobsterAI thinking applier.
func NewApplier() *Applier { return &Applier{} }

func init() {
	thinking.RegisterProvider("lobsterai", NewApplier())
}

// Apply writes a discrete reasoning_effort level, or removes the field when
// thinking is disabled. Token budgets are translated to the nearest level.
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}
	if modelInfo != nil && !thinking.IsUserDefinedModel(modelInfo) && modelInfo.Thinking == nil {
		// Catalog model without reasoning support: leave the payload untouched.
		return body, nil
	}

	switch config.Mode {
	case thinking.ModeNone:
		// LobsterAI has no "none" level; disabling thinking means omitting it.
		result, _ := sjson.DeleteBytes(body, "reasoning_effort")
		return result, nil
	case thinking.ModeLevel:
		if config.Level == "" {
			return body, nil
		}
		return setReasoningEffort(body, string(config.Level))
	case thinking.ModeAuto:
		return setReasoningEffort(body, string(thinking.LevelAuto))
	case thinking.ModeBudget:
		level, ok := thinking.ConvertBudgetToLevel(config.Budget)
		if !ok {
			return body, nil
		}
		return setReasoningEffort(body, level)
	default:
		return body, nil
	}
}

func setReasoningEffort(body []byte, effort string) ([]byte, error) {
	result, _ := sjson.SetBytes(body, "reasoning_effort", effort)
	return result, nil
}
