// Package commandcode implements thinking configuration for Command Code models.
//
// Command Code models use the OpenAI-style reasoning_effort field, located on
// the wire envelope's params object, with discrete effort levels. The canonical
// pipeline converts budget and auto modes to levels before the applier runs.
package commandcode

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// effortField is the wire envelope path carrying the reasoning effort.
const effortField = "params.reasoning_effort"

// Applier implements thinking.ProviderApplier for Command Code models.
//
// Command Code-specific behavior:
//   - Output format: params.reasoning_effort (string: low/medium/high/xhigh/max)
//   - Level-only mode: numeric budgets are converted to the nearest level
//   - Models that cannot disable thinking fall back to the clamped lowest level
type Applier struct{}

var _ thinking.ProviderApplier = (*Applier)(nil)

// NewApplier creates a new Command Code thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("commandcode", NewApplier())
}

// Apply applies thinking configuration to a Command Code wire envelope.
//
// Expected output format:
//
//	{
//	  "params": {
//	    "reasoning_effort": "high"
//	  }
//	}
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if thinking.IsUserDefinedModel(modelInfo) {
		return applyCompatible(body, config)
	}
	if modelInfo.Thinking == nil {
		return body, nil
	}
	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	var effort string
	switch config.Mode {
	case thinking.ModeLevel:
		if config.Level == "" {
			return body, nil
		}
		effort = string(config.Level)
	case thinking.ModeNone:
		// Respect the clamped fallback level for models that cannot disable
		// thinking; otherwise clear the field so upstream defaults apply.
		if config.Level != "" && config.Level != thinking.LevelNone {
			effort = string(config.Level)
			break
		}
		if thinking.HasLevel(modelInfo.Thinking.Levels, string(thinking.LevelNone)) {
			effort = string(thinking.LevelNone)
			break
		}
		return clearEffort(body)
	case thinking.ModeBudget:
		// Convert budget to level using threshold mapping
		level, ok := thinking.ConvertBudgetToLevel(config.Budget)
		if !ok {
			return body, nil
		}
		effort = level
	case thinking.ModeAuto:
		effort = string(thinking.LevelAuto)
	default:
		return body, nil
	}

	if effort == "" {
		return body, nil
	}
	result, err := sjson.SetBytes(body, effortField, effort)
	if err != nil {
		return body, err
	}
	return result, nil
}

// applyCompatible applies thinking config for user-defined Command Code models
// without ThinkingSupport validation, letting the upstream validate it.
func applyCompatible(body []byte, config thinking.ThinkingConfig) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	var effort string
	switch config.Mode {
	case thinking.ModeLevel:
		if config.Level == "" {
			return body, nil
		}
		effort = string(config.Level)
	case thinking.ModeNone:
		effort = string(thinking.LevelNone)
		if config.Level != "" {
			effort = string(config.Level)
		}
	case thinking.ModeAuto:
		effort = string(thinking.LevelAuto)
	case thinking.ModeBudget:
		level, ok := thinking.ConvertBudgetToLevel(config.Budget)
		if !ok {
			return body, nil
		}
		effort = level
	default:
		return body, nil
	}

	if effort == "" {
		return body, nil
	}
	result, err := sjson.SetBytes(body, effortField, effort)
	if err != nil {
		return body, err
	}
	return result, nil
}

func clearEffort(body []byte) ([]byte, error) {
	result, err := sjson.DeleteBytes(body, effortField)
	if err != nil {
		return body, err
	}
	return result, nil
}
