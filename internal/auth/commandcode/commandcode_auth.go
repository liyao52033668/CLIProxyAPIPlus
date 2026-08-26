// Package commandcode provides authentication and token management
// functionality for Command Code services.
package commandcode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const (
	// BaseURL is the base URL for the Command Code API.
	BaseURL = "https://api.commandcode.ai"

	// StudioBaseURL is the base URL for the Command Code studio web app.
	StudioBaseURL = "https://commandcode.ai"

	// Header constants matching official command-code CLI requirements.
	CLIEnvHeader        = "x-cli-environment"
	CLIEnvProd          = "production"
	CLIVersionHeader    = "x-command-code-version"
	DefaultCLIVersion   = "1.33.0"
	ProjectSlugHeader   = "x-project-slug"
	DefaultProjectSlug  = "root"
	TasteLearningHeader = "x-taste-learning"
	CoFlagHeader        = "x-co-flag"
	SessionIDHeader     = "x-session-id"
)

// WhoamiUser represents the authenticated user from /alpha/whoami.
type WhoamiUser struct {
	ID       string `json:"id"`
	UserName string `json:"userName"`
	Email    string `json:"email"`
}

// WhoamiOrg represents an organization from /alpha/whoami.
type WhoamiOrg struct {
	ID    string `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
}

// WhoamiResponse represents the response from the /alpha/whoami endpoint.
type WhoamiResponse struct {
	Success bool        `json:"success"`
	User    *WhoamiUser `json:"user"`
	Org     *WhoamiOrg  `json:"org"`
	Orgs    []WhoamiOrg `json:"orgs"`
}

// CommandCodeAuth provides methods for handling Command Code authentication.
type CommandCodeAuth struct {
	client *http.Client
}

// NewCommandCodeAuth creates a new instance of CommandCodeAuth.
func NewCommandCodeAuth() *CommandCodeAuth {
	return &CommandCodeAuth{
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// Whoami validates the API key and returns account information.
func (k *CommandCodeAuth) Whoami(ctx context.Context, apiKey string) (*WhoamiResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, BaseURL+"/alpha/whoami", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create whoami request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set(CLIEnvHeader, CLIEnvProd)

	resp, err := k.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call whoami: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("invalid API key")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("whoami failed: status %d", resp.StatusCode)
	}

	var data WhoamiResponse
	if err = json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("failed to decode whoami response: %w", err)
	}
	return &data, nil
}

// ValidateAPIKey reports whether the given API key is usable.
func (k *CommandCodeAuth) ValidateAPIKey(ctx context.Context, apiKey string) error {
	_, err := k.Whoami(ctx, apiKey)
	return err
}
