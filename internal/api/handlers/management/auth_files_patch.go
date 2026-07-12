package management

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	// "github.com/router-for-me/CLIProxyAPI/v7/internal/browser"

	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func (h *Handler) PatchAuthFileStatus(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	var req struct {
		Name     string `json:"name"`
		Disabled *bool  `json:"disabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	if req.Disabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "disabled is required"})
		return
	}

	ctx := c.Request.Context()

	// Find auth by name or ID
	var targetAuth *coreauth.Auth
	if auth, ok := h.authManager.GetByID(name); ok {
		targetAuth = auth
	} else {
		auths := h.authManager.List()
		for _, auth := range auths {
			if auth.FileName == name {
				targetAuth = auth
				break
			}
		}
	}

	if targetAuth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
		return
	}

	// Update disabled state
	targetAuth.Disabled = *req.Disabled
	if *req.Disabled {
		targetAuth.Status = coreauth.StatusDisabled
		targetAuth.StatusMessage = "disabled via management API"
	} else {
		targetAuth.Status = coreauth.StatusActive
		targetAuth.StatusMessage = ""
	}
	targetAuth.UpdatedAt = time.Now()

	if _, err := h.authManager.Update(ctx, targetAuth); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to update auth: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok", "disabled": *req.Disabled})
}

// PatchAuthFileFields updates editable fields (prefix, proxy_url, headers, priority, excluded_models, disable_cooling, websockets, using_api, note) of an auth file.
func (h *Handler) PatchAuthFileFields(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	var req struct {
		Name           string            `json:"name"`
		Prefix         *string           `json:"prefix"`
		ProxyURL       *string           `json:"proxy_url"`
		Headers        map[string]string `json:"headers"`
		Priority       *int              `json:"priority"`
		ExcludedModels []string          `json:"excluded_models"`
		DisableCooling json.RawMessage   `json:"disable_cooling"`
		Websockets     json.RawMessage   `json:"websockets"`
		UsingAPI       json.RawMessage   `json:"using_api"`
		Note           *string           `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}

	ctx := c.Request.Context()

	// Find auth by name or ID
	var targetAuth *coreauth.Auth
	if auth, ok := h.authManager.GetByID(name); ok {
		targetAuth = auth
	} else {
		auths := h.authManager.List()
		for _, auth := range auths {
			if auth.FileName == name {
				targetAuth = auth
				break
			}
		}
	}

	if targetAuth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
		return
	}

	changed := false
	if req.Prefix != nil {
		prefix := strings.TrimSpace(*req.Prefix)
		targetAuth.Prefix = prefix
		if targetAuth.Metadata == nil {
			targetAuth.Metadata = make(map[string]any)
		}
		if prefix == "" {
			delete(targetAuth.Metadata, "prefix")
		} else {
			targetAuth.Metadata["prefix"] = prefix
		}
		changed = true
	}
	if req.ProxyURL != nil {
		proxyURL := strings.TrimSpace(*req.ProxyURL)
		targetAuth.ProxyURL = proxyURL
		if targetAuth.Metadata == nil {
			targetAuth.Metadata = make(map[string]any)
		}
		if proxyURL == "" {
			delete(targetAuth.Metadata, "proxy_url")
		} else {
			targetAuth.Metadata["proxy_url"] = proxyURL
		}
		changed = true
	}
	if req.Headers != nil {
		existingHeaders := coreauth.ExtractCustomHeadersFromMetadata(targetAuth.Metadata)
		nextHeaders := make(map[string]string, len(existingHeaders))
		maps.Copy(nextHeaders, existingHeaders)
		headerChanged := len(req.Headers) == 0 && len(existingHeaders) > 0

		if targetAuth.Attributes != nil {
			for key := range targetAuth.Attributes {
				if strings.HasPrefix(key, "header:") && len(req.Headers) == 0 {
					headerChanged = true
					break
				}
			}
		}

		if len(req.Headers) == 0 {
			nextHeaders = map[string]string{}
		} else {
			for key, value := range req.Headers {
				name := strings.TrimSpace(key)
				if name == "" {
					continue
				}
				val := strings.TrimSpace(value)
				attrKey := "header:" + name
				if val == "" {
					if _, ok := nextHeaders[name]; ok {
						delete(nextHeaders, name)
						headerChanged = true
					}
					if targetAuth.Attributes != nil {
						if _, ok := targetAuth.Attributes[attrKey]; ok {
							headerChanged = true
						}
					}
					continue
				}
				if prev, ok := nextHeaders[name]; !ok || prev != val {
					headerChanged = true
				}
				nextHeaders[name] = val
				if targetAuth.Attributes != nil {
					if prev, ok := targetAuth.Attributes[attrKey]; !ok || prev != val {
						headerChanged = true
					}
				} else {
					headerChanged = true
				}
			}
		}

		if headerChanged {
			if targetAuth.Metadata == nil {
				targetAuth.Metadata = make(map[string]any)
			}
			if targetAuth.Attributes == nil {
				targetAuth.Attributes = make(map[string]string)
			}
			for key := range targetAuth.Attributes {
				if strings.HasPrefix(key, "header:") {
					delete(targetAuth.Attributes, key)
				}
			}
			for name, value := range nextHeaders {
				targetAuth.Attributes["header:"+name] = value
			}
			if len(nextHeaders) == 0 {
				delete(targetAuth.Metadata, "headers")
			} else {
				metaHeaders := make(map[string]any, len(nextHeaders))
				for k, v := range nextHeaders {
					metaHeaders[k] = v
				}
				targetAuth.Metadata["headers"] = metaHeaders
			}
			changed = true
		}
	}
	if req.Priority != nil || req.ExcludedModels != nil || len(req.DisableCooling) > 0 || len(req.Websockets) > 0 || len(req.UsingAPI) > 0 || req.Note != nil {
		if targetAuth.Metadata == nil {
			targetAuth.Metadata = make(map[string]any)
		}
		if targetAuth.Attributes == nil {
			targetAuth.Attributes = make(map[string]string)
		}

		if req.Priority != nil {
			if *req.Priority == 0 {
				delete(targetAuth.Metadata, "priority")
				delete(targetAuth.Attributes, "priority")
			} else {
				targetAuth.Metadata["priority"] = *req.Priority
				targetAuth.Attributes["priority"] = strconv.Itoa(*req.Priority)
			}
		}
		if req.ExcludedModels != nil {
			seenModels := make(map[string]struct{}, len(req.ExcludedModels))
			excludedModels := make([]string, 0, len(req.ExcludedModels))
			for _, rawModel := range req.ExcludedModels {
				model := strings.TrimSpace(strings.ToLower(rawModel))
				if model == "" {
					continue
				}
				if _, ok := seenModels[model]; ok {
					continue
				}
				seenModels[model] = struct{}{}
				excludedModels = append(excludedModels, model)
			}
			sort.Strings(excludedModels)
			if len(excludedModels) == 0 {
				delete(targetAuth.Metadata, "excluded_models")
				delete(targetAuth.Metadata, "excluded-models")
				delete(targetAuth.Attributes, "excluded_models")
			} else {
				targetAuth.Metadata["excluded_models"] = excludedModels
				delete(targetAuth.Metadata, "excluded-models")
				targetAuth.Attributes["excluded_models"] = strings.Join(excludedModels, ",")
			}
		}
		if len(req.DisableCooling) > 0 {
			if parsed, ok := parseNullableBoolRaw(req.DisableCooling); ok {
				targetAuth.Metadata["disable_cooling"] = parsed
			} else {
				delete(targetAuth.Metadata, "disable_cooling")
			}
			delete(targetAuth.Metadata, "disable-cooling")
		}
		if len(req.Websockets) > 0 {
			if parsed, ok := parseNullableBoolRaw(req.Websockets); ok {
				targetAuth.Metadata["websockets"] = parsed
				targetAuth.Attributes["websockets"] = strconv.FormatBool(parsed)
			} else {
				delete(targetAuth.Metadata, "websockets")
				delete(targetAuth.Attributes, "websockets")
			}
		}
		if len(req.UsingAPI) > 0 {
			// xAI only: true uses official API, false uses CLI chat-proxy.
			// null/empty clears the override so auth_kind defaults apply.
			// Keep base_url aligned so auth-file preview and runtime credentials match.
			if parsed, ok := parseNullableBoolRaw(req.UsingAPI); ok {
				targetAuth.Metadata["using_api"] = parsed
				targetAuth.Attributes["using_api"] = strconv.FormatBool(parsed)
				baseURL := xaiauth.CLIChatProxyBaseURL
				if parsed {
					baseURL = xaiauth.DefaultAPIBaseURL
				}
				targetAuth.Metadata["base_url"] = baseURL
				targetAuth.Attributes["base_url"] = baseURL
			} else {
				delete(targetAuth.Metadata, "using_api")
				delete(targetAuth.Attributes, "using_api")
				// Restore the auth_kind default endpoint when the override is cleared.
				baseURL := xaiauth.DefaultAPIBaseURL
				authKind := strings.TrimSpace(authAttribute(targetAuth, "auth_kind"))
				if authKind == "" && targetAuth.Metadata != nil {
					if raw, ok := targetAuth.Metadata["auth_kind"].(string); ok {
						authKind = strings.TrimSpace(raw)
					}
				}
				if strings.EqualFold(authKind, "oauth") {
					baseURL = xaiauth.CLIChatProxyBaseURL
				}
				targetAuth.Metadata["base_url"] = baseURL
				targetAuth.Attributes["base_url"] = baseURL
			}
		}
		if req.Note != nil {
			trimmedNote := strings.TrimSpace(*req.Note)
			if trimmedNote == "" {
				delete(targetAuth.Metadata, "note")
				delete(targetAuth.Attributes, "note")
			} else {
				targetAuth.Metadata["note"] = trimmedNote
				targetAuth.Attributes["note"] = trimmedNote
			}
		}
		changed = true
	}

	if !changed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no fields to update"})
		return
	}

	targetAuth.UpdatedAt = time.Now()

	if _, err := h.authManager.Update(ctx, targetAuth); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to update auth: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) disableAuth(ctx context.Context, id string) {
	if h == nil || h.authManager == nil {
		return
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}

	// Add timeout for disable operation to prevent hanging
	updateCtx, updateCancel := context.WithTimeout(ctx, 10*time.Second)
	defer updateCancel()

	if auth, ok := h.authManager.GetByID(id); ok {
		auth.Disabled = true
		auth.Status = coreauth.StatusDisabled
		auth.StatusMessage = "removed via management API"
		auth.UpdatedAt = time.Now()
		_, _ = h.authManager.Update(updateCtx, auth)
		return
	}
	authID := h.authIDForPath(id)
	if authID == "" {
		return
	}
	if auth, ok := h.authManager.GetByID(authID); ok {
		auth.Disabled = true
		auth.Status = coreauth.StatusDisabled
		auth.StatusMessage = "removed via management API"
		auth.UpdatedAt = time.Now()
		_, _ = h.authManager.Update(updateCtx, auth)
	}
}

func (h *Handler) deleteTokenRecord(ctx context.Context, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("auth path is empty")
	}
	store := h.tokenStoreWithBaseDir()
	if store == nil {
		return fmt.Errorf("token store unavailable")
	}

	// Add timeout for delete operation to prevent hanging
	deleteCtx, deleteCancel := context.WithTimeout(ctx, 10*time.Second)
	defer deleteCancel()
	return store.Delete(deleteCtx, path)
}

func (h *Handler) tokenStoreWithBaseDir() coreauth.Store {
	if h == nil {
		return nil
	}
	store := h.tokenStore
	if store == nil {
		store = sdkAuth.GetTokenStore()
		h.tokenStore = store
	}
	if h.cfg != nil {
		if dirSetter, ok := store.(interface{ SetBaseDir(string) }); ok {
			dirSetter.SetBaseDir(h.cfg.AuthDir)
		}
	}
	return store
}

func (h *Handler) saveTokenRecord(ctx context.Context, record *coreauth.Auth) (string, error) {
	if record == nil {
		return "", fmt.Errorf("token record is nil")
	}
	store := h.tokenStoreWithBaseDir()
	if store == nil {
		return "", fmt.Errorf("token store unavailable")
	}
	if h.postAuthHook != nil {
		if err := h.postAuthHook(ctx, record); err != nil {
			return "", fmt.Errorf("post-auth hook failed: %w", err)
		}
	}
	return store.Save(ctx, record)
}
