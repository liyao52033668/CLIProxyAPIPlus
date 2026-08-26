// Package openai provides translation between OpenAI Chat Completions and the
// Command Code wire protocol.
package openai

import (
	. "github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/translator"
)

func init() {
	translator.Register(
		OpenAI, // source format
		CommandCode,
		ConvertOpenAIToCommandCodeRequest,
		interfaces.TranslateResponse{
			Stream:    ConvertCommandCodeStreamToOpenAI,
			NonStream: ConvertCommandCodeNonStreamToOpenAI,
		},
	)
}
