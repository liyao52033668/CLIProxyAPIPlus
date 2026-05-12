package response

import (
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage/keeper/cpa/dto/authfiles"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage/keeper/cpa/dto/externalkeys"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage/keeper/cpa/dto/models"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage/keeper/cpa/dto/providerconfig"
)

// ExternalAPIKeysResult 是 FetchExternalAPIKeys 返回的 HTTP 包装，保留状态码、原始响应体和解析后的 DTO。
type ExternalAPIKeysResult struct {
	StatusCode int
	Body       []byte
	Payload    externalkeys.ExternalAPIKeysResponse
}

// ModelsResult 是 FetchModels 返回的 HTTP 包装，保留状态码、原始响应体和解析后的 DTO。
type ModelsResult struct {
	StatusCode int
	Body       []byte
	Payload    models.ModelsResponse
}

// AuthFilesResult 是 FetchAuthFiles 返回的 HTTP 包装，保留状态码、原始响应体和解析后的 DTO。
type AuthFilesResult struct {
	StatusCode int
	Body       []byte
	Payload    authfiles.AuthFilesResponse
}

// UsageQueueResult 是 FetchUsageQueue 返回的 HTTP 包装，payload 保留为 raw JSON 供 Redis usage 解码流程处理。
type UsageQueueResult struct {
	StatusCode int
	Body       []byte
	Payload    []json.RawMessage
}

// ProviderKeyConfigResult 是 provider API key 管理接口返回的 HTTP 包装，payload 是兼容归一化后的 provider 配置。
type ProviderKeyConfigResult struct {
	StatusCode int
	Body       []byte
	Payload    []providerconfig.ProviderKeyConfig
}

// OpenAICompatibilityResult 是 openai-compatibility 管理接口返回的 HTTP 包装，payload 是兼容归一化后的 provider 配置。
type OpenAICompatibilityResult struct {
	StatusCode int
	Body       []byte
	Payload    []providerconfig.OpenAICompatibilityConfig
}
