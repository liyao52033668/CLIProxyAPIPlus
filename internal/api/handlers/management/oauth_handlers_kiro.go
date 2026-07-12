package management

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	// "github.com/router-for-me/CLIProxyAPI/v7/internal/browser"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func (h *Handler) RequestKiroToken(c *gin.Context) {
	ctx := context.Background()

	// Get the login method from query parameter (default: google for social auth flow)
	method := strings.ToLower(strings.TrimSpace(c.Query("method")))
	if method == "" {
		method = "aws"
	}

	fmt.Println("Initializing Kiro authentication...")

	state := fmt.Sprintf("kiro-%d", time.Now().UnixNano())

	switch method {
	case "aws", "builder-id":
		RegisterOAuthSession(state, "kiro")

		// AWS Builder ID uses device code flow (no callback needed)
		go func() {
			ssoClient := kiroauth.NewSSOOIDCClient(h.cfg)

			// Step 1: Register client
			fmt.Println("Registering client...")
			regResp, errRegister := ssoClient.RegisterClient(ctx)
			if errRegister != nil {
				log.Errorf("Failed to register client: %v", errRegister)
				SetOAuthSessionError(state, "Failed to register client")
				return
			}

			// Step 2: Start device authorization
			fmt.Println("Starting device authorization...")
			authResp, errAuth := ssoClient.StartDeviceAuthorization(ctx, regResp.ClientID, regResp.ClientSecret)
			if errAuth != nil {
				log.Errorf("Failed to start device auth: %v", errAuth)
				SetOAuthSessionError(state, "Failed to start device authorization")
				return
			}

			// Store the verification URL for the frontend to display.
			// Using "|" as separator because URLs contain ":".
			SetOAuthSessionError(state, "device_code|"+authResp.VerificationURIComplete+"|"+authResp.UserCode)

			// Step 3: Poll for token
			fmt.Println("Waiting for authorization...")
			interval := 5 * time.Second
			if authResp.Interval > 0 {
				interval = time.Duration(authResp.Interval) * time.Second
			}
			deadline := time.Now().Add(time.Duration(authResp.ExpiresIn) * time.Second)

			for time.Now().Before(deadline) {
				select {
				case <-ctx.Done():
					SetOAuthSessionError(state, "Authorization cancelled")
					return
				case <-time.After(interval):
					tokenResp, errToken := ssoClient.CreateToken(ctx, regResp.ClientID, regResp.ClientSecret, authResp.DeviceCode)
					if errToken != nil {
						errStr := errToken.Error()
						if strings.Contains(errStr, "authorization_pending") {
							continue
						}
						if strings.Contains(errStr, "slow_down") {
							interval += 5 * time.Second
							continue
						}
						log.Errorf("Token creation failed: %v", errToken)
						SetOAuthSessionError(state, "Token creation failed")
						return
					}

					// Success! Save the token
					expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
					email := kiroauth.ExtractEmailFromJWT(tokenResp.AccessToken)

					idPart := kiroauth.SanitizeEmailForFilename(email)
					if idPart == "" {
						idPart = fmt.Sprintf("%d", time.Now().UnixNano()%100000)
					}

					now := time.Now()
					// Use format: provider-email.json
					fileName := fmt.Sprintf("kiro-%s.json", idPart)

					record := &coreauth.Auth{
						ID:       fileName,
						Provider: "kiro",
						FileName: fileName,
						Metadata: map[string]any{
							"type":          "kiro",
							"access_token":  tokenResp.AccessToken,
							"refresh_token": tokenResp.RefreshToken,
							"expires_at":    expiresAt.Format(time.RFC3339),
							"auth_method":   "builder-id",
							"provider":      "AWS",
							"client_id":     regResp.ClientID,
							"client_secret": regResp.ClientSecret,
							"email":         email,
							"last_refresh":  now.Format(time.RFC3339),
						},
					}

					savedPath, errSave := h.saveTokenRecord(ctx, record)
					if errSave != nil {
						log.Errorf("Failed to save authentication tokens: %v", errSave)
						SetOAuthSessionError(state, "Failed to save authentication tokens")
						return
					}

					fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
					if email != "" {
						fmt.Printf("Authenticated as: %s\n", email)
					}
					CompleteOAuthSession(state)
					return
				}
			}

			SetOAuthSessionError(state, "Authorization timed out")
		}()

		// Return immediately with the state for polling
		c.JSON(http.StatusOK, gin.H{"status": "ok", "state": state, "method": "device_code"})

	case "google", "github":
		RegisterOAuthSession(state, "kiro")

		// Social auth uses protocol handler - for WEB UI we use a callback forwarder
		provider := "Google"
		if method == "github" {
			provider = "Github"
		}

		isWebUI, forwarder, errForwarder := h.startWebUICallbackForwarderIfNeeded(c, kiroCallbackPort, "kiro", "/kiro/callback")
		if errForwarder != nil {
			log.WithError(errForwarder).Error("failed to start kiro callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
			return
		}

		// Generate PKCE codes outside goroutine so we can return auth URL immediately
		codeVerifier, codeChallenge, errPKCE := generateKiroPKCE()
		if errPKCE != nil {
			log.Errorf("Failed to generate PKCE: %v", errPKCE)
			SetOAuthSessionError(state, "Failed to generate PKCE")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate auth params"})
			return
		}

		// Build login URL
		authURL := fmt.Sprintf("%s/login?idp=%s&redirect_uri=%s&code_challenge=%s&code_challenge_method=S256&state=%s&prompt=select_account",
			"https://prod.us-east-1.auth.desktop.kiro.dev",
			provider,
			url.QueryEscape(kiroauth.KiroRedirectURI),
			codeChallenge,
			state,
		)

		// Store auth URL for frontend polling
		SetOAuthSessionError(state, "auth_url|"+authURL)

		// Start background goroutine to wait for callback
		socialClient := kiroauth.NewSocialAuthClient(h.cfg)
		go func() {
			if isWebUI {
				defer stopCallbackForwarderInstance(kiroCallbackPort, forwarder)
			}

			callbackPayload, errWait := waitForOAuthCallbackFile(h.cfg.AuthDir, "kiro", state, defaultOAuthCallbackWait)
			if errWait != nil {
				if errors.Is(errWait, errOAuthSessionNotPending) {
					return
				}
				log.Error("oauth flow timed out")
				return
			}
			if errValidate := validateOAuthCallbackPayload("kiro", state, callbackPayload, true); errValidate != nil {
				log.Errorf("Authentication failed: %v", errValidate)
				return
			}
			code := callbackPayload.Code

			// Exchange code for tokens
			tokenReq := &kiroauth.CreateTokenRequest{
				Code:         code,
				CodeVerifier: codeVerifier,
				RedirectURI:  kiroauth.KiroRedirectURI,
			}

			tokenResp, errToken := socialClient.CreateToken(ctx, tokenReq)
			if errToken != nil {
				log.Errorf("Failed to exchange code for tokens: %v", errToken)
				SetOAuthSessionError(state, "Failed to exchange code for tokens")
				return
			}

			// Save the token
			expiresIn := tokenResp.ExpiresIn
			if expiresIn <= 0 {
				expiresIn = 3600
			}
			expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)
			email := kiroauth.ExtractEmailFromJWT(tokenResp.AccessToken)

			idPart := kiroauth.SanitizeEmailForFilename(email)
			if idPart == "" {
				idPart = fmt.Sprintf("%d", time.Now().UnixNano()%100000)
			}

			now := time.Now()
			fileName := fmt.Sprintf("kiro-%s-%s.json", strings.ToLower(provider), idPart)

			record := &coreauth.Auth{
				ID:       fileName,
				Provider: "kiro",
				FileName: fileName,
				Metadata: map[string]any{
					"type":          "kiro",
					"access_token":  tokenResp.AccessToken,
					"refresh_token": tokenResp.RefreshToken,
					"profile_arn":   tokenResp.ProfileArn,
					"expires_at":    expiresAt.Format(time.RFC3339),
					"auth_method":   "social",
					"provider":      provider,
					"email":         email,
					"last_refresh":  now.Format(time.RFC3339),
				},
			}

			savedPath, errSave := h.saveTokenRecord(ctx, record)
			if errSave != nil {
				log.Errorf("Failed to save authentication tokens: %v", errSave)
				SetOAuthSessionError(state, "Failed to save authentication tokens")
				return
			}

			fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
			if email != "" {
				fmt.Printf("Authenticated as: %s\n", email)
			}
			CompleteOAuthSession(state)
		}()

		c.JSON(http.StatusOK, gin.H{"status": "ok", "url": authURL, "state": state})

	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid method, use 'aws', 'google', or 'github'"})
	}
}

// generateKiroPKCE generates PKCE code verifier and challenge for Kiro OAuth.
func generateKiroPKCE() (verifier, challenge string, err error) {
	b := make([]byte, 32)
	if _, errRead := io.ReadFull(rand.Reader, b); errRead != nil {
		return "", "", fmt.Errorf("failed to generate random bytes: %w", errRead)
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)

	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])

	return verifier, challenge, nil
}

// func (h *Handler) RequestKiroToken(c *gin.Context) {
// 	ctx := context.Background()

// 	// Get the login method from query parameter
// 	method := strings.ToLower(strings.TrimSpace(c.Query("method")))
// 	isWebUI := isWebUIRequest(c)

// 	log.Infof("=== Kiro auth request START ===")
// 	log.Infof("method=%q, isWebUI=%v", method, isWebUI)

// 	if method == "" {
// 		if isWebUI {
// 			method = "google"
// 		} else {
// 			method = "aws"
// 		}
// 	}

// 	log.Infof("method determined: %q", method)
// 	log.Infof("=== Kiro auth request END ===")

// 	state := fmt.Sprintf("kiro-%d", time.Now().UnixNano())

// 	switch method {
// 	case "aws", "builder-id":
// 		RegisterOAuthSession(state, "kiro")

// 		// AWS Builder ID uses device code flow (no callback needed)
// 		go func() {
// 			ssoClient := kiroauth.NewSSOOIDCClient(h.cfg)

// 			// Step 1: Register client
// 			fmt.Println("Registering client...")
// 			regResp, errRegister := ssoClient.RegisterClient(ctx)
// 			if errRegister != nil {
// 				log.Errorf("Failed to register client: %v", errRegister)
// 				SetOAuthSessionError(state, "Failed to register client")
// 				return
// 			}

// 			// Step 2: Start device authorization
// 			fmt.Println("Starting device authorization...")
// 			authResp, errAuth := ssoClient.StartDeviceAuthorization(ctx, regResp.ClientID, regResp.ClientSecret)
// 			if errAuth != nil {
// 				log.Errorf("Failed to start device auth: %v", errAuth)
// 				SetOAuthSessionError(state, "Failed to start device authorization")
// 				return
// 			}

// 			// Store the verification URL for the frontend to display.
// 			// Using "|" as separator because URLs contain ":".
// 			SetOAuthSessionError(state, "device_code|"+authResp.VerificationURIComplete+"|"+authResp.UserCode)

// 			// Step 3: Poll for token
// 			fmt.Println("Waiting for authorization...")
// 			interval := 5 * time.Second
// 			if authResp.Interval > 0 {
// 				interval = time.Duration(authResp.Interval) * time.Second
// 			}
// 			deadline := time.Now().Add(time.Duration(authResp.ExpiresIn) * time.Second)

// 			for time.Now().Before(deadline) {
// 				select {
// 				case <-ctx.Done():
// 					SetOAuthSessionError(state, "Authorization cancelled")
// 					return
// 				case <-time.After(interval):
// 					tokenResp, errToken := ssoClient.CreateToken(ctx, regResp.ClientID, regResp.ClientSecret, authResp.DeviceCode)
// 					if errToken != nil {
// 						errStr := errToken.Error()
// 						if strings.Contains(errStr, "authorization_pending") {
// 							continue
// 						}
// 						if strings.Contains(errStr, "slow_down") {
// 							interval += 5 * time.Second
// 							continue
// 						}
// 						log.Errorf("Token creation failed: %v", errToken)
// 						SetOAuthSessionError(state, "Token creation failed")
// 						return
// 					}

// 					// Success! Save the token
// 					expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
// 					email := kiroauth.ExtractEmailFromJWT(tokenResp.AccessToken)

// 					idPart := kiroauth.SanitizeEmailForFilename(email)
// 					if idPart == "" {
// 						idPart = fmt.Sprintf("%d", time.Now().UnixNano()%100000)
// 					}

// 					now := time.Now()
// 					fileName := fmt.Sprintf("kiro-aws-%s.json", idPart)

// 					record := &coreauth.Auth{
// 						ID:       fileName,
// 						Provider: "kiro",
// 						FileName: fileName,
// 						Metadata: map[string]any{
// 							"type":          "kiro",
// 							"access_token":  tokenResp.AccessToken,
// 							"refresh_token": tokenResp.RefreshToken,
// 							"expires_at":    expiresAt.Format(time.RFC3339),
// 							"auth_method":   "builder-id",
// 							"provider":      "AWS",
// 							"client_id":     regResp.ClientID,
// 							"client_secret": regResp.ClientSecret,
// 							"email":         email,
// 							"last_refresh":  now.Format(time.RFC3339),
// 						},
// 					}

// 					savedPath, errSave := h.saveTokenRecord(ctx, record)
// 					if errSave != nil {
// 						log.Errorf("Failed to save authentication tokens: %v", errSave)
// 						SetOAuthSessionError(state, "Failed to save authentication tokens")
// 						return
// 					}

// 					fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
// 					if email != "" {
// 						fmt.Printf("Authenticated as: %s\n", email)
// 					}
// 					CompleteOAuthSession(state)
// 					return
// 				}
// 			}

// 			SetOAuthSessionError(state, "Authorization timed out")
// 		}()

// 		// Return immediately with the state for polling
// 		c.JSON(http.StatusOK, gin.H{"status": "ok", "state": state, "method": "device_code"})

// 	case "google", "github":
// 		RegisterOAuthSession(state, "kiro")

// 		// Social auth uses protocol handler - for WEB UI we use a callback forwarder
// 		provider := "Google"
// 		if method == "github" {
// 			provider = "Github"
// 		}

// 		isWebUI := isWebUIRequest(c)
// 		var forwarder *callbackForwarder
// 		if isWebUI {
// 			targetURL, errTarget := h.managementCallbackURL("/kiro/callback")
// 			if errTarget != nil {
// 				log.WithError(errTarget).Error("failed to compute kiro callback target")
// 				c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
// 				return
// 			}
// 			var errStart error
// 			if forwarder, errStart = startCallbackForwarder(kiroCallbackPort, "kiro", targetURL); errStart != nil {
// 				log.WithError(errStart).Error("failed to start kiro callback forwarder")
// 				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
// 				return
// 			}
// 		}

// 		// Generate PKCE codes in main thread to return auth_url immediately
// 		socialClient := kiroauth.NewSocialAuthClient(h.cfg)
// 		codeVerifier, codeChallenge, errPKCE := generateKiroPKCE()
// 		if errPKCE != nil {
// 			log.Errorf("Failed to generate PKCE: %v", errPKCE)
// 			SetOAuthSessionError(state, "Failed to generate PKCE")
// 			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate auth params"})
// 			return
// 		}

// 		authURL := fmt.Sprintf("%s?redirect_uri=%s&code_challenge=%s&code_challenge_method=S256&state=%s&redirect_from=KiroIDE",
// 			"https://app.kiro.dev/signin",
// 			url.QueryEscape(fmt.Sprintf("http://localhost:%d/redirect", kiroCallbackPort)),
// 			codeChallenge,
// 			state,
// 		)

// 		log.Infof("Kiro auth URL built: %s for state: %s", authURL, state)

// 		// Store auth URL for frontend immediately
// 		SetOAuthSessionError(state, "auth_url|"+authURL)
// 		log.Infof("Kiro auth URL stored for state: %s", state)

// 		log.Infof("Kiro auth: about to start goroutine for state: %s, isWebUI: %v", state, isWebUI)

// 		go func() {
// 			log.Infof("Kiro social auth goroutine STARTED for state: %s", state)
// 			if isWebUI {
// 				defer stopCallbackForwarderInstance(kiroCallbackPort, forwarder)
// 			}

// 			log.Infof("Kiro social auth goroutine started for state: %s, provider: %s", state, provider)

// 			// Open browser with the auth URL
// 			if err := browser.OpenURL(authURL); err != nil {
// 				log.Errorf("Failed to open browser for Kiro auth: %v", err)
// 			} else {
// 				log.Infof("Successfully opened browser for Kiro auth: %s", authURL)
// 			}

// 			// Wait for callback file
// 			waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-kiro-%s.oauth", state))
// 			deadline := time.Now().Add(5 * time.Minute)

// 			for {
// 				if time.Now().After(deadline) {
// 					log.Error("oauth flow timed out")
// 					SetOAuthSessionError(state, "OAuth flow timed out")
// 					return
// 				}
// 				if data, errRead := os.ReadFile(waitFile); errRead == nil {
// 					var m map[string]string
// 					_ = json.Unmarshal(data, &m)
// 					_ = os.Remove(waitFile)
// 					if errStr := m["error"]; errStr != "" {
// 						log.Errorf("Authentication failed: %s", errStr)
// 						SetOAuthSessionError(state, "Authentication failed")
// 						return
// 					}
// 					if m["state"] != state {
// 						log.Errorf("State mismatch")
// 						SetOAuthSessionError(state, "State mismatch")
// 						return
// 					}
// 					code := m["code"]
// 					if code == "" {
// 						log.Error("No authorization code received")
// 						SetOAuthSessionError(state, "No authorization code received")
// 						return
// 					}

// 					// Exchange code for tokens
// 					tokenReq := &kiroauth.CreateTokenRequest{
// 						Code:         code,
// 						CodeVerifier: codeVerifier,
// 						RedirectURI:  kiroauth.KiroRedirectURI,
// 					}

// 					tokenResp, errToken := socialClient.CreateToken(ctx, tokenReq)
// 					if errToken != nil {
// 						log.Errorf("Failed to exchange code for tokens: %v", errToken)
// 						SetOAuthSessionError(state, "Failed to exchange code for tokens")
// 						return
// 					}

// 					// Save the token
// 					expiresIn := tokenResp.ExpiresIn
// 					if expiresIn <= 0 {
// 						expiresIn = 3600
// 					}
// 					expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)
// 					email := kiroauth.ExtractEmailFromJWT(tokenResp.AccessToken)

// 					idPart := kiroauth.SanitizeEmailForFilename(email)
// 					if idPart == "" {
// 						idPart = fmt.Sprintf("%d", time.Now().UnixNano()%100000)
// 					}

// 					now := time.Now()
// 					fileName := fmt.Sprintf("kiro-%s-%s.json", strings.ToLower(provider), idPart)

// 					record := &coreauth.Auth{
// 						ID:       fileName,
// 						Provider: "kiro",
// 						FileName: fileName,
// 						Metadata: map[string]any{
// 							"type":          "kiro",
// 							"access_token":  tokenResp.AccessToken,
// 							"refresh_token": tokenResp.RefreshToken,
// 							"profile_arn":   tokenResp.ProfileArn,
// 							"expires_at":    expiresAt.Format(time.RFC3339),
// 							"auth_method":   "social",
// 							"provider":      provider,
// 							"email":         email,
// 							"last_refresh":  now.Format(time.RFC3339),
// 						},
// 					}

// 					savedPath, errSave := h.saveTokenRecord(ctx, record)
// 					if errSave != nil {
// 						log.Errorf("Failed to save authentication tokens: %v", errSave)
// 						SetOAuthSessionError(state, "Failed to save authentication tokens")
// 						return
// 					}

// 					fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
// 					if email != "" {
// 						fmt.Printf("Authenticated as: %s\n", email)
// 					}
// 					CompleteOAuthSession(state)
// 					return
// 				}
// 				time.Sleep(500 * time.Millisecond)
// 			}
// 		}()

// 		c.JSON(http.StatusOK, gin.H{"status": "ok", "state": state, "method": "social"})

// 	default:
// 		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid method, use 'aws', 'google', or 'github'"})
// 	}
// }

// // generateKiroPKCE generates PKCE code verifier and challenge for Kiro OAuth.
// func generateKiroPKCE() (verifier, challenge string, err error) {
// 	b := make([]byte, 32)
// 	if _, errRead := io.ReadFull(rand.Reader, b); errRead != nil {
// 		return "", "", fmt.Errorf("failed to generate random bytes: %w", errRead)
// 	}
// 	verifier = base64.RawURLEncoding.EncodeToString(b)

// 	h := sha256.Sum256([]byte(verifier))
// 	challenge = base64.RawURLEncoding.EncodeToString(h[:])

// 	return verifier, challenge, nil
// }
