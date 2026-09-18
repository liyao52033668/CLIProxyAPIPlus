package lobsterai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	// ProviderID is the provider key persisted as the auth file "type".
	ProviderID = "lobsterai"
	// RefreshLead refreshes access tokens 30 minutes before expiry.
	RefreshLead = 30 * time.Minute
)

// Credentials is the credential set an executor reads out of an auth record.
type Credentials struct {
	AccessToken      string
	RefreshToken     string
	InstallationUUID string
	UserID           string
	BaseURL          string
}

// CredentialsFromAuth extracts the LobsterAI credential set from an auth record.
// It accepts metadata keys written by the login flows and the equivalent
// attribute keys so hand-written auth files work too.
func CredentialsFromAuth(auth *coreauth.Auth) Credentials {
	if auth == nil {
		return Credentials{}
	}
	creds := Credentials{
		AccessToken:      metadataString(auth, "access_token", "accessToken", "api_key", "auth_token"),
		RefreshToken:     metadataString(auth, "refresh_token", "refreshToken"),
		InstallationUUID: metadataString(auth, "installation_uuid", "uuid"),
		UserID:           metadataString(auth, "user_id", "userId"),
		BaseURL:          metadataString(auth, "base_url", "baseUrl"),
	}
	if creds.AccessToken == "" && auth.Attributes != nil {
		creds.AccessToken = strings.TrimSpace(auth.Attributes["api_key"])
	}
	if creds.BaseURL == "" && auth.Attributes != nil {
		creds.BaseURL = strings.TrimSpace(auth.Attributes["base_url"])
	}
	if creds.UserID == "" && auth.Attributes != nil {
		creds.UserID = strings.TrimSpace(auth.Attributes["user_id"])
	}
	return creds
}

// ExpiresAt reads the persisted token expiry.
func ExpiresAt(auth *coreauth.Auth) (time.Time, bool) {
	if auth == nil || auth.Metadata == nil {
		return time.Time{}, false
	}
	return ParseExpiresAt(auth.Metadata["expires_at"])
}

// BuildAuthRecord assembles the persisted credential for an exchanged token.
// The usage snapshot is optional; when the caller already fetched one it seeds
// the quota signals so the panel shows balances before the first refresh.
func BuildAuthRecord(payload *TokenPayload, installationUUID, serverBase string, usage *Usage) *coreauth.Auth {
	if payload == nil {
		return nil
	}
	accessToken := strings.TrimSpace(payload.AccessToken)
	uid := strings.TrimSpace(payload.UID)
	if uid == "" {
		sum := sha256.Sum256([]byte(accessToken))
		uid = hex.EncodeToString(sum[:8])
	}
	baseURL := strings.TrimSpace(serverBase)
	if baseURL == DefaultServerBaseURL {
		baseURL = ""
	}

	metadata := map[string]any{
		"type":           ProviderID,
		"access_token":   accessToken,
		"auth_method":    "oauth",
		"login_method":   "browser",
		"uid":            uid,
		"timestamp":      time.Now().UnixMilli(),
		"first_keyfrom":  DefaultKeyfrom,
		"latest_keyfrom": DefaultKeyfrom,
	}
	if refreshToken := strings.TrimSpace(payload.RefreshToken); refreshToken != "" {
		metadata["refresh_token"] = refreshToken
	}
	if expiresAt := FormatExpiresAt(payload.ExpiresAt); expiresAt != "" {
		metadata["expires_at"] = expiresAt
	}
	if installationUUID != "" {
		metadata["installation_uuid"] = installationUUID
	}
	if payload.UserID != "" {
		metadata["user_id"] = payload.UserID
	}
	if payload.Nickname != "" {
		metadata["nickname"] = payload.Nickname
	}
	if baseURL != "" {
		metadata["base_url"] = baseURL
	}

	attributes := map[string]string{
		"auth_kind": "oauth",
		"uid":       uid,
	}
	if baseURL != "" {
		attributes["base_url"] = baseURL
	}
	if installationUUID != "" {
		attributes["installation_uuid"] = installationUUID
	}
	if payload.UserID != "" {
		attributes["user_id"] = payload.UserID
	}
	if payload.Nickname != "" {
		attributes["nickname"] = payload.Nickname
	}

	label := payload.Nickname
	if label == "" {
		label = uid
	}

	fileName := CredentialFileName(uid, payload.Nickname)
	record := &coreauth.Auth{
		ID:         fileName,
		Provider:   ProviderID,
		FileName:   fileName,
		Label:      label,
		Metadata:   metadata,
		Attributes: attributes,
	}
	if usage != nil {
		record.Quota = coreauth.QuotaState{
			ObservedAt: time.Now(),
			Signals:    Signals(usage),
		}
	}
	return record
}

// CredentialFileName derives the auth file name, preferring the nickname and
// falling back to the account uid.
func CredentialFileName(uid, nickname string) string {
	base := sanitizeFileSegment(nickname)
	if base == "" {
		base = sanitizeFileSegment(uid)
	}
	if base == "" {
		base = "account"
	}
	return fmt.Sprintf("%s-%s.json", ProviderID, base)
}

// sanitizeFileSegment keeps the file name portable across platforms.
func sanitizeFileSegment(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	var builder strings.Builder
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			builder.WriteRune(r)
		case r == '@' || r == '.' || r == ' ':
			builder.WriteRune('_')
		}
	}
	return strings.Trim(builder.String(), "_")
}

// Signals converts a usage snapshot into quota watermark signals for the panel.
func Signals(usage *Usage) map[string]string {
	if usage == nil {
		return nil
	}
	signals := map[string]string{}
	if usage.CreditsRemaining > 0 {
		signals["total_credits_remaining"] = formatCredits(usage.CreditsRemaining)
	}
	if usage.CreditsLimit > 0 {
		signals["credits_limit"] = formatCredits(usage.CreditsLimit)
		signals["credits_used"] = formatCredits(usage.CreditsUsed)
	}
	if usage.PlanName != "" {
		signals["plan"] = usage.PlanName
	}
	if usage.Subscription != "" {
		signals["subscription_status"] = usage.Subscription
	}
	if len(signals) == 0 {
		return nil
	}
	return signals
}

func formatCredits(value float64) string {
	if value == float64(int64(value)) {
		return fmt.Sprintf("%d", int64(value))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", value), "0"), ".")
}

// FetchUsage fetches the account usage snapshot, logging and returning nil on
// failure so callers can treat quota as best effort.
func FetchUsage(ctx context.Context, service *Service, accessToken string) *Usage {
	if service == nil {
		return nil
	}
	usage, errUsage := service.FetchUsage(ctx, accessToken)
	if errUsage != nil {
		log.Debugf("lobsterai: usage lookup failed: %v", errUsage)
		return nil
	}
	return usage
}

func metadataString(auth *coreauth.Auth, keys ...string) string {
	if auth == nil {
		return ""
	}
	for _, key := range keys {
		if auth.Metadata == nil {
			break
		}
		if value, ok := auth.Metadata[key].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	for _, key := range keys {
		if auth.Attributes == nil {
			break
		}
		if value := strings.TrimSpace(auth.Attributes[key]); value != "" {
			return value
		}
	}
	return ""
}
