package quota

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/cpa/dto/apicall"
)

type commandCodeProvider struct {
	caller ManagementAPICaller
	config APICallConfig
}

func NewCommandCodeProvider(caller ManagementAPICaller, config APICallConfig) ProviderHandler {
	return commandCodeProvider{caller: caller, config: config}
}

func (p commandCodeProvider) Check(ctx context.Context, input ProviderInput) (ProviderOutput, error) {
	response, err := p.caller.CallManagementAPI(ctx, apicall.Request{
		AuthIndex: input.Identity.Identity,
		Method:    p.config.Method,
		URL:       p.config.URL,
		Header:    p.config.Headers,
	})
	if err != nil {
		return ProviderOutput{}, err
	}
	usage, err := parseCommandCodeUsagePayload(response)
	if err != nil {
		return ProviderOutput{}, err
	}
	return ProviderOutput{Provider: "commandcode", Result: CommandCodeResult{Usage: usage}}, nil
}
