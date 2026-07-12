package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"

	// "github.com/router-for-me/CLIProxyAPI/v7/internal/browser"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

func (h *Handler) ListAuthFiles(c *gin.Context) {
	if h == nil {
		c.JSON(500, gin.H{"error": "handler not initialized"})
		return
	}

	// Add timeout to prevent hanging on slow operations
	listCtx, listCancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer listCancel()

	if h.authManager == nil {
		h.listAuthFilesFromDisk(listCtx, c)
		return
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		auths := h.authManager.List()
		files := make([]gin.H, 0, len(auths))
		for _, auth := range auths {
			if entry := h.buildAuthFileEntry(auth); entry != nil {
				files = append(files, entry)
			}
		}
		if h.invalidAuthSnapshot != nil {
			for _, invalid := range h.invalidAuthSnapshot() {
				files = append(files, invalidWatcherAuthEntry(invalid))
			}
		}
		sort.Slice(files, func(i, j int) bool {
			nameI, _ := files[i]["name"].(string)
			nameJ, _ := files[j]["name"].(string)
			return strings.ToLower(nameI) < strings.ToLower(nameJ)
		})
		c.JSON(200, gin.H{"files": files})
	}()

	select {
	case <-done:
		return
	case <-listCtx.Done():
		c.JSON(http.StatusRequestTimeout, gin.H{"error": "list auth files timeout"})
	}
}

// GetAuthFileModels returns the models supported by a specific auth file
func (h *Handler) GetAuthFileModels(c *gin.Context) {
	name := c.Query("name")
	if name == "" {
		c.JSON(400, gin.H{"error": "name is required"})
		return
	}

	// Try to find auth ID via authManager
	var auth *coreauth.Auth
	var authID string
	if h.authManager != nil {
		auths := h.authManager.List()
		for _, a := range auths {
			if a.FileName == name || a.ID == name {
				auth = a
				authID = a.ID
				break
			}
		}
	}

	if authID == "" {
		authID = name // fallback to filename as ID
	}

	// Get models from registry
	reg := registry.GetGlobalRegistry()
	models := reg.GetModelsForClient(authID)

	result := make([]gin.H, 0, len(models))
	for _, m := range models {
		modelID := m.ID
		// Use alias if available
		if auth != nil && h.authManager != nil {
			modelID = h.authManager.GetOAuthModelAlias(auth, m.ID)
		}
		entry := gin.H{
			"id": modelID,
		}
		if m.DisplayName != "" {
			entry["display_name"] = m.DisplayName
		}
		if m.Type != "" {
			entry["type"] = m.Type
		}
		if m.OwnedBy != "" {
			entry["owned_by"] = m.OwnedBy
		}
		result = append(result, entry)
	}

	c.JSON(200, gin.H{"models": result})
}

// List auth files from disk when the auth manager is unavailable.
func (h *Handler) listAuthFilesFromDisk(ctx context.Context, c *gin.Context) {
	done := make(chan []gin.H, 1)
	errChan := make(chan error, 1)

	go func() {
		entries, err := os.ReadDir(h.cfg.AuthDir)
		if err != nil {
			errChan <- err
			return
		}
		files := make([]gin.H, 0)
		for _, e := range entries {
			select {
			case <-ctx.Done():
				return
			default:
			}

			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(strings.ToLower(name), ".json") {
				continue
			}
			if info, errInfo := e.Info(); errInfo == nil {
				fileData := gin.H{"name": name, "size": info.Size(), "modtime": info.ModTime()}

				// Read file to get type field
				full := filepath.Join(h.cfg.AuthDir, name)
				if data, errRead := os.ReadFile(full); errRead == nil {
					typeValue := gjson.GetBytes(data, "type").String()
					emailValue := gjson.GetBytes(data, "email").String()
					fileData["type"] = typeValue
					fileData["email"] = emailValue
					if projectID := strings.TrimSpace(gjson.GetBytes(data, "project_id").String()); projectID != "" {
						fileData["project_id"] = projectID
					}
					if pv := gjson.GetBytes(data, "priority"); pv.Exists() {
						switch pv.Type {
						case gjson.Number:
							fileData["priority"] = int(pv.Int())
						case gjson.String:
							if parsed, errAtoi := strconv.Atoi(strings.TrimSpace(pv.String())); errAtoi == nil {
								fileData["priority"] = parsed
							}
						}
					}
					if nv := gjson.GetBytes(data, "note"); nv.Exists() && nv.Type == gjson.String {
						if trimmed := strings.TrimSpace(nv.String()); trimmed != "" {
							fileData["note"] = trimmed
						}
					}
				}

				files = append(files, fileData)
			}
		}
		done <- files
	}()

	select {
	case files := <-done:
		c.JSON(200, gin.H{"files": files})
	case err := <-errChan:
		c.JSON(500, gin.H{"error": fmt.Sprintf("failed to read auth dir: %v", err)})
	case <-ctx.Done():
		c.JSON(http.StatusRequestTimeout, gin.H{"error": "list auth files from disk timeout"})
	}
}

func (h *Handler) buildAuthFileEntry(auth *coreauth.Auth) gin.H {
	if auth == nil {
		return nil
	}
	auth.EnsureIndex()
	runtimeOnly := isRuntimeOnlyAuth(auth)
	if runtimeOnly && (auth.Disabled || auth.Status == coreauth.StatusDisabled) {
		return nil
	}
	path := strings.TrimSpace(authAttribute(auth, "path"))
	if path == "" && !runtimeOnly {
		return nil
	}
	name := strings.TrimSpace(auth.FileName)
	if name == "" {
		name = auth.ID
	}
	entry := gin.H{
		"id":             auth.ID,
		"auth_index":     auth.Index,
		"name":           name,
		"type":           strings.TrimSpace(auth.Provider),
		"provider":       strings.TrimSpace(auth.Provider),
		"label":          auth.Label,
		"status":         auth.Status,
		"status_message": auth.StatusMessage,
		"disabled":       auth.Disabled,
		"unavailable":    auth.Unavailable,
		"runtime_only":   runtimeOnly,
		"source":         "memory",
		"size":           int64(0),
	}
	entry["success"] = auth.Success
	entry["failed"] = auth.Failed
	entry["recent_requests"] = auth.RecentRequestsSnapshot(time.Now())
	if email := authEmail(auth); email != "" {
		entry["email"] = email
	}
	if projectID := authProjectID(auth); projectID != "" {
		entry["project_id"] = projectID
	}
	if accountType, account := auth.AccountInfo(); accountType != "" || account != "" {
		if accountType != "" {
			entry["account_type"] = accountType
		}
		if account != "" {
			entry["account"] = account
		}
	}
	if !auth.CreatedAt.IsZero() {
		entry["created_at"] = auth.CreatedAt
	}
	if !auth.UpdatedAt.IsZero() {
		entry["modtime"] = auth.UpdatedAt
		entry["updated_at"] = auth.UpdatedAt
	}
	if !auth.LastRefreshedAt.IsZero() {
		entry["last_refresh"] = auth.LastRefreshedAt
	}
	if !auth.NextRetryAfter.IsZero() {
		entry["next_retry_after"] = auth.NextRetryAfter
	}
	if path != "" {
		entry["path"] = path
		entry["source"] = "file"
		if info, err := os.Stat(path); err == nil {
			entry["size"] = info.Size()
			entry["modtime"] = info.ModTime()
		} else if os.IsNotExist(err) {
			// Hide credentials removed from disk but still lingering in memory.
			if !runtimeOnly && (auth.Disabled || auth.Status == coreauth.StatusDisabled || strings.EqualFold(strings.TrimSpace(auth.StatusMessage), "removed via management api")) {
				return nil
			}
			entry["source"] = "memory"
		} else {
			log.WithError(err).Warnf("failed to stat auth file %s", path)
		}
	}
	if claims := extractCodexIDTokenClaims(auth); claims != nil {
		entry["id_token"] = claims
	}
	if kiroQuota := h.getKiroQuotaCached(auth); kiroQuota != nil {
		entry["kiro_quota"] = kiroQuota
	}
	// Expose priority from Attributes (set by synthesizer from JSON "priority" field).
	// Fall back to Metadata for auths registered via UploadAuthFile (no synthesizer).
	if p := strings.TrimSpace(authAttribute(auth, "priority")); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil {
			entry["priority"] = parsed
		}
	} else if auth.Metadata != nil {
		if rawPriority, ok := auth.Metadata["priority"]; ok {
			switch v := rawPriority.(type) {
			case float64:
				entry["priority"] = int(v)
			case int:
				entry["priority"] = v
			case string:
				if parsed, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
					entry["priority"] = parsed
				}
			}
		}
	}
	// Expose note from Attributes (set by synthesizer from JSON "note" field).
	// Fall back to Metadata for auths registered via UploadAuthFile (no synthesizer).
	if note := strings.TrimSpace(authAttribute(auth, "note")); note != "" {
		entry["note"] = note
	} else if auth.Metadata != nil {
		if rawNote, ok := auth.Metadata["note"].(string); ok {
			if trimmed := strings.TrimSpace(rawNote); trimmed != "" {
				entry["note"] = trimmed
			}
		}
	}
	if prefix := strings.TrimSpace(auth.Prefix); prefix != "" {
		entry["prefix"] = prefix
	} else if auth.Metadata != nil {
		if rawPrefix, ok := auth.Metadata["prefix"].(string); ok {
			if trimmed := strings.TrimSpace(rawPrefix); trimmed != "" {
				entry["prefix"] = trimmed
			}
		}
	}
	if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
		entry["proxy_url"] = proxyURL
	} else if auth.Metadata != nil {
		if rawProxyURL, ok := auth.Metadata["proxy_url"].(string); ok {
			if trimmed := strings.TrimSpace(rawProxyURL); trimmed != "" {
				entry["proxy_url"] = trimmed
			}
		}
	}
	if headers := coreauth.ExtractCustomHeadersFromMetadata(auth.Metadata); len(headers) > 0 {
		entry["headers"] = headers
	}
	if excludedModels := authExcludedModels(auth); len(excludedModels) > 0 {
		entry["excluded_models"] = excludedModels
	}
	if disabledCooling, ok := auth.DisableCoolingOverride(); ok {
		entry["disable_cooling"] = disabledCooling
	}
	if rawWebsockets := strings.TrimSpace(authAttribute(auth, "websockets")); rawWebsockets != "" {
		if parsed, err := strconv.ParseBool(rawWebsockets); err == nil {
			entry["websockets"] = parsed
		}
	} else if auth.Metadata != nil {
		if raw, ok := auth.Metadata["websockets"]; ok {
			switch v := raw.(type) {
			case bool:
				entry["websockets"] = v
			case string:
				if parsed, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
					entry["websockets"] = parsed
				}
			}
		}
	}
	// using_api is an xAI routing switch: true -> official API, false -> CLI chat-proxy.
	if rawUsingAPI := strings.TrimSpace(authAttribute(auth, "using_api")); rawUsingAPI != "" {
		if parsed, err := strconv.ParseBool(rawUsingAPI); err == nil {
			entry["using_api"] = parsed
		}
	} else if auth.Metadata != nil {
		if raw, ok := auth.Metadata["using_api"]; ok {
			switch v := raw.(type) {
			case bool:
				entry["using_api"] = v
			case string:
				if parsed, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
					entry["using_api"] = parsed
				}
			}
		}
	}
	return entry
}

func parseNullableBoolRaw(raw json.RawMessage) (bool, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || strings.EqualFold(trimmed, "null") {
		return false, false
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, false
	}
	return value, true
}

func authExcludedModels(auth *coreauth.Auth) []string {
	if auth == nil {
		return nil
	}
	rawModels := ""
	if auth.Attributes != nil {
		rawModels = strings.TrimSpace(auth.Attributes["excluded_models"])
	}
	if rawModels == "" && auth.Metadata != nil {
		switch value := auth.Metadata["excluded_models"].(type) {
		case []string:
			return normalizeAuthFileStringList(value)
		case []any:
			models := make([]string, 0, len(value))
			for _, item := range value {
				models = append(models, fmt.Sprint(item))
			}
			return normalizeAuthFileStringList(models)
		case string:
			rawModels = value
		}
	}
	if rawModels == "" {
		return nil
	}
	return normalizeAuthFileStringList(strings.Split(rawModels, ","))
}

func normalizeAuthFileStringList(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		item := strings.TrimSpace(strings.ToLower(value))
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func authProjectID(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["project_id"].(string); ok {
			if projectID := strings.TrimSpace(v); projectID != "" {
				return projectID
			}
		}
	}
	if auth.Attributes != nil {
		if projectID := strings.TrimSpace(auth.Attributes["project_id"]); projectID != "" {
			return projectID
		}
		if projectID := strings.TrimSpace(auth.Attributes["gemini_virtual_project"]); projectID != "" {
			return projectID
		}
	}
	return ""
}

func extractCodexIDTokenClaims(auth *coreauth.Auth) gin.H {
	if auth == nil || auth.Metadata == nil {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return nil
	}
	idTokenRaw, ok := auth.Metadata["id_token"].(string)
	if !ok {
		return nil
	}
	idToken := strings.TrimSpace(idTokenRaw)
	if idToken == "" {
		return nil
	}
	claims, err := codex.ParseJWTToken(idToken)
	if err != nil || claims == nil {
		return nil
	}

	result := gin.H{}
	if v := strings.TrimSpace(claims.CodexAuthInfo.ChatgptAccountID); v != "" {
		result["chatgpt_account_id"] = v
	} else if v, ok := auth.Metadata["account_id"].(string); ok {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			result["chatgpt_account_id"] = trimmed
		}
	}
	if v := strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType); v != "" {
		result["plan_type"] = v
	}
	if v := claims.CodexAuthInfo.ChatgptSubscriptionActiveStart; v != nil {
		result["chatgpt_subscription_active_start"] = v
	}
	if v := claims.CodexAuthInfo.ChatgptSubscriptionActiveUntil; v != nil {
		result["chatgpt_subscription_active_until"] = v
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

func authEmail(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["email"].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["email"]); v != "" {
			return v
		}
		if v := strings.TrimSpace(auth.Attributes["account_email"]); v != "" {
			return v
		}
	}
	return ""
}

func authAttribute(auth *coreauth.Auth, key string) string {
	if auth == nil || len(auth.Attributes) == 0 {
		return ""
	}
	return auth.Attributes[key]
}

func isRuntimeOnlyAuth(auth *coreauth.Auth) bool {
	if auth == nil || len(auth.Attributes) == 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Attributes["runtime_only"]), "true")
}
