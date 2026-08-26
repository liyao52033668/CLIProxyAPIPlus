package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	commandcodeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/commandcode"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// CommandCodeAuthenticator implements API key authentication for Command Code.
type CommandCodeAuthenticator struct{}

// NewCommandCodeAuthenticator constructs a new Command Code authenticator.
func NewCommandCodeAuthenticator() Authenticator {
	return &CommandCodeAuthenticator{}
}

// Provider returns the provider key for Command Code.
func (CommandCodeAuthenticator) Provider() string {
	return "commandcode"
}

// RefreshLead returns nil: the API key is long-lived and needs no refresh.
func (CommandCodeAuthenticator) RefreshLead() *time.Duration {
	return nil
}

// Login prompts for the Command Code API key and persists it.
func (a CommandCodeAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	promptFn := opts.Prompt
	if promptFn == nil {
		promptFn = func(prompt string) (string, error) {
			fmt.Print(prompt)
			var value string
			fmt.Scanln(&value)
			return strings.TrimSpace(value), nil
		}
	}

	apiKey, errPrompt := promptFn("Enter Command Code API Key (user_...): ")
	if errPrompt != nil {
		return nil, fmt.Errorf("commandcode: failed to read API key: %w", errPrompt)
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("commandcode: API key cannot be empty")
	}

	authSvc := commandcodeauth.NewCommandCodeAuth()
	whoamiCtx, cancelWhoami := context.WithTimeout(ctx, 30*time.Second)
	defer cancelWhoami()
	whoami, errWhoami := authSvc.Whoami(whoamiCtx, apiKey)
	if errWhoami != nil {
		return nil, fmt.Errorf("commandcode: credential validation failed: %w", errWhoami)
	}
	userName := ""
	userID := ""
	if whoami.User != nil {
		userName = whoami.User.UserName
		userID = whoami.User.ID
	}

	ts := &commandcodeauth.CommandCodeTokenStorage{
		APIKey:          apiKey,
		UserID:          userID,
		UserName:        userName,
		KeyName:         "cli-proxy-api",
		AuthenticatedAt: time.Now().UTC().Format(time.RFC3339),
		Type:            "commandcode",
	}

	fileName := commandcodeauth.CredentialFileName(userName)
	label := userName
	if label == "" {
		label = userID
	}
	if label == "" {
		label = "Command Code"
	}

	metadata := map[string]any{
		"type":      "commandcode",
		"api_key":   apiKey,
		"user_id":   userID,
		"user_name": userName,
	}
	if userName != "" {
		metadata["email"] = userName
	}

	fmt.Println("Command Code authentication successful")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    label,
		Storage:  ts,
		Metadata: metadata,
	}, nil
}
