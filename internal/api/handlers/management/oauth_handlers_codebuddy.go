package management

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codebuddy"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codebuddy_ai"

	// "github.com/router-for-me/CLIProxyAPI/v7/internal/browser"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func (h *Handler) RequestCodeBuddyToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing CodeBuddy authentication...")

	authSvc := codebuddy.NewCodeBuddyAuth(h.cfg)
	authState, errState := authSvc.FetchAuthState(ctx)
	if errState != nil {
		log.Errorf("Failed to fetch CodeBuddy auth state: %v", errState)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch auth url"})
		return
	}

	state := strings.TrimSpace(authState.State)
	if state == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid auth state"})
		return
	}

	RegisterOAuthSession(state, "codebuddy")

	go func() {
		tokens, errPoll := authSvc.PollForToken(ctx, state)
		if errPoll != nil {
			SetOAuthSessionError(state, "Authentication failed: "+errPoll.Error())
			log.Errorf("CodeBuddy authentication failed: %v", errPoll)
			return
		}
		if tokens == nil || strings.TrimSpace(tokens.AccessToken) == "" {
			SetOAuthSessionError(state, "Authentication failed")
			log.Errorf("CodeBuddy authentication failed: empty token")
			return
		}

		uid := strings.TrimSpace(tokens.UserID)
		if uid == "" {
			uid = fmt.Sprintf("%d", time.Now().UnixMilli())
		}

		fileName := fmt.Sprintf("codebuddy-%s.json", uid)
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "codebuddy",
			FileName: fileName,
			Label:    uid,
			Storage:  tokens,
			Metadata: map[string]any{
				"type":          "codebuddy",
				"access_token":  strings.TrimSpace(tokens.AccessToken),
				"refresh_token": strings.TrimSpace(tokens.RefreshToken),
				"expires_in":    tokens.ExpiresIn,
				"token_type":    strings.TrimSpace(tokens.TokenType),
				"domain":        strings.TrimSpace(tokens.Domain),
				"user_id":       strings.TrimSpace(tokens.UserID),
				"timestamp":     time.Now().UnixMilli(),
			},
		}

		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save CodeBuddy token: %v", errSave)
			SetOAuthSessionError(state, "Failed to save token")
			return
		}

		completeOAuthSuccess(state, "codebuddy")
		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		fmt.Println("You can now use CodeBuddy services through this CLI")
	}()

	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": authState.AuthURL, "state": state})
}

func (h *Handler) RequestCodeBuddyAIToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing CodeBuddy AI authentication...")

	authSvc := codebuddy_ai.NewCodeBuddyAIAuth(h.cfg)
	authState, errState := authSvc.FetchAuthState(ctx)
	if errState != nil {
		log.Errorf("Failed to fetch CodeBuddy AI auth state: %v", errState)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch auth url"})
		return
	}

	state := strings.TrimSpace(authState.State)
	if state == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid auth state"})
		return
	}

	RegisterOAuthSession(state, "codebuddy-ai")

	go func() {
		tokens, errPoll := authSvc.PollForToken(ctx, state)
		if errPoll != nil {
			SetOAuthSessionError(state, "Authentication failed: "+errPoll.Error())
			log.Errorf("CodeBuddy AI authentication failed: %v", errPoll)
			return
		}
		if tokens == nil || strings.TrimSpace(tokens.AccessToken) == "" {
			SetOAuthSessionError(state, "Authentication failed")
			log.Errorf("CodeBuddy AI authentication failed: empty token")
			return
		}

		uid := strings.TrimSpace(tokens.UserID)
		if uid == "" {
			uid = fmt.Sprintf("%d", time.Now().UnixMilli())
		}

		fileName := fmt.Sprintf("codebuddy-ai-%s.json", uid)
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "codebuddy-ai",
			FileName: fileName,
			Label:    uid,
			Storage:  tokens,
			Metadata: map[string]any{
				"type":          "codebuddy-ai",
				"access_token":  strings.TrimSpace(tokens.AccessToken),
				"refresh_token": strings.TrimSpace(tokens.RefreshToken),
				"expires_in":    tokens.ExpiresIn,
				"token_type":    strings.TrimSpace(tokens.TokenType),
				"domain":        strings.TrimSpace(tokens.Domain),
				"user_id":       strings.TrimSpace(tokens.UserID),
				"timestamp":     time.Now().UnixMilli(),
			},
		}

		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save CodeBuddy AI token: %v", errSave)
			SetOAuthSessionError(state, "Failed to save token")
			return
		}

		completeOAuthSuccess(state, "codebuddy-ai")
		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		fmt.Println("You can now use CodeBuddy AI services through this CLI")
	}()

	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": authState.AuthURL, "state": state})
}
