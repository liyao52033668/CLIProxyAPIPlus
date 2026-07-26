package response

import (
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/cpa/dto/authfiles"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/cpa/dto/externalkeys"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/cpa/dto/models"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/cpa/dto/providerconfig"
)

// ExternalAPIKeysResult is the HTTP wrapper returned by FetchExternalAPIKeys, keeping status code, raw body, and parsed DTO.
type ExternalAPIKeysResult struct {
	StatusCode int
	Body       []byte
	Payload    externalkeys.ExternalAPIKeysResponse
}

// ModelsResult is the HTTP wrapper returned by FetchModels, keeping status code, raw body, and parsed DTO.
type ModelsResult struct {
	StatusCode int
	Body       []byte
	Payload    models.ModelsResponse
}

// AuthFilesResult is the HTTP wrapper returned by FetchAuthFiles, keeping status code, raw body, and parsed DTO.
type AuthFilesResult struct {
	StatusCode int
	Body       []byte
	Payload    authfiles.AuthFilesResponse
}

// UsageQueueResult is the HTTP wrapper returned by FetchUsageQueue; payload stays raw JSON for the Redis usage decode pipeline.
type UsageQueueResult struct {
	StatusCode int
	Body       []byte
	Payload    []json.RawMessage
}

// ProviderKeyConfigResult is the HTTP wrapper from provider API-key management APIs; payload is compatibility-normalized provider config.
type ProviderKeyConfigResult struct {
	StatusCode int
	Body       []byte
	Payload    []providerconfig.ProviderKeyConfig
}

// OpenAICompatibilityResult is the HTTP wrapper from the openai-compatibility management API; payload is compatibility-normalized provider config.
type OpenAICompatibilityResult struct {
	StatusCode int
	Body       []byte
	Payload    []providerconfig.OpenAICompatibilityConfig
}
