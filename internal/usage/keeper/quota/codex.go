package quota

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/cpa/dto/apicall"
)

type codexProvider struct {
	caller ManagementAPICaller
	config APICallConfig
}

func NewCodexProvider(caller ManagementAPICaller, config APICallConfig) ProviderHandler {
	return codexProvider{caller: caller, config: config}
}

func (p codexProvider) Check(ctx context.Context, input ProviderInput) (ProviderOutput, error) {
	// The official API allows requests without an account ID; append the account header when syncing a specific account, otherwise use common auth headers for quota refresh.
	headers := p.config.Headers
	if accountID := optionalAccountID(input.Identity.AccountID); accountID != "" {
		headers = mergeHeaders(headers, map[string]string{"Chatgpt-Account-Id": accountID})
	}
	// Always call CPA api-call so the backend fills fixed URL/headers and per-account dynamic headers.
	request := apicall.Request{
		AuthIndex: input.Identity.Identity,
		Method:    p.config.Method,
		URL:       p.config.URL,
		Header:    headers,
	}
	response, err := p.caller.CallManagementAPI(ctx, request)
	if err != nil {
		return ProviderOutput{}, err
	}
	usage, err := parseCodexUsagePayload(response)
	if err != nil {
		return ProviderOutput{}, err
	}
	return ProviderOutput{Provider: "codex", Result: CodexResult{Usage: usage}}, nil
}

func optionalAccountID(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
