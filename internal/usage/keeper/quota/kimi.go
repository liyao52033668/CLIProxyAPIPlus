package quota

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/cpa/dto/apicall"
)

type kimiProvider struct {
	caller ManagementAPICaller
	config APICallConfig
}

func NewKimiProvider(caller ManagementAPICaller, config APICallConfig) ProviderHandler {
	return kimiProvider{caller: caller, config: config}
}

func (p kimiProvider) Check(ctx context.Context, input ProviderInput) (ProviderOutput, error) {
	// Kimi only needs the current auth_index against a single usage endpoint; parse then convert via the shared export path.
	response, err := p.caller.CallManagementAPI(ctx, apicall.Request{
		AuthIndex: input.Identity.Identity,
		Method:    p.config.Method,
		URL:       p.config.URL,
		Header:    p.config.Headers,
	})
	if err != nil {
		return ProviderOutput{}, err
	}
	usage, err := parseKimiUsagePayload(response)
	if err != nil {
		return ProviderOutput{}, err
	}
	return ProviderOutput{Provider: "kimi", Result: KimiResult{Usage: usage}}, nil
}
