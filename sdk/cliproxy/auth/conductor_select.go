package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

func (m *Manager) useSchedulerFastPath() bool {
	if m == nil || m.scheduler == nil {
		return false
	}
	return isBuiltInSelector(m.selector)
}

func shouldRetrySchedulerPick(err error) bool {
	if err == nil {
		return false
	}
	var cooldownErr *modelCooldownError
	if errors.As(err, &cooldownErr) {
		return true
	}
	var authErr *Error
	if !errors.As(err, &authErr) {
		return false
	}
	return authErr.Code == "auth_not_found" || authErr.Code == "auth_unavailable"
}

func (m *Manager) routeAwareSelectionRequired(auth *Auth, routeModel string) bool {
	if auth == nil || strings.TrimSpace(routeModel) == "" {
		return false
	}
	return m.selectionModelKeyForAuth(auth, routeModel) != canonicalModelKey(routeModel)
}

func (m *Manager) pickNextLegacy(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, error) {
	if m.HomeEnabled() {
		auth, exec, _, err := m.pickNextViaHome(ctx, model, opts, tried)
		return auth, exec, err
	}

	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	disallowFreeAuth := disallowFreeAuthFromMetadata(opts.Metadata)

	m.mu.RLock()
	executor, okExecutor := m.executors[provider]
	if !okExecutor {
		m.mu.RUnlock()
		return nil, nil, &Error{Code: "executor_not_found", Message: "executor not registered"}
	}
	candidates := make([]*Auth, 0, len(m.auths))
	modelKey := strings.TrimSpace(model)
	// Always use base model name (without thinking suffix) for auth matching.
	if modelKey != "" {
		parsed := thinking.ParseSuffix(modelKey)
		if parsed.ModelName != "" {
			modelKey = strings.TrimSpace(parsed.ModelName)
		}
	}
	registryRef := registry.GetGlobalRegistry()
	for _, candidate := range m.auths {
		if candidate.Provider != provider || candidate.Disabled {
			continue
		}
		if pinnedAuthID != "" && candidate.ID != pinnedAuthID {
			continue
		}
		if disallowFreeAuth && isFreeCodexAuth(candidate) {
			continue
		}
		if _, used := tried[candidate.ID]; used {
			continue
		}
		if modelKey != "" && !m.authSupportsRouteModel(registryRef, candidate, model) {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		m.mu.RUnlock()
		return nil, nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	available, errAvailable := m.availableAuthsForRouteModel(candidates, provider, model, time.Now())
	if errAvailable != nil {
		m.mu.RUnlock()
		return nil, nil, errAvailable
	}
	selected, errPick := m.selector.Pick(ctx, provider, selectionArgForSelector(m.selector, model), opts, available)
	if errPick != nil {
		m.mu.RUnlock()
		return nil, nil, errPick
	}
	if selected == nil {
		m.mu.RUnlock()
		return nil, nil, &Error{Code: "auth_not_found", Message: "selector returned no auth"}
	}
	authCopy := selected.Clone()
	m.mu.RUnlock()
	if !selected.indexAssigned {
		m.mu.Lock()
		if current := m.auths[authCopy.ID]; current != nil && !current.indexAssigned {
			current.EnsureIndex()
			authCopy = current.Clone()
		}
		m.mu.Unlock()
	}
	return authCopy, executor, nil
}

// SelectAuthByKind selects one credential of the required kind through the configured scheduling strategy.
func (m *Manager) SelectAuthByKind(ctx context.Context, provider, model, requiredKind string, opts cliproxyexecutor.Options) (*Auth, error) {
	requiredKind = normalizeAuthKind(requiredKind)
	if requiredKind == "" {
		return nil, &Error{Code: "invalid_auth_kind", Message: "required auth kind is invalid", HTTPStatus: http.StatusBadRequest}
	}

	homeMode := m.HomeEnabled()
	homeAuthCount := homeAuthCountFromMetadata(opts.Metadata)
	tried := make(map[string]struct{})
	for {
		pickOpts := opts
		if homeMode {
			pickOpts = withHomeAuthCount(opts, homeAuthCount)
		}
		selected, _, errPick := m.pickNext(ctx, provider, model, pickOpts, tried)
		if errPick != nil {
			return nil, errPick
		}
		if selected == nil {
			return nil, &Error{Code: "auth_not_found", Message: "selector returned no auth"}
		}
		if selected.AuthKind() == requiredKind {
			return selected, nil
		}
		authID := strings.TrimSpace(selected.ID)
		if authID == "" {
			return nil, &Error{Code: "auth_not_found", Message: "selected auth has no ID"}
		}
		if _, alreadyTried := tried[authID]; alreadyTried {
			return nil, &Error{Code: "auth_not_found", Message: "selector repeatedly returned an ineligible auth"}
		}
		tried[authID] = struct{}{}
		if homeMode {
			homeAuthCount++
		}
	}
}

func (m *Manager) pickNext(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, error) {
	if m.HomeEnabled() {
		auth, exec, _, err := m.pickNextViaHome(ctx, model, opts, tried)
		return auth, exec, err
	}

	if !m.useSchedulerFastPath() {
		return m.pickNextLegacy(ctx, provider, model, opts, tried)
	}
	if strings.TrimSpace(model) != "" {
		m.mu.RLock()
		for _, candidate := range m.auths {
			if candidate == nil || candidate.Provider != provider || candidate.Disabled {
				continue
			}
			if _, used := tried[candidate.ID]; used {
				continue
			}
			if m.routeAwareSelectionRequired(candidate, model) {
				m.mu.RUnlock()
				return m.pickNextLegacy(ctx, provider, model, opts, tried)
			}
		}
		m.mu.RUnlock()
	}
	executor, okExecutor := m.Executor(provider)
	if !okExecutor {
		return nil, nil, &Error{Code: "executor_not_found", Message: "executor not registered"}
	}
	disallowFreeAuth := disallowFreeAuthFromMetadata(opts.Metadata)
	for {
		selected, errPick := m.scheduler.pickSingle(ctx, provider, model, opts, tried)
		if errPick != nil && model != "" && shouldRetrySchedulerPick(errPick) {
			m.syncScheduler()
			selected, errPick = m.scheduler.pickSingle(ctx, provider, model, opts, tried)
		}
		if errPick != nil {
			return nil, nil, errPick
		}
		if selected == nil {
			return nil, nil, &Error{Code: "auth_not_found", Message: "selector returned no auth"}
		}
		if disallowFreeAuth && isFreeCodexAuth(selected) {
			if tried == nil {
				tried = make(map[string]struct{})
			}
			tried[selected.ID] = struct{}{}
			continue
		}
		authCopy := selected.Clone()
		if !selected.indexAssigned {
			m.mu.Lock()
			if current := m.auths[authCopy.ID]; current != nil && !current.indexAssigned {
				current.EnsureIndex()
				authCopy = current.Clone()
			}
			m.mu.Unlock()
		}
		return authCopy, executor, nil
	}
}

func (m *Manager) pickNextMixedLegacy(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, string, error) {
	if m.HomeEnabled() {
		return m.pickNextViaHome(ctx, model, opts, tried)
	}

	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	disallowFreeAuth := disallowFreeAuthFromMetadata(opts.Metadata)

	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		p := strings.TrimSpace(strings.ToLower(provider))
		if p == "" {
			continue
		}
		providerSet[p] = struct{}{}
	}
	if len(providerSet) == 0 {
		return nil, nil, "", &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}

	m.mu.RLock()
	candidates := make([]*Auth, 0, len(m.auths))
	modelKey := strings.TrimSpace(model)
	// Always use base model name (without thinking suffix) for auth matching.
	if modelKey != "" {
		parsed := thinking.ParseSuffix(modelKey)
		if parsed.ModelName != "" {
			modelKey = strings.TrimSpace(parsed.ModelName)
		}
	}
	registryRef := registry.GetGlobalRegistry()
	for _, candidate := range m.auths {
		if candidate == nil || candidate.Disabled {
			continue
		}
		if pinnedAuthID != "" && candidate.ID != pinnedAuthID {
			continue
		}
		if disallowFreeAuth && isFreeCodexAuth(candidate) {
			continue
		}
		providerKey := strings.TrimSpace(strings.ToLower(candidate.Provider))
		if providerKey == "" {
			continue
		}
		if _, ok := providerSet[providerKey]; !ok {
			continue
		}
		if _, used := tried[candidate.ID]; used {
			continue
		}
		if _, ok := m.executors[providerKey]; !ok {
			continue
		}
		if modelKey != "" && !m.authSupportsRouteModel(registryRef, candidate, model) {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		m.mu.RUnlock()
		return nil, nil, "", &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	available, errAvailable := m.availableAuthsForRouteModel(candidates, "mixed", model, time.Now())
	if errAvailable != nil {
		m.mu.RUnlock()
		return nil, nil, "", errAvailable
	}
	selected, errPick := m.selector.Pick(ctx, "mixed", selectionArgForSelector(m.selector, model), opts, available)
	if errPick != nil {
		m.mu.RUnlock()
		return nil, nil, "", errPick
	}
	if selected == nil {
		m.mu.RUnlock()
		return nil, nil, "", &Error{Code: "auth_not_found", Message: "selector returned no auth"}
	}
	providerKey := strings.TrimSpace(strings.ToLower(selected.Provider))
	executor, okExecutor := m.executors[providerKey]
	if !okExecutor {
		m.mu.RUnlock()
		return nil, nil, "", &Error{Code: "executor_not_found", Message: "executor not registered"}
	}
	authCopy := selected.Clone()
	m.mu.RUnlock()
	if !selected.indexAssigned {
		m.mu.Lock()
		if current := m.auths[authCopy.ID]; current != nil && !current.indexAssigned {
			current.EnsureIndex()
			authCopy = current.Clone()
		}
		m.mu.Unlock()
	}
	return authCopy, executor, providerKey, nil
}

func (m *Manager) pickNextMixed(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, string, error) {
	if m.HomeEnabled() {
		return m.pickNextViaHome(ctx, model, opts, tried)
	}

	if opts.IsAuto {
		return m.pickNextMixedLegacy(ctx, providers, "", opts, tried)
	}

	if !m.useSchedulerFastPath() {
		return m.pickNextMixedLegacy(ctx, providers, model, opts, tried)
	}

	eligibleProviders := make([]string, 0, len(providers))
	seenProviders := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		providerKey := strings.TrimSpace(strings.ToLower(provider))
		if providerKey == "" {
			continue
		}
		if _, seen := seenProviders[providerKey]; seen {
			continue
		}
		if _, okExecutor := m.Executor(providerKey); !okExecutor {
			continue
		}
		seenProviders[providerKey] = struct{}{}
		eligibleProviders = append(eligibleProviders, providerKey)
	}
	if len(eligibleProviders) == 0 {
		return nil, nil, "", &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	if strings.TrimSpace(model) != "" {
		providerSet := make(map[string]struct{}, len(eligibleProviders))
		for _, providerKey := range eligibleProviders {
			providerSet[providerKey] = struct{}{}
		}
		m.mu.RLock()
		for _, candidate := range m.auths {
			if candidate == nil || candidate.Disabled {
				continue
			}
			if _, ok := providerSet[strings.TrimSpace(strings.ToLower(candidate.Provider))]; !ok {
				continue
			}
			if _, used := tried[candidate.ID]; used {
				continue
			}
			if m.routeAwareSelectionRequired(candidate, model) {
				m.mu.RUnlock()
				return m.pickNextMixedLegacy(ctx, providers, model, opts, tried)
			}
		}
		m.mu.RUnlock()
	}

	disallowFreeAuth := disallowFreeAuthFromMetadata(opts.Metadata)
	for {
		selected, providerKey, errPick := m.scheduler.pickMixed(ctx, eligibleProviders, model, opts, tried)
		if errPick != nil && model != "" && shouldRetrySchedulerPick(errPick) {
			m.syncScheduler()
			selected, providerKey, errPick = m.scheduler.pickMixed(ctx, eligibleProviders, model, opts, tried)
		}
		if errPick != nil {
			return nil, nil, "", errPick
		}
		if selected == nil {
			return nil, nil, "", &Error{Code: "auth_not_found", Message: "selector returned no auth"}
		}
		if disallowFreeAuth && isFreeCodexAuth(selected) {
			if tried == nil {
				tried = make(map[string]struct{})
			}
			tried[selected.ID] = struct{}{}
			continue
		}
		executor, okExecutor := m.Executor(providerKey)
		if !okExecutor {
			return nil, nil, "", &Error{Code: "executor_not_found", Message: "executor not registered"}
		}
		authCopy := selected.Clone()
		if !selected.indexAssigned {
			m.mu.Lock()
			if current := m.auths[authCopy.ID]; current != nil && !current.indexAssigned {
				current.EnsureIndex()
				authCopy = current.Clone()
			}
			m.mu.Unlock()
		}
		return authCopy, executor, providerKey, nil
	}
}

type homeErrorEnvelope struct {
	Error *homeErrorDetail `json:"error"`
}

type homeErrorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

const (
	homeUpstreamModelAttributeKey     = "home_upstream_model"
	homeRequestRetryExceededErrorCode = "request_retry_exceeded"
)

func isHomeRequestRetryExceededError(err error) bool {
	var authErr *Error
	if !errors.As(err, &authErr) || authErr == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(authErr.Code), homeRequestRetryExceededErrorCode)
}

func shouldReturnLastErrorOnPickFailure(homeMode bool, lastErr error, errPick error) bool {
	if lastErr == nil {
		return false
	}
	if !homeMode {
		return true
	}
	return isHomeRequestRetryExceededError(errPick)
}

type homeAuthDispatchResponse struct {
	Model      string `json:"model"`
	Provider   string `json:"provider"`
	AuthIndex  string `json:"auth_index"`
	UserAPIKey string `json:"user_api_key"`
	Auth       Auth   `json:"auth"`
}

func setHomeUserAPIKeyOnGinContext(ctx context.Context, apiKey string) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" || ctx == nil {
		return
	}
	ginCtx, ok := ctx.Value("gin").(interface{ Set(string, any) })
	if !ok || ginCtx == nil {
		return
	}
	ginCtx.Set("userApiKey", apiKey)
}

func homeDispatchHeaders(ctx context.Context, headers http.Header) http.Header {
	apiKey, ok := homeQueryCredentialFromContext(ctx)
	if !ok {
		return headers
	}
	out := headers.Clone()
	if out == nil {
		out = http.Header{}
	}
	if out.Get("Authorization") != "" || out.Get("X-Goog-Api-Key") != "" || out.Get("X-Api-Key") != "" {
		return out
	}
	out.Set("X-Goog-Api-Key", apiKey)
	return out
}

func homeQueryCredentialFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	if queryCtx, ok := ctx.Value("gin").(interface{ Query(string) string }); ok && queryCtx != nil {
		if apiKey := strings.TrimSpace(queryCtx.Query("key")); apiKey != "" {
			return apiKey, true
		}
		if apiKey := strings.TrimSpace(queryCtx.Query("auth_token")); apiKey != "" {
			return apiKey, true
		}
	}
	ginCtx, ok := ctx.Value("gin").(interface{ Get(string) (any, bool) })
	if !ok || ginCtx == nil {
		return "", false
	}
	rawMetadata, ok := ginCtx.Get("accessMetadata")
	if !ok {
		return "", false
	}
	source := accessMetadataSource(rawMetadata)
	if source != "query-key" && source != "query-auth-token" {
		return "", false
	}
	rawAPIKey, ok := ginCtx.Get("userApiKey")
	if !ok {
		return "", false
	}
	apiKey := contextStringValue(rawAPIKey)
	if apiKey == "" {
		return "", false
	}
	return apiKey, true
}

func accessMetadataSource(raw any) string {
	switch v := raw.(type) {
	case map[string]string:
		return strings.TrimSpace(v["source"])
	case map[string]any:
		return contextStringValue(v["source"])
	default:
		return ""
	}
}

func contextStringValue(raw any) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		return ""
	}
}

func homeExecutionSessionIDFromMetadata(meta map[string]any) string {
	if len(meta) == 0 {
		return ""
	}
	raw, ok := meta[cliproxyexecutor.ExecutionSessionMetadataKey]
	if !ok || raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
}

func (m *Manager) clearHomeRuntimeAuths() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.clearHomeRuntimeAuthsLocked()
	m.mu.Unlock()
}

func (m *Manager) removeRefreshSchedule(authID string) {
	if m == nil || authID == "" {
		return
	}
	m.mu.RLock()
	loop := m.refreshLoop
	m.mu.RUnlock()
	if loop == nil {
		return
	}
	loop.remove(authID)
}

func (m *Manager) clearHomeRuntimeAuthsLocked() {
	if m == nil {
		return
	}
	m.homeRuntimeAuths = make(map[string]map[string]*Auth)
}

func (m *Manager) clearHomeRuntimeAuthsForSessionLocked(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if m == nil || sessionID == "" {
		return
	}
	delete(m.homeRuntimeAuths, sessionID)
}

func (m *Manager) rememberHomeRuntimeAuth(sessionID string, auth *Auth) {
	sessionID = strings.TrimSpace(sessionID)
	authID := ""
	if auth != nil {
		authID = strings.TrimSpace(auth.ID)
	}
	if m == nil || auth == nil || sessionID == "" || authID == "" || !authWebsocketsEnabled(auth) {
		return
	}
	m.mu.Lock()
	if m.homeRuntimeAuths == nil {
		m.homeRuntimeAuths = make(map[string]map[string]*Auth)
	}
	sessionAuths := m.homeRuntimeAuths[sessionID]
	if sessionAuths == nil {
		sessionAuths = make(map[string]*Auth)
		m.homeRuntimeAuths[sessionID] = sessionAuths
	}
	sessionAuths[authID] = auth.Clone()
	m.mu.Unlock()
}

func (m *Manager) homeRuntimeAuthByID(sessionID string, authID string) (*Auth, ProviderExecutor, string, bool) {
	sessionID = strings.TrimSpace(sessionID)
	authID = strings.TrimSpace(authID)
	if m == nil || sessionID == "" || authID == "" {
		return nil, nil, "", false
	}
	m.mu.RLock()
	sessionAuths := m.homeRuntimeAuths[sessionID]
	auth := sessionAuths[authID]
	m.mu.RUnlock()
	if auth == nil || !authWebsocketsEnabled(auth) {
		return nil, nil, "", false
	}
	providerKey := strings.ToLower(strings.TrimSpace(auth.Provider))
	if providerKey == "" {
		return nil, nil, "", false
	}
	executor, ok := m.Executor(providerKey)
	if !ok && auth.Attributes != nil && strings.TrimSpace(auth.Attributes["base_url"]) != "" {
		executor, ok = m.Executor("openai-compatibility")
		if ok {
			providerKey = "openai-compatibility"
		}
	}
	if !ok {
		return nil, nil, "", false
	}
	return auth.Clone(), executor, providerKey, true
}

func (m *Manager) pickNextViaHome(ctx context.Context, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, string, error) {
	if m == nil {
		return nil, nil, "", &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	executionSessionID := homeExecutionSessionIDFromMetadata(opts.Metadata)
	count := homeAuthCountFromMetadata(opts.Metadata)
	if cliproxyexecutor.DownstreamWebsocket(ctx) && executionSessionID != "" && count <= 1 {
		if pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata); pinnedAuthID != "" {
			_, alreadyTried := tried[pinnedAuthID]
			if !alreadyTried {
				if auth, executor, providerKey, ok := m.homeRuntimeAuthByID(executionSessionID, pinnedAuthID); ok {
					return auth, executor, providerKey, nil
				}
			}
		}
	}

	client := currentHomeDispatcher()
	if client == nil || !client.HeartbeatOK() {
		return nil, nil, "", &Error{Code: "home_unavailable", Message: "home control center unavailable", HTTPStatus: http.StatusServiceUnavailable}
	}

	requestedModel := requestedModelFromMetadata(opts.Metadata, model)
	sessionID := ExtractSessionID(opts.Headers, opts.OriginalRequest, opts.Metadata)
	dispatchHeaders := homeDispatchHeaders(ctx, opts.Headers)

	raw, err := client.RPopAuth(ctx, requestedModel, sessionID, dispatchHeaders, count)
	if err != nil {
		return nil, nil, "", &Error{Code: "auth_not_found", Message: err.Error(), HTTPStatus: http.StatusServiceUnavailable}
	}

	var env homeErrorEnvelope
	if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal == nil && env.Error != nil {
		code := strings.TrimSpace(env.Error.Type)
		if code == "" {
			code = strings.TrimSpace(env.Error.Code)
		}
		msg := strings.TrimSpace(env.Error.Message)
		if msg == "" {
			msg = "home returned error"
		}
		status := http.StatusBadGateway
		switch strings.ToLower(code) {
		case "model_not_found":
			status = http.StatusNotFound
		case "authentication_error", "unauthorized":
			status = http.StatusUnauthorized
		}
		return nil, nil, "", &Error{Code: code, Message: msg, HTTPStatus: status}
	}

	var dispatch homeAuthDispatchResponse
	if errUnmarshal := json.Unmarshal(raw, &dispatch); errUnmarshal != nil {
		return nil, nil, "", &Error{Code: "invalid_auth", Message: "home returned invalid auth payload", HTTPStatus: http.StatusBadGateway}
	}
	setHomeUserAPIKeyOnGinContext(ctx, dispatch.UserAPIKey)
	auth := dispatch.Auth
	if strings.TrimSpace(auth.ID) == "" {
		// Backward compatibility: older home instances returned the auth directly.
		if errUnmarshal := json.Unmarshal(raw, &auth); errUnmarshal != nil {
			return nil, nil, "", &Error{Code: "invalid_auth", Message: "home returned invalid auth payload", HTTPStatus: http.StatusBadGateway}
		}
	}
	if upstreamModel := strings.TrimSpace(dispatch.Model); upstreamModel != "" {
		if auth.Attributes == nil {
			auth.Attributes = make(map[string]string, 1)
		}
		auth.Attributes[homeUpstreamModelAttributeKey] = upstreamModel
	}
	if strings.TrimSpace(auth.ID) == "" {
		return nil, nil, "", &Error{Code: "invalid_auth", Message: "home returned auth without id", HTTPStatus: http.StatusBadGateway}
	}
	if _, alreadyTried := tried[auth.ID]; alreadyTried {
		return nil, nil, "", &Error{Code: homeRequestRetryExceededErrorCode, Message: "home returned already-tried auth", HTTPStatus: http.StatusUnauthorized}
	}
	providerKey := strings.ToLower(strings.TrimSpace(auth.Provider))
	if providerKey == "" {
		return nil, nil, "", &Error{Code: "invalid_auth", Message: "home returned auth without provider", HTTPStatus: http.StatusBadGateway}
	}

	homeAuthIndex := strings.TrimSpace(dispatch.AuthIndex)
	if homeAuthIndex != "" {
		auth.Index = homeAuthIndex
		auth.indexAssigned = true
	} else {
		auth.EnsureIndex()
	}

	executor, ok := m.Executor(providerKey)
	if !ok && auth.Attributes != nil && strings.TrimSpace(auth.Attributes["base_url"]) != "" {
		executor, ok = m.Executor("openai-compatibility")
		if ok {
			providerKey = "openai-compatibility"
		}
	}
	if !ok {
		return nil, nil, "", &Error{Code: "executor_not_found", Message: "executor not registered", HTTPStatus: http.StatusBadGateway}
	}

	authCopy := auth.Clone()
	if cliproxyexecutor.DownstreamWebsocket(ctx) && executionSessionID != "" && authWebsocketsEnabled(authCopy) {
		m.rememberHomeRuntimeAuth(executionSessionID, authCopy)
	}
	return authCopy, executor, providerKey, nil
}

func requestedModelFromMetadata(metadata map[string]any, fallback string) string {
	if metadata != nil {
		if v, ok := metadata[cliproxyexecutor.RequestedModelMetadataKey]; ok {
			switch typed := v.(type) {
			case string:
				if trimmed := strings.TrimSpace(typed); trimmed != "" {
					return trimmed
				}
			case []byte:
				if trimmed := strings.TrimSpace(string(typed)); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	fallback = strings.TrimSpace(fallback)
	if fallback == "" {
		return "unknown"
	}
	return fallback
}

func (m *Manager) findAllAntigravityCreditsCandidateAuths(ctx context.Context, routeModel string, opts cliproxyexecutor.Options) ([]creditsCandidateEntry, error) {
	if m == nil {
		return nil, nil
	}
	if !strings.Contains(strings.ToLower(strings.TrimSpace(routeModel)), "claude") {
		return nil, nil
	}
	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	var candidates []creditsCandidateEntry
	m.mu.RLock()
	for _, auth := range m.auths {
		if auth == nil || auth.Disabled || auth.Status == StatusDisabled {
			continue
		}
		if pinnedAuthID != "" && auth.ID != pinnedAuthID {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(auth.Provider), "antigravity") {
			continue
		}
		providerKey := strings.TrimSpace(strings.ToLower(auth.Provider))
		executor, ok := m.executors[providerKey]
		if !ok {
			continue
		}
		candidates = append(candidates, creditsCandidateEntry{
			auth:     auth.Clone(),
			executor: executor,
			provider: providerKey,
		})
	}
	m.mu.RUnlock()

	var known []creditsCandidateEntry
	var unknown []creditsCandidateEntry
	for _, candidate := range candidates {
		hint, okHint, errHint := GetAntigravityCreditsHintRequired(ctx, candidate.auth.ID)
		if errHint != nil {
			return nil, antigravityCreditsKVUnavailableError(errHint)
		}
		if okHint && hint.Known {
			if !hint.Available {
				continue
			}
			known = append(known, candidate)
			continue
		}
		unknown = append(unknown, candidate)
	}
	sort.Slice(known, func(i, j int) bool { return known[i].auth.ID < known[j].auth.ID })
	sort.Slice(unknown, func(i, j int) bool { return unknown[i].auth.ID < unknown[j].auth.ID })
	return append(known, unknown...), nil
}

func hasAntigravityProvider(providers []string) bool {
	for _, p := range providers {
		if strings.EqualFold(strings.TrimSpace(p), "antigravity") {
			return true
		}
	}
	return false
}

func antigravityCreditsKVUnavailableError(cause error) error {
	if cause == nil {
		return &Error{Code: "home_kv_unavailable", Message: "home kv store unavailable", HTTPStatus: http.StatusServiceUnavailable}
	}
	return &Error{Code: "home_kv_unavailable", Message: "home kv store unavailable: " + cause.Error(), HTTPStatus: http.StatusServiceUnavailable}
}

type creditsCandidateEntry struct {
	auth     *Auth
	executor ProviderExecutor
	provider string
}

func shouldAttemptAntigravityCreditsFallback(m *Manager, lastErr error, providers []string) bool {
	status := statusCodeFromError(lastErr)
	log.WithFields(log.Fields{
		"lastErr":   errorString(lastErr),
		"status":    status,
		"providers": providers,
	}).Debug("shouldAttemptAntigravityCreditsFallback")
	if m == nil || lastErr == nil {
		return false
	}
	if len(providers) > 0 {
		hasAntigravity := false
		for _, p := range providers {
			if strings.EqualFold(strings.TrimSpace(p), "antigravity") {
				hasAntigravity = true
				break
			}
		}
		if !hasAntigravity {
			return false
		}
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil || !cfg.QuotaExceeded.AntigravityCredits {
		return false
	}
	switch status {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return true
	case 0:
		var authErr *Error
		if errors.As(lastErr, &authErr) && authErr != nil {
			return authErr.Code == "auth_not_found" || authErr.Code == "auth_unavailable" || authErr.Code == "model_cooldown"
		}
		var cooldownErr *modelCooldownError
		if errors.As(lastErr, &cooldownErr) {
			return true
		}
		return false
	default:
		return false
	}
}

func (m *Manager) tryAntigravityCreditsExecute(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, bool, error) {
	routeModel := req.Model
	candidates, errCandidates := m.findAllAntigravityCreditsCandidateAuths(ctx, routeModel, opts)
	if errCandidates != nil {
		return cliproxyexecutor.Response{}, false, errCandidates
	}
	for _, c := range candidates {
		if ctx.Err() != nil {
			return cliproxyexecutor.Response{}, false, nil
		}
		creditsCtx := WithAntigravityCredits(ctx)
		if rt := m.roundTripperFor(c.auth); rt != nil {
			creditsCtx = context.WithValue(creditsCtx, roundTripperContextKey{}, rt)
			creditsCtx = context.WithValue(creditsCtx, "cliproxy.roundtripper", rt)
		}
		creditsOpts := ensureRequestedModelMetadata(opts, routeModel)
		creditsCtx = contextWithRequestedModelAlias(creditsCtx, creditsOpts, routeModel)
		publishSelectedAuthMetadata(creditsOpts.Metadata, c.auth.ID)
		models := m.executionModelCandidates(c.auth, routeModel)
		if len(models) == 0 {
			continue
		}
		for _, upstreamModel := range models {
			resultModel := m.stateModelForExecution(c.auth, routeModel, upstreamModel, len(models) > 1)
			execReq := req
			execReq.Model = upstreamModel
			resp, errExec := c.executor.Execute(creditsCtx, c.auth, execReq, creditsOpts)
			result := Result{AuthID: c.auth.ID, Provider: c.provider, Model: resultModel, Success: errExec == nil}
			if errExec != nil {
				result.Error = newErrorFromExecution(errExec)
				if ra := retryAfterFromError(errExec); ra != nil {
					result.RetryAfter = ra
				}
				m.MarkResult(creditsCtx, result)
				continue
			}
			m.MarkResult(creditsCtx, result)
			return resp, true, nil
		}
	}
	return cliproxyexecutor.Response{}, false, nil
}

func (m *Manager) tryAntigravityCreditsExecuteStream(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, bool, error) {
	routeModel := req.Model
	candidates, errCandidates := m.findAllAntigravityCreditsCandidateAuths(ctx, routeModel, opts)
	if errCandidates != nil {
		return nil, false, errCandidates
	}
	for _, c := range candidates {
		if ctx.Err() != nil {
			return nil, false, nil
		}
		creditsCtx := WithAntigravityCredits(ctx)
		if rt := m.roundTripperFor(c.auth); rt != nil {
			creditsCtx = context.WithValue(creditsCtx, roundTripperContextKey{}, rt)
			creditsCtx = context.WithValue(creditsCtx, "cliproxy.roundtripper", rt)
		}
		creditsOpts := ensureRequestedModelMetadata(opts, routeModel)
		publishSelectedAuthMetadata(creditsOpts.Metadata, c.auth.ID)
		models := m.executionModelCandidates(c.auth, routeModel)
		if len(models) == 0 {
			continue
		}
		result, errStream := m.executeStreamWithModelPool(creditsCtx, c.executor, c.auth, c.provider, req, creditsOpts, routeModel, models, len(models) > 1)
		if errStream == nil {
			return result, true, nil
		}
	}
	return nil, false, nil
}
