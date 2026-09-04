package codearts

// Permanent IAM AK/SK authentication for CodeArts.
//
// The official CodeArts Agent CLI accepts permanent IAM access keys through the
// CODEARTS_CLI_AK / CODEARTS_CLI_SK environment variables. In that mode it signs
// every upstream call with a bare SDK-HMAC-SHA256 signature and never sends a
// security token, so there is no 24-hour STS expiry to renew.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	// EnvAccessKeyID and EnvSecretAccessKey mirror the environment variables the
	// official CodeArts Agent CLI reads permanent IAM credentials from.
	EnvAccessKeyID     = "CODEARTS_CLI_AK"
	EnvSecretAccessKey = "CODEARTS_CLI_SK"

	// SnapManagerCurrentUserURL identifies the caller behind an AK/SK signature.
	SnapManagerCurrentUserURL = SnapManagerHost + "/v1/current/user"

	// LoginModeMetadataKey selects which credential flow produced an auth record.
	LoginModeMetadataKey = "login_mode"
	// LoginModeOAuth marks records produced by the PKCE portal flow, whose STS
	// credentials expire after 24 hours.
	LoginModeOAuth = "oauth"
	// LoginModeAKSK marks records backed by permanent IAM keys, which never expire.
	LoginModeAKSK = "aksk"
	// AccessKeyMetadataKey and SecretKeyMetadataKey name the stored IAM keys.
	AccessKeyMetadataKey = "ak"
	SecretKeyMetadataKey = "sk"
)

// AKSKUserInfo is the identity resolved for a pair of permanent IAM keys.
type AKSKUserInfo struct {
	UserID   string `json:"user_id"`
	UserName string `json:"user_name"`
	DomainID string `json:"domain_id"`
}

// VerifyAKSK validates permanent IAM credentials and resolves the caller
// identity. The request carries no security token, matching the official CLI's
// AK/SK-only signing path.
func (a *CodeArtsAuth) VerifyAKSK(ctx context.Context, ak, sk string) (*AKSKUserInfo, error) {
	if ak == "" || sk == "" {
		return nil, fmt.Errorf("codearts: AK and SK are both required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Credential acquisition is allowed to bound its own wait.
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, SnapManagerCurrentUserURL, nil)
	if err != nil {
		return nil, fmt.Errorf("codearts: build current-user request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Language", "zh-cn")
	SignRequest(req, nil, ak, sk, "")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codearts: verify AK/SK: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("codearts: close verify response body: %v", errClose)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("codearts: read current-user response: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("codearts: AK/SK rejected by upstream (HTTP %d): %s", resp.StatusCode, truncateCodeArtsStr(string(body), 256))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("codearts: current-user returned HTTP %d: %s", resp.StatusCode, truncateCodeArtsStr(string(body), 256))
	}

	info := &AKSKUserInfo{}
	if err := json.Unmarshal(body, info); err != nil {
		return nil, fmt.Errorf("codearts: decode current-user response: %w", err)
	}
	return info, nil
}

// AKSKLabel derives a human-readable account label from a verified identity.
func AKSKLabel(info *AKSKUserInfo) string {
	if info == nil {
		return "codearts"
	}
	if label := strings.TrimSpace(info.UserName); label != "" {
		return label
	}
	if label := strings.TrimSpace(info.UserID); label != "" {
		return label
	}
	return "codearts"
}

// AKSKFileName builds the auth file name for a permanent AK/SK record.
func AKSKFileName(label string) string {
	return fmt.Sprintf("codearts-%s-aksk.json", SanitizeFileName(label))
}

// BuildAKSKMetadata assembles the auth metadata for permanent IAM credentials.
// It deliberately omits expires_at: permanent keys must never be scheduled for
// refresh.
func BuildAKSKMetadata(ak, sk string, info *AKSKUserInfo) map[string]any {
	metadata := map[string]any{
		"type":               "codearts",
		"auth_kind":          LoginModeAKSK,
		LoginModeMetadataKey: LoginModeAKSK,
		AccessKeyMetadataKey: ak,
		SecretKeyMetadataKey: sk,
		"user_id":            "",
		"user_name":          "",
		"domain_id":          "",
	}
	if info != nil {
		metadata["user_id"] = strings.TrimSpace(info.UserID)
		metadata["user_name"] = strings.TrimSpace(info.UserName)
		metadata["domain_id"] = strings.TrimSpace(info.DomainID)
	}
	return metadata
}

// SanitizeFileName reduces an identifier to characters safe for an auth file name.
func SanitizeFileName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "user"
	}
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_' || r == '.':
			builder.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				builder.WriteRune('-')
				lastDash = true
			}
		}
	}
	result := strings.Trim(builder.String(), "-")
	if result == "" {
		return "user"
	}
	return result
}
