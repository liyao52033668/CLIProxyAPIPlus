package management

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type oauthCallbackRequest struct {
	Provider    string `json:"provider"`
	RedirectURL string `json:"redirect_url"`
	Code        string `json:"code"`
	State       string `json:"state"`
	Error       string `json:"error"`
	Token       string `json:"token"`
	Auth        string `json:"auth"`
}

func (h *Handler) PostOAuthCallback(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "handler not initialized"})
		return
	}

	var req oauthCallbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid body"})
		return
	}

	canonicalProvider, err := NormalizeOAuthProvider(req.Provider)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "unsupported provider"})
		return
	}

	state := strings.TrimSpace(req.State)
	code := strings.TrimSpace(req.Code)
	errMsg := strings.TrimSpace(req.Error)
	token := strings.TrimSpace(req.Token)
	auth := strings.TrimSpace(req.Auth)

	if rawRedirect := strings.TrimSpace(req.RedirectURL); rawRedirect != "" {
		if strings.HasPrefix(rawRedirect, "qoder://") {
			u, errParse := url.Parse(rawRedirect)
			if errParse == nil {
				q := u.Query()
				if state == "" {
					state = strings.TrimSpace(q.Get("state"))
				}
				if token == "" {
					token = strings.TrimSpace(q.Get("token"))
					if token == "" {
						token = strings.TrimSpace(q.Get("tokenString"))
					}
				}
				if auth == "" {
					auth = strings.TrimSpace(q.Get("auth"))
				}
			}
		} else if strings.HasPrefix(rawRedirect, "https://qoder.com?") {
			qoderPart := rawRedirect[len("https://qoder.com?"):]
			if strings.HasPrefix(qoderPart, "qoder://") {
				u, errParse := url.Parse(qoderPart)
				if errParse == nil {
					q := u.Query()
					if state == "" {
						state = strings.TrimSpace(q.Get("state"))
					}
					if token == "" {
						token = strings.TrimSpace(q.Get("token"))
						if token == "" {
							token = strings.TrimSpace(q.Get("tokenString"))
						}
					}
					if auth == "" {
						auth = strings.TrimSpace(q.Get("auth"))
					}
				}
			}
		} else {
			u, errParse := url.Parse(rawRedirect)
			if errParse != nil {
				c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid redirect_url"})
				return
			}
			q := u.Query()
			if state == "" {
				state = strings.TrimSpace(q.Get("state"))
			}
			if code == "" {
				code = strings.TrimSpace(q.Get("code"))
				if code == "" {
					code = strings.TrimSpace(q.Get("apiKey"))
				}
				if code == "" {
					code = strings.TrimSpace(q.Get("api_key"))
				}
			}
			if errMsg == "" {
				errMsg = strings.TrimSpace(q.Get("error"))
				if errMsg == "" {
					errMsg = strings.TrimSpace(q.Get("error_description"))
				}
			}
			if token == "" {
				token = strings.TrimSpace(q.Get("token"))
				if token == "" {
					token = strings.TrimSpace(q.Get("tokenString"))
				}
				if token == "" {
					token = strings.TrimSpace(q.Get("apiKey"))
				}
				if token == "" {
					token = strings.TrimSpace(q.Get("api_key"))
				}
			}
			if auth == "" {
				auth = strings.TrimSpace(q.Get("auth"))
			}
		}
	}

	if state == "" && canonicalProvider == "qoder" {
		log.Warnf("Qoder callback without state - will try to match by provider")
	} else if state == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "state is required"})
		return
	} else if err := ValidateOAuthState(state); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid state"})
		return
	}

	// CodeArts special handling: the callback URL carries secret + redirect (with ticket_id),
	// not code. Complete the flow synchronously via ticket polling.
	if canonicalProvider == "codearts" {
		if err := h.finishCodeArtsFromCallback(c, state, req.RedirectURL); err != nil {
			log.WithError(err).Warnf("CodeArts OAuth: callback completion failed for state %s", state)
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": err.Error()})
			return
		}
		completeOAuthSuccess(state, "codearts")
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}

	if code == "" && errMsg == "" && token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "code, token, or error is required"})
		return
	}

	if state != "" {
		if IsOAuthSessionCompleted(state) {
			c.JSON(http.StatusConflict, gin.H{"status": "error", "error": "oauth flow is already completed"})
			return
		}
		sessionProvider, sessionStatus, ok := GetOAuthSession(state)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "unknown or expired state"})
			return
		}
		if sessionStatus != "" && !strings.HasPrefix(sessionStatus, "auth_url|") && !strings.HasPrefix(sessionStatus, "device_code|") {
			c.JSON(http.StatusConflict, gin.H{"status": "error", "error": sessionStatus})
			return
		}
		if !strings.EqualFold(sessionProvider, canonicalProvider) {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "provider does not match state"})
			return
		}
	}

	if canonicalProvider == "qoder" {
		if token != "" {
			code = token
		}
	}

	if _, errWrite := WriteOAuthCallbackFileForPendingSessionWithAuth(h.cfg.AuthDir, canonicalProvider, state, code, errMsg, auth); errWrite != nil {
		if errors.Is(errWrite, errOAuthSessionNotPending) {
			_, status, okSession := GetOAuthSession(state)
			if okSession && status != "" {
				c.JSON(http.StatusConflict, gin.H{"status": "error", "error": status})
				return
			}
			c.JSON(http.StatusConflict, gin.H{"status": "error", "error": "oauth flow is not pending"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "failed to persist oauth callback"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// finishCodeArtsFromCallback completes the CodeArts OAuth flow by parsing the
// callback URL (which carries secret + redirect with ticket_id) and calling
// CompleteWithTicketPoll synchronously. This is used when users manually paste
// the redirect URL in remote deployment scenarios.
func (h *Handler) finishCodeArtsFromCallback(c *gin.Context, state, rawRedirect string) error {
	if h.codeArtsOAuthHandler == nil {
		return fmt.Errorf("codearts handler unavailable")
	}

	u, err := url.Parse(rawRedirect)
	if err != nil {
		return fmt.Errorf("invalid redirect_url: %w", err)
	}

	q := u.Query()
	secret := strings.TrimSpace(q.Get("secret"))
	if secret == "" {
		return fmt.Errorf("missing secret in callback URL")
	}

	// Extract ticket_id from the nested redirect parameter.
	ticketID := ""
	if redirectParam := q.Get("redirect"); redirectParam != "" {
		if redirectURL, parseErr := url.Parse(redirectParam); parseErr == nil {
			ticketID = strings.TrimSpace(redirectURL.Query().Get("ticket_id"))
		}
	}

	if ticketID == "" {
		return fmt.Errorf("missing ticket_id in callback URL")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	tokenResp, err := h.codeArtsOAuthHandler.CompleteWithTicketPoll(ctx, state, ticketID, secret)
	if err != nil {
		return fmt.Errorf("ticket polling failed: %w", err)
	}

	// Save auth file
	h.codeArtsOAuthHandler.SaveTokenFromResponse(state, tokenResp)

	log.Infof("CodeArts OAuth: callback completed successfully for state %s, user %s", state, tokenResp.UserName)
	return nil
}
