package quota

import (
	"context"

	qoderauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/cpa/dto/apicall"
)

type qoderProvider struct {
	caller ManagementAPICaller
	config APICallConfig
}

func NewQoderProvider(caller ManagementAPICaller, config APICallConfig) ProviderHandler {
	return qoderProvider{caller: caller, config: config}
}

func (p qoderProvider) Check(ctx context.Context, input ProviderInput) (ProviderOutput, error) {
	// Qoder exposes a single OpenAPI credits endpoint; auth_index supplies the Bearer token via $TOKEN$.
	// User-Agent version tracks the live qodercli channel, same as the executor path.
	headers := mergeHeaders(p.config.Headers, map[string]string{
		"User-Agent": "qoder/" + qoderauth.GetCosyVersion(),
	})
	response, err := p.caller.CallManagementAPI(ctx, apicall.Request{
		AuthIndex: input.Identity.Identity,
		Method:    p.config.Method,
		URL:       p.config.URL,
		Header:    headers,
	})
	if err != nil {
		return ProviderOutput{}, err
	}
	usage, err := parseQoderUsagePayload(response)
	if err != nil {
		return ProviderOutput{}, err
	}
	return ProviderOutput{Provider: "qoder", Result: QoderResult{Usage: usage}}, nil
}
