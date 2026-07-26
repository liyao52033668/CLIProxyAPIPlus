package quota

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/cpa/dto/apicall"
)

type claudeProvider struct {
	caller        ManagementAPICaller
	usageConfig   APICallConfig
	profileConfig APICallConfig
}

func NewClaudeProvider(caller ManagementAPICaller, usageConfig APICallConfig, profileConfig APICallConfig) ProviderHandler {
	return claudeProvider{caller: caller, usageConfig: usageConfig, profileConfig: profileConfig}
}

func (p claudeProvider) Check(ctx context.Context, input ProviderInput) (ProviderOutput, error) {
	// Claude requires usage first, then profile; profile carries label data needed by the frontend.
	usageResponse, err := p.caller.CallManagementAPI(ctx, apicall.Request{
		AuthIndex: input.Identity.Identity,
		Method:    p.usageConfig.Method,
		URL:       p.usageConfig.URL,
		Header:    p.usageConfig.Headers,
	})
	if err != nil {
		return ProviderOutput{}, err
	}
	usage, err := parseClaudeUsagePayload(usageResponse)
	if err != nil {
		return ProviderOutput{}, err
	}
	// Query profile only after usage parses successfully so profile errors do not hide primary quota failures.
	profileResponse, err := p.caller.CallManagementAPI(ctx, apicall.Request{
		AuthIndex: input.Identity.Identity,
		Method:    p.profileConfig.Method,
		URL:       p.profileConfig.URL,
		Header:    p.profileConfig.Headers,
	})
	if err != nil {
		return ProviderOutput{}, err
	}
	profile, err := parseClaudeProfilePayload(profileResponse)
	if err != nil {
		return ProviderOutput{}, err
	}
	return ProviderOutput{Provider: "claude", Result: ClaudeResult{Usage: usage, Profile: profile}}, nil
}
