// Package qoder provides OAuth2 authentication functionality for the Qoder provider.
package qoder

import "time"

// Qoder login configuration
const (
	CallbackPort = 51122
	AuthBase     = "https://qoder.com"
	CenterBase   = "https://center.qoder.sh"
	ChatBase     = "https://api3.qoder.sh"
	OpenAPIBase  = "https://openapi.qoder.sh"
	// CosyVersion is the compile-time fallback qodercli version; the live value
	// is resolved from the remote channel manifest via GetCosyVersion.
	CosyVersion = "1.1.5"
	// RedirectURI is retained for legacy IDE-style callbacks; qodercli device
	// flow no longer requires it.
	RedirectURI = "qoder://aicoding.aicoding-agent/login-success"

	// ClientIDCLI is the production client_id used by official qodercli.
	ClientIDCLI = "e883ade2-e6e3-4d6d-adf7-f92ceff5fdcb"
	// ClientIDAlt is the non-production client_id used by qodercli.
	ClientIDAlt = "e93fe488-5778-4c35-a6fc-0f54ed7b3139"
)

// SelectAccountsPath is the browser login page path.
const SelectAccountsPath = "/device/selectAccounts"

// OpenAPI auth endpoints used by qodercli.
const (
	DeviceTokenPollPath    = "/api/v1/deviceToken/poll"
	DeviceTokenRefreshPath = "/api/v1/deviceToken/refresh"
	JobTokenExchangePath   = "/api/v1/jobToken/exchange"
	UserInfoPath           = "/api/v1/userinfo"
)

// ServerPublicKeyPEM is the RSA public key for COSY authentication.
const ServerPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`

// Custom base64 encoding alphabet used by Qoder body encoding.
const (
	CustomPad      = '$'
	StdAlphabet    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	CustomAlphabet = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"
)

// Chat endpoint path
const (
	ChatPath       = "/algo/api/v2/service/pro/sse/agent_chat_generation"
	ChatQueryExtra = "FetchKeys=llm_model_result&AgentId=agent_common&Encode=1"
	ModelListPath  = "/algo/api/v2/model/list"
	UserPlanPath   = "/algo/api/v2/user/plan"
	UserStatusPath = "/api/v3/user/status"
)

// Polling configuration (aligned with qodercli: 1s interval, 5 minute timeout).
const (
	PollInterval         = 1 * time.Second
	PollTimeout          = 5 * time.Minute
	PollBaseDelay        = 3 * time.Second
	PollMaxDelay         = 30 * time.Second
	PollBackoffMultiply  = 1.5
	PollMaxAttempts      = 100
	MaxConsecutiveErrors = 5
)
