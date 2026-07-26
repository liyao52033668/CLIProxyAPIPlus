package quota

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/cpa/dto/apicall"
)

type antigravityProvider struct {
	caller  ManagementAPICaller
	configs []APICallConfig
}

func NewAntigravityProvider(caller ManagementAPICaller, configs ...APICallConfig) ProviderHandler {
	return antigravityProvider{caller: caller, configs: configs}
}

func (p antigravityProvider) Check(ctx context.Context, input ProviderInput) (ProviderOutput, error) {
	// Antigravity quota depends on project_id; block the request and ask the user to complete auth-file metadata when missing.
	if input.Identity.ProjectID == nil || *input.Identity.ProjectID == "" {
		return ProviderOutput{}, fmt.Errorf("%w: missing project_id parameter", ErrProviderInput)
	}
	if len(p.configs) == 0 {
		return ProviderOutput{}, fmt.Errorf("%w: antigravity config is required", ErrProviderInput)
	}
	// Try candidate endpoints in configured order until a usable quota is parsed.
	var lastErr error
	for _, config := range p.configs {
		response, err := p.caller.CallManagementAPI(ctx, apicall.Request{
			AuthIndex: input.Identity.Identity,
			Method:    config.Method,
			URL:       config.URL,
			Header:    config.Headers,
			Data:      map[string]string{"project": *input.Identity.ProjectID},
		})
		if err != nil {
			lastErr = err
			continue
		}
		quota, err := parseAntigravityQuotaPayload(response)
		if err != nil {
			lastErr = err
			continue
		}
		return ProviderOutput{Provider: "antigravity", Result: AntigravityResult{Quota: quota}}, nil
	}
	return ProviderOutput{}, lastErr
}
