package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

const (
	authFileTestDefaultTimeout = 30 * time.Second
	authFileTestMaxTimeout     = 120 * time.Second
	authFileTestPreviewLimit   = 240
)

type authFileTestRequest struct {
	Name           string `json:"name"`
	ID             string `json:"id"`
	AuthIndex      string `json:"auth_index"`
	Model          string `json:"model"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type authFileTestResponse struct {
	OK             bool     `json:"ok"`
	Status         string   `json:"status"`
	LatencyMS      int64    `json:"latency_ms"`
	Model          string   `json:"model"`
	Provider       string   `json:"provider,omitempty"`
	AuthID         string   `json:"auth_id,omitempty"`
	HTTPStatus     int      `json:"http_status,omitempty"`
	Error          string   `json:"error,omitempty"`
	Preview        string   `json:"preview,omitempty"`
	ExcludedAdded  bool     `json:"excluded_added,omitempty"`
	ExcludedModels []string `json:"excluded_models,omitempty"`
}

// TestAuthFileModel probes whether a specific auth credential can serve a model.
//
// Endpoint:
//
//	POST /v0/management/auth-files/test
//
// It pins execution to the selected auth, sends a minimal OpenAI-compatible chat
// request through the normal executor pipeline, and returns a structured diagnostic
// result. This endpoint is read-only from an operator perspective (it never flips
// auth.Disabled); routing side-effects from the shared Execute path may still apply.
func (h *Handler) TestAuthFileModel(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler not initialized"})
		return
	}
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth manager unavailable"})
		return
	}

	var body authFileTestRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	model := strings.TrimSpace(body.Model)
	if model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model is required"})
		return
	}

	auth := h.findAuthForModelTest(body)
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
		return
	}

	resp := authFileTestResponse{
		Model:    model,
		Provider: strings.TrimSpace(auth.Provider),
		AuthID:   strings.TrimSpace(auth.ID),
	}

	if auth.Disabled || auth.Status == coreauth.StatusDisabled {
		resp.Status = "disabled"
		resp.Error = "auth is disabled"
		c.JSON(http.StatusOK, resp)
		return
	}

	provider := strings.TrimSpace(auth.Provider)
	if provider == "" {
		resp.Status = "unsupported"
		resp.Error = "auth provider is empty"
		c.JSON(http.StatusOK, resp)
		return
	}
	if _, ok := h.authManager.Executor(provider); !ok {
		resp.Status = "unsupported"
		resp.Error = fmt.Sprintf("provider executor not found: %s", provider)
		c.JSON(http.StatusOK, resp)
		return
	}
	if kind, reason := authFileProbeMediaKind(model, provider); kind != "" {
		// Chat-style ping cannot validate image/video generation models.
		// Do not auto-exclude them — they are simply outside this probe's scope.
		resp.Status = "unsupported"
		resp.Error = reason
		c.JSON(http.StatusOK, resp)
		return
	}

	timeout := h.authFileTestTimeout(body.TimeoutSeconds)
	probeCtx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()

	payload, err := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": "ping"},
		},
		"max_tokens": 1,
		"stream":     false,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build probe payload"})
		return
	}

	opts := cliproxyexecutor.Options{
		Stream:          false,
		OriginalRequest: payload,
		SourceFormat:    sdktranslator.FormatOpenAI,
		Metadata: map[string]any{
			cliproxyexecutor.PinnedAuthMetadataKey:     auth.ID,
			cliproxyexecutor.RequestedModelMetadataKey: model,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   model,
		Payload: payload,
	}

	started := time.Now()
	execResp, execErr := h.authManager.Execute(probeCtx, []string{provider}, req, opts)
	resp.LatencyMS = time.Since(started).Milliseconds()

	if execErr != nil {
		if errors.Is(execErr, context.DeadlineExceeded) || errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
			resp.Status = "timeout"
			resp.Error = fmt.Sprintf("probe timed out after %s", timeout)
			h.applyAutoExcludeOnProbeFailure(c.Request.Context(), auth, model, &resp)
			c.JSON(http.StatusOK, resp)
			return
		}
		if errors.Is(execErr, context.Canceled) || errors.Is(probeCtx.Err(), context.Canceled) {
			// Operator canceled the probe; do not mutate excluded_models.
			resp.Status = "failed"
			resp.Error = "probe canceled"
			c.JSON(http.StatusOK, resp)
			return
		}
		resp.Status = "failed"
		resp.Error = strings.TrimSpace(execErr.Error())
		if resp.Error == "" {
			resp.Error = "probe failed"
		}
		resp.HTTPStatus = statusCodeFromProbeError(execErr)
		h.applyAutoExcludeOnProbeFailure(c.Request.Context(), auth, model, &resp)
		c.JSON(http.StatusOK, resp)
		return
	}

	resp.OK = true
	resp.Status = "success"
	resp.HTTPStatus = http.StatusOK
	resp.Preview = truncateProbePreview(string(execResp.Payload), authFileTestPreviewLimit)
	c.JSON(http.StatusOK, resp)
}

// applyAutoExcludeOnProbeFailure appends the probed model to this auth file's
// excluded_models (file-level Metadata). Best-effort: probe result stays intact
// even if the exclusion write fails.
func (h *Handler) applyAutoExcludeOnProbeFailure(ctx context.Context, auth *coreauth.Auth, model string, resp *authFileTestResponse) {
	if h == nil || resp == nil {
		return
	}
	excluded, added, err := h.appendAuthFileExcludedModel(ctx, auth, model)
	if err != nil {
		if resp.Error == "" {
			resp.Error = fmt.Sprintf("auto-exclude failed: %v", err)
		} else {
			resp.Error = fmt.Sprintf("%s; auto-exclude failed: %v", resp.Error, err)
		}
		return
	}
	resp.ExcludedModels = excluded
	resp.ExcludedAdded = added
}

// appendAuthFileExcludedModel merges model into the auth file's own
// excluded_models list (Metadata only — never bake OAuth-global exclusions into the file).
func (h *Handler) appendAuthFileExcludedModel(ctx context.Context, auth *coreauth.Auth, model string) ([]string, bool, error) {
	model = strings.TrimSpace(strings.ToLower(model))
	if h == nil || h.authManager == nil || auth == nil || model == "" {
		return nil, false, nil
	}

	target := h.refreshAuthForExclude(auth)
	if target == nil {
		return nil, false, fmt.Errorf("auth not found")
	}

	current := authFileExcludedModelsFromMetadata(target)
	for _, existing := range current {
		if existing == model {
			return current, false, nil
		}
	}
	next := normalizeAuthFileStringList(append(append([]string{}, current...), model))

	if target.Metadata == nil {
		target.Metadata = make(map[string]any)
	}
	if target.Attributes == nil {
		target.Attributes = make(map[string]string)
	}
	target.Metadata["excluded_models"] = next
	delete(target.Metadata, "excluded-models")
	target.Attributes["excluded_models"] = strings.Join(next, ",")
	target.UpdatedAt = time.Now()

	if _, err := h.authManager.Update(ctx, target); err != nil {
		return nil, false, err
	}
	return next, true, nil
}

func (h *Handler) refreshAuthForExclude(auth *coreauth.Auth) *coreauth.Auth {
	if h == nil || h.authManager == nil || auth == nil {
		return nil
	}
	if id := strings.TrimSpace(auth.ID); id != "" {
		if fresh, ok := h.authManager.GetByID(id); ok && fresh != nil {
			return fresh
		}
	}
	key := strings.TrimSpace(auth.FileName)
	if key == "" {
		key = strings.TrimSpace(auth.ID)
	}
	if key == "" {
		return nil
	}
	for _, item := range h.authManager.List() {
		if item == nil {
			continue
		}
		if item.FileName == key || item.ID == key || item.Index == key {
			return item
		}
	}
	return nil
}

// authFileExcludedModelsFromMetadata reads only the file-owned exclusion list.
// Attributes may already contain OAuth-global merges and must not be written back.
func authFileExcludedModelsFromMetadata(auth *coreauth.Auth) []string {
	if auth == nil || auth.Metadata == nil {
		return nil
	}
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
		return normalizeAuthFileStringList(strings.Split(value, ","))
	default:
		if raw, ok := auth.Metadata["excluded-models"]; ok {
			switch v := raw.(type) {
			case []string:
				return normalizeAuthFileStringList(v)
			case []any:
				models := make([]string, 0, len(v))
				for _, item := range v {
					models = append(models, fmt.Sprint(item))
				}
				return normalizeAuthFileStringList(models)
			case string:
				return normalizeAuthFileStringList(strings.Split(v, ","))
			}
		}
	}
	return nil
}

func (h *Handler) findAuthForModelTest(body authFileTestRequest) *coreauth.Auth {
	if h == nil || h.authManager == nil {
		return nil
	}

	name := strings.TrimSpace(body.Name)
	id := strings.TrimSpace(body.ID)
	authIndex := strings.TrimSpace(body.AuthIndex)

	if authIndex != "" {
		if auth := h.authByIndex(authIndex); auth != nil {
			return auth
		}
	}
	if id != "" {
		if auth, ok := h.authManager.GetByID(id); ok && auth != nil {
			return auth
		}
	}

	key := name
	if key == "" {
		key = id
	}
	if key == "" {
		return nil
	}
	if auth, ok := h.authManager.GetByID(key); ok && auth != nil {
		return auth
	}
	for _, auth := range h.authManager.List() {
		if auth == nil {
			continue
		}
		if auth.FileName == key || auth.ID == key || auth.Index == key {
			return auth
		}
	}
	return nil
}

func (h *Handler) authFileTestTimeout(seconds int) time.Duration {
	if seconds > 0 {
		timeout := time.Duration(seconds) * time.Second
		if timeout > authFileTestMaxTimeout {
			return authFileTestMaxTimeout
		}
		if timeout < time.Second {
			return time.Second
		}
		return timeout
	}
	if h != nil {
		if configured := h.apiCallTimeout(); configured > 0 && configured < authFileTestMaxTimeout {
			// Prefer a tighter default than the generic api-call timeout.
			if configured > authFileTestDefaultTimeout {
				return authFileTestDefaultTimeout
			}
			return configured
		}
	}
	return authFileTestDefaultTimeout
}

func statusCodeFromProbeError(err error) int {
	if err == nil {
		return 0
	}
	type statusCoder interface {
		StatusCode() int
	}
	if sc, ok := err.(statusCoder); ok && sc != nil {
		if code := sc.StatusCode(); code > 0 {
			return code
		}
	}
	var authErr *coreauth.Error
	if errors.As(err, &authErr) && authErr != nil && authErr.HTTPStatus > 0 {
		return authErr.HTTPStatus
	}
	return 0
}

func truncateProbePreview(text string, limit int) string {
	text = strings.TrimSpace(text)
	if text == "" || limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit]) + "…"
}

// authFileProbeMediaKind returns ("image"|"video", reason) when the model is a
// generation-only media model that cannot be validated by a chat ping probe.
// kind is empty when the model is eligible for chat probing.
func authFileProbeMediaKind(model, provider string) (kind string, reason string) {
	raw := strings.TrimSpace(model)
	if raw == "" {
		return "", ""
	}
	base := authFileProbeModelBase(raw)
	lowerBase := strings.ToLower(base)
	lowerRaw := strings.ToLower(raw)

	if info := registry.LookupModelInfo(raw, provider); info != nil {
		if strings.EqualFold(strings.TrimSpace(info.Type), registry.OpenAIImageModelType) {
			return "image", "image models cannot be tested via chat probe"
		}
		if kind := authFileProbeKindFromModalities(info); kind != "" {
			return kind, fmt.Sprintf("%s models cannot be tested via chat probe", kind)
		}
		if desc := strings.ToLower(strings.TrimSpace(info.Description)); desc != "" {
			if strings.Contains(desc, "video generation") {
				return "video", "video models cannot be tested via chat probe"
			}
			if strings.Contains(desc, "image generation") {
				return "image", "image models cannot be tested via chat probe"
			}
		}
	}

	// Built-in / well-known generation-only models (and common aliases).
	switch lowerBase {
	case "gpt-image-2", "gpt-image-1.5", "gpt-image-1", "dall-e-3", "dall-e-2",
		"grok-imagine-image", "grok-imagine-image-quality":
		return "image", "image models cannot be tested via chat probe"
	case "grok-imagine-video", "grok-imagine-video-1.5-preview":
		return "video", "video models cannot be tested via chat probe"
	}

	// Gemini / Imagen style image generators: *-image, *-image-preview, *imagen*.
	// Multimodal chat models only advertise IMAGE as an input modality and keep
	// /chat/completions (or similar) in SupportedEndpoints — those pass above.
	if strings.Contains(lowerBase, "imagen") ||
		strings.Contains(lowerBase, "image-preview") ||
		strings.HasSuffix(lowerBase, "-image") ||
		strings.Contains(lowerBase, "-image-") {
		return "image", "image models cannot be tested via chat probe"
	}
	if strings.Contains(lowerBase, "video") || strings.Contains(lowerRaw, "video") {
		return "video", "video models cannot be tested via chat probe"
	}
	return "", ""
}

func authFileProbeKindFromModalities(info *registry.ModelInfo) string {
	if info == nil {
		return ""
	}
	// Prefer endpoint metadata when present: chat-capable multimodal models stay testable.
	if hasAuthFileChatEndpoint(info.SupportedEndpoints) {
		return ""
	}
	hasImageOut := false
	hasVideoOut := false
	hasTextOut := false
	for _, m := range info.SupportedOutputModalities {
		switch strings.ToUpper(strings.TrimSpace(m)) {
		case "IMAGE":
			hasImageOut = true
		case "VIDEO":
			hasVideoOut = true
		case "TEXT":
			hasTextOut = true
		}
	}
	if hasVideoOut && !hasTextOut {
		return "video"
	}
	if hasImageOut && !hasTextOut {
		return "image"
	}
	return ""
}

func hasAuthFileChatEndpoint(endpoints []string) bool {
	for _, ep := range endpoints {
		lower := strings.ToLower(strings.TrimSpace(ep))
		if lower == "" {
			continue
		}
		if strings.Contains(lower, "/chat/completions") ||
			strings.Contains(lower, "/messages") ||
			strings.Contains(lower, "/responses") ||
			strings.Contains(lower, "generatecontent") {
			return true
		}
	}
	return false
}

// authFileProbeModelBase strips optional provider prefixes (e.g. "xai/grok-imagine-image").
func authFileProbeModelBase(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if idx := strings.LastIndex(model, "/"); idx >= 0 && idx+1 < len(model) {
		return strings.TrimSpace(model[idx+1:])
	}
	return model
}
