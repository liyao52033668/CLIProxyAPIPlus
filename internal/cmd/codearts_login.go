package cmd

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	log "github.com/sirupsen/logrus"
)

func DoCodeArtsLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	manager := newAuthManager()
	authOpts := &sdkAuth.LoginOptions{
		NoBrowser:    options.NoBrowser,
		CallbackPort: options.CallbackPort,
		Metadata:     map[string]string{},
	}

	record, savedPath, err := manager.Login(context.Background(), "codearts", cfg, authOpts)
	if err != nil {
		log.Errorf("CodeArts authentication failed: %v", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Authenticated as %s\n", record.Label)
	}
	fmt.Println("CodeArts authentication successful!")
}

// DoCodeArtsAKSKLogin authorizes CodeArts with permanent HuaweiCloud IAM access
// keys. Unlike the OAuth flow, the resulting credentials do not expire after 24
// hours. Keys are read from the CODEARTS_CLI_AK / CODEARTS_CLI_SK environment
// variables when present, otherwise entered interactively.
func DoCodeArtsAKSKLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	promptFn := options.Prompt
	if promptFn == nil {
		promptFn = defaultProjectPrompt()
	}

	manager := newAuthManager()
	authOpts := &sdkAuth.LoginOptions{
		Metadata: map[string]string{
			"login_mode": "aksk",
		},
		Prompt: promptFn,
	}

	record, savedPath, err := manager.Login(context.Background(), "codearts", cfg, authOpts)
	if err != nil {
		log.Errorf("CodeArts AK/SK authentication failed: %v", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Authenticated as %s\n", record.Label)
	}
	fmt.Println("CodeArts AK/SK authentication successful!")
}
