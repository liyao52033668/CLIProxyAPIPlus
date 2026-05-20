package management

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

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

	if code == "" && errMsg == "" && token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "code, token, or error is required"})
		return
	}

	if state != "" {
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
