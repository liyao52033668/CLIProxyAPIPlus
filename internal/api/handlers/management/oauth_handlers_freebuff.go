package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	freebuffauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/freebuff"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	// freebuffLoginPollInterval paces the upstream status checks while the user
	// completes the browser login.
	freebuffLoginPollInterval = 2500 * time.Millisecond
	// freebuffLoginPollTimeout bounds credential acquisition; it matches the
	// OAuth session TTL so the state cannot outlive the poll loop.
	freebuffLoginPollTimeout = 10 * time.Minute
)

// Upstream client seams so tests can stub the device-flow exchange offline.
var (
	freebuffRequestLoginCodeFn = freebuffauth.RequestLoginCode
	freebuffPollLoginStatusFn  = freebuffauth.PollLoginStatus
)

// RequestFreebuffToken starts the Freebuff CLI device-flow login. It follows
// the standard OAuth session machinery: the browser login URL is returned
// immediately and stored on the session (so get-auth-status can hand it back),
// and a background goroutine polls the upstream until the user authorizes,
// then persists the credential as a Freebuff OAuth auth file.
func (h *Handler) RequestFreebuffToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	CompleteOAuthSessionsByProvider("freebuff")

	fingerprintID, errFingerprint := freebuffauth.NewFingerprintID()
	if errFingerprint != nil {
		log.Errorf("freebuff login: failed to generate fingerprint: %v", errFingerprint)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate login fingerprint"})
		return
	}
	client := helps.NewProxyAwareHTTPClient(ctx, h.cfg, nil, 0)
	code, errCode := freebuffRequestLoginCodeFn(ctx, client, freebuffauth.DefaultLoginBaseURL, fingerprintID)
	if errCode != nil {
		log.Errorf("freebuff login: failed to request login code: %v", errCode)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to start freebuff login"})
		return
	}

	state, errState := misc.GenerateRandomState()
	if errState != nil {
		log.Errorf("freebuff login: failed to generate state: %v", errState)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}
	RegisterOAuthSession(state, "freebuff")
	SetOAuthSessionError(state, "auth_url|"+code.LoginURL)

	go h.pollFreebuffLogin(ctx, state, client, code)

	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
		"url":    code.LoginURL,
		"state":  state,
	})
}

// pollFreebuffLogin drives the upstream status checks until the browser login
// completes, then saves the credential file. It returns when the session is
// completed, cancelled, or the poll deadline expires.
func (h *Handler) pollFreebuffLogin(ctx context.Context, state string, client *http.Client, code *freebuffauth.LoginCode) {
	pollCtx, cancel := context.WithTimeout(ctx, freebuffLoginPollTimeout)
	defer cancel()

	// Abort the poll loop when the OAuth session is cancelled from the UI.
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
				if !IsOAuthSessionPending(state, "freebuff") {
					cancel()
					return
				}
			}
		}
	}()

	for {
		user, pending, errPoll := freebuffPollLoginStatusFn(pollCtx, client, freebuffauth.DefaultLoginBaseURL, code)
		if errPoll != nil {
			if errors.Is(errPoll, context.Canceled) || errors.Is(errPoll, context.DeadlineExceeded) {
				return
			}
			log.Errorf("freebuff login: status poll failed: %v", errPoll)
			SetOAuthSessionError(state, "Authentication failed: "+errPoll.Error())
			return
		}
		if pending || user == nil {
			if sleepFreebuffPoll(pollCtx, freebuffLoginPollInterval) {
				return
			}
			continue
		}
		if strings.TrimSpace(user.AuthToken) == "" {
			log.Error("freebuff login: authorized without token")
			SetOAuthSessionError(state, "Authentication failed: token not found")
			return
		}
		record := buildFreebuffAuthRecord(user.AuthToken, freebuffauth.DefaultLoginBaseURL, user.Email, user.Name, "")
		savedPath, errSave := h.saveTokenRecord(pollCtx, record)
		if errSave != nil {
			log.Errorf("freebuff login: failed to save credential file: %v", errSave)
			SetOAuthSessionError(state, "Failed to save token to file")
			return
		}
		completeOAuthSuccess(state, "freebuff")
		log.Infof("freebuff login: credential saved to %s", savedPath)
		return
	}
}

func sleepFreebuffPoll(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-timer.C:
		return false
	}
}

// buildFreebuffAuthRecord assembles the persisted OAuth credential for a
// device-flow login. The account email/name come from the poll step, which is
// the only upstream response carrying them. The file name is derived from the
// account email (or a token hash) so re-logging the same account overwrites
// the same file.
func buildFreebuffAuthRecord(token, baseURL, email, name, comment string) *coreauth.Auth {
	token = strings.TrimSpace(token)
	sum := sha256.Sum256([]byte(token))
	uid := hex.EncodeToString(sum[:8])

	metadata := map[string]any{
		"type":         "freebuff",
		"access_token": token,
		"uid":          uid,
		"auth_method":  "oauth",
		"login_method": "device_flow",
		"timestamp":    time.Now().UnixMilli(),
	}
	email = strings.TrimSpace(email)
	name = strings.TrimSpace(name)
	if email != "" {
		metadata["email"] = email
	}
	if name != "" {
		metadata["name"] = name
	}
	if baseURL != "" && baseURL != freebuffauth.DefaultLoginBaseURL {
		metadata["base_url"] = baseURL
	}
	label := strings.TrimSpace(comment)
	if label == "" {
		label = email
	}
	if label == "" {
		label = name
	}
	if label == "" {
		label = "freebuff"
	}

	fileName := freebuffCredentialFileName(uid, email)
	return &coreauth.Auth{
		ID:       fileName,
		Provider: "freebuff",
		FileName: fileName,
		Label:    label,
		Metadata: metadata,
	}
}

// freebuffCredentialFileName mirrors the qoder naming: prefer the email, fall
// back to the uid, and always end with a .json suffix.
func freebuffCredentialFileName(uid, email string) string {
	email = strings.TrimSpace(email)
	if email != "" {
		email = strings.ReplaceAll(email, "@", "_")
		email = strings.ReplaceAll(email, ".", "_")
		return fmt.Sprintf("freebuff-%s.json", email)
	}
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return "freebuff.json"
	}
	return fmt.Sprintf("freebuff-%s.json", uid)
}
