package management

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kilo"

	// "github.com/router-for-me/CLIProxyAPI/v7/internal/browser"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func (h *Handler) RequestKiloToken(c *gin.Context) {
	ctx := context.Background()

	fmt.Println("Initializing Kilo authentication...")

	state := fmt.Sprintf("kil-%d", time.Now().UnixNano())
	kilocodeAuth := kilo.NewKiloAuth()

	resp, err := kilocodeAuth.InitiateDeviceFlow(ctx)
	if err != nil {
		log.Errorf("Failed to initiate device flow: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to initiate device flow"})
		return
	}

	RegisterOAuthSession(state, "kilo")

	go func() {
		fmt.Printf("Please visit %s and enter code: %s\n", resp.VerificationURL, resp.Code)

		status, err := kilocodeAuth.PollForToken(ctx, resp.Code)
		if err != nil {
			SetOAuthSessionError(state, "Authentication failed")
			fmt.Printf("Authentication failed: %v\n", err)
			return
		}

		profile, err := kilocodeAuth.GetProfile(ctx, status.Token)
		if err != nil {
			log.Warnf("Failed to fetch profile: %v", err)
			profile = &kilo.Profile{Email: status.UserEmail}
		}

		var orgID string
		if len(profile.Orgs) > 0 {
			orgID = profile.Orgs[0].ID
		}

		defaults, err := kilocodeAuth.GetDefaults(ctx, status.Token, orgID)
		if err != nil {
			defaults = &kilo.Defaults{}
		}

		ts := &kilo.KiloTokenStorage{
			Token:          status.Token,
			OrganizationID: orgID,
			Model:          defaults.Model,
			Email:          status.UserEmail,
			Type:           "kilo",
		}

		fileName := kilo.CredentialFileName(status.UserEmail)
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "kilo",
			FileName: fileName,
			Storage:  ts,
			Metadata: map[string]any{
				"email":           status.UserEmail,
				"organization_id": orgID,
				"model":           defaults.Model,
			},
		}

		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}

		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		completeOAuthSuccess(state, "kilo")
	}()

	c.JSON(200, gin.H{
		"status":           "ok",
		"url":              resp.VerificationURL,
		"state":            state,
		"user_code":        resp.Code,
		"verification_uri": resp.VerificationURL,
	})
}

// RequestCursorToken initiates the Cursor PKCE authentication flow.
// Supports multiple accounts via ?label=xxx query parameter.
// The user opens the returned URL in a browser, logs in, and the server polls
// until the authentication completes.

// func (h *Handler) RequestKiloToken(c *gin.Context) {
// 	ctx := context.Background()

// 	fmt.Println("Initializing Kilo authentication...")

// 	state := fmt.Sprintf("kil-%d", time.Now().UnixNano())
// 	kilocodeAuth := kilo.NewKiloAuth()

// 	resp, err := kilocodeAuth.InitiateDeviceFlow(ctx)
// 	if err != nil {
// 		log.Errorf("Failed to initiate device flow: %v", err)
// 		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to initiate device flow"})
// 		return
// 	}

// 	RegisterOAuthSession(state, "kilo")

// 	go func() {
// 		fmt.Printf("Please visit %s and enter code: %s\n", resp.VerificationURL, resp.Code)

// 		status, err := kilocodeAuth.PollForToken(ctx, resp.Code)
// 		if err != nil {
// 			SetOAuthSessionError(state, "Authentication failed")
// 			fmt.Printf("Authentication failed: %v\n", err)
// 			return
// 		}

// 		profile, err := kilocodeAuth.GetProfile(ctx, status.Token)
// 		if err != nil {
// 			log.Warnf("Failed to fetch profile: %v", err)
// 			profile = &kilo.Profile{Email: status.UserEmail}
// 		}

// 		var orgID string
// 		if len(profile.Orgs) > 0 {
// 			orgID = profile.Orgs[0].ID
// 		}

// 		defaults, err := kilocodeAuth.GetDefaults(ctx, status.Token, orgID)
// 		if err != nil {
// 			defaults = &kilo.Defaults{}
// 		}

// 		ts := &kilo.KiloTokenStorage{
// 			Token:          status.Token,
// 			OrganizationID: orgID,
// 			Model:          defaults.Model,
// 			Email:          status.UserEmail,
// 			Type:           "kilo",
// 		}

// 		fileName := kilo.CredentialFileName(status.UserEmail)
// 		record := &coreauth.Auth{
// 			ID:       fileName,
// 			Provider: "kilo",
// 			FileName: fileName,
// 			Storage:  ts,
// 			Metadata: map[string]any{
// 				"email":           status.UserEmail,
// 				"organization_id": orgID,
// 				"model":           defaults.Model,
// 			},
// 		}

// 		savedPath, errSave := h.saveTokenRecord(ctx, record)
// 		if errSave != nil {
// 			log.Errorf("Failed to save authentication tokens: %v", errSave)
// 			SetOAuthSessionError(state, "Failed to save authentication tokens")
// 			return
// 		}

// 		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
// 		CompleteOAuthSession(state)
// 		CompleteOAuthSessionsByProvider("kilo")
// 	}()

// 	c.JSON(200, gin.H{
// 		"status":           "ok",
// 		"url":              resp.VerificationURL,
// 		"state":            state,
// 		"user_code":        resp.Code,
// 		"verification_uri": resp.VerificationURL,
// 	})
// }

const kiroCallbackPort = 9876
