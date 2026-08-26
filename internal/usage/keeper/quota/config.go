package quota

import qoderauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/qoder"

type APICallConfig struct {
	Method  string
	URL     string
	Headers map[string]string
}

type ProviderConfigs struct {
	Antigravity         []APICallConfig
	Codex               APICallConfig
	GeminiCLI           APICallConfig
	GeminiCLICodeAssist APICallConfig
	ClaudeUsage         APICallConfig
	ClaudeProfile       APICallConfig
	Kimi                APICallConfig
	Qoder               APICallConfig
	CommandCode         APICallConfig
}

func DefaultProviderConfigs() ProviderConfigs {
	return ProviderConfigs{
		Antigravity: []APICallConfig{
			{
				Method: "POST",
				URL:    "https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels",
				Headers: map[string]string{
					"Authorization": "Bearer $TOKEN$",
					"Content-Type":  "application/json",
					"User-Agent":    "antigravity/1.11.5 windows/amd64",
				},
			},
			{
				Method: "POST",
				URL:    "https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:fetchAvailableModels",
				Headers: map[string]string{
					"Authorization": "Bearer $TOKEN$",
					"Content-Type":  "application/json",
					"User-Agent":    "antigravity/1.11.5 windows/amd64",
				},
			},
			{
				Method: "POST",
				URL:    "https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels",
				Headers: map[string]string{
					"Authorization": "Bearer $TOKEN$",
					"Content-Type":  "application/json",
					"User-Agent":    "antigravity/1.11.5 windows/amd64",
				},
			},
		},
		Codex: APICallConfig{
			Method: "GET",
			URL:    "https://chatgpt.com/backend-api/wham/usage",
			Headers: map[string]string{
				"Authorization": "Bearer $TOKEN$",
				"Content-Type":  "application/json",
				"User-Agent":    "codex_cli_rs/0.76.0 (Debian 13.0.0; x86_64) WindowsTerminal",
			},
		},
		GeminiCLI: APICallConfig{
			Method: "POST",
			URL:    "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota",
			Headers: map[string]string{
				"Authorization": "Bearer $TOKEN$",
				"Content-Type":  "application/json",
			},
		},
		GeminiCLICodeAssist: APICallConfig{
			Method: "POST",
			URL:    "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist",
			Headers: map[string]string{
				"Authorization": "Bearer $TOKEN$",
				"Content-Type":  "application/json",
			},
		},
		ClaudeUsage: APICallConfig{
			Method: "GET",
			URL:    "https://api.anthropic.com/api/oauth/usage",
			Headers: map[string]string{
				"Authorization":  "Bearer $TOKEN$",
				"Content-Type":   "application/json",
				"anthropic-beta": "oauth-2025-04-20",
			},
		},
		ClaudeProfile: APICallConfig{
			Method: "GET",
			URL:    "https://api.anthropic.com/api/oauth/profile",
			Headers: map[string]string{
				"Authorization":  "Bearer $TOKEN$",
				"Content-Type":   "application/json",
				"anthropic-beta": "oauth-2025-04-20",
			},
		},
		Kimi: APICallConfig{
			Method: "GET",
			URL:    "https://api.kimi.com/coding/v1/usages",
			Headers: map[string]string{
				"Authorization": "Bearer $TOKEN$",
			},
		},
		Qoder: APICallConfig{
			Method: "GET",
			URL:    "https://openapi.qoder.sh/api/v2/quota/usage",
			Headers: map[string]string{
				"Authorization": "Bearer $TOKEN$",
				"Accept":        "application/json",
				"User-Agent":    "qoder/" + qoderauth.CosyVersion,
			},
		},
		CommandCode: APICallConfig{
			Method: "GET",
			URL:    "https://api.commandcode.ai/internal/usage/summary",
			Headers: map[string]string{
				"Cookie":       "__Secure-commandcode_prod_.session_token=$TOKEN$",
				"Accept":       "*/*",
				"Content-Type": "application/json",
				"Origin":       "https://commandcode.ai",
				"Host":         "api.commandcode.ai",
				"Connection":   "keep-alive",
				"User-Agent":   "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36",
			},
		},
	}
}

func (c ProviderConfigs) APICallTemplates() []APICallConfig {
	templates := make([]APICallConfig, 0, len(c.Antigravity)+8)
	templates = append(templates, c.Antigravity...)
	templates = append(templates,
		c.Codex,
		c.GeminiCLI,
		c.GeminiCLICodeAssist,
		c.ClaudeUsage,
		c.ClaudeProfile,
		c.Kimi,
		c.Qoder,
		c.CommandCode,
	)
	return templates
}
