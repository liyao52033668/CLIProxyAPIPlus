package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codearts"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	codeartsChatURL   = "https://snap-access.cn-north-4.myhuaweicloud.com/v1/chat/chat"
	codeArtsUserAgent = "DevKit-VSCode:huaweicloud.vscode-codebot|CodeArts Agent:D1"

	// codeartsChatV2URL is the newer OpenAI-compatible chat endpoint used by the
	// official CodeArts Agent kernel (InferHub-registered model IDs, case-sensitive).
	codeartsChatV2URL = "https://snap-access.cn-north-4.myhuaweicloud.com/api/v2/chat/completions"

	codeartsMarketplaceURL = "https://marketplace.visualstudio.com/_apis/public/gallery/extensionquery"
	// codeartsVSCodeUpdateURL returns the latest stable VSCode releases, newest first.
	codeartsVSCodeUpdateURL = "https://update.code.visualstudio.com/api/releases/stable"

	// Static header values sent to the CodeArts chat API.
	codeartsAgentType       = "ChatAgent"
	codeartsHeartbeatEnable = "true"
	codeartsIdeName         = "CodeArts Agent"
	codeartsIsConfidential  = "false"
	codeartsPluginName      = "snap_vscode"
	codeartsLanguage        = "zh-cn"
	// codeartsPluginVersionDefault is the fallback plugin version used when the
	// marketplace fetch fails.
	codeartsPluginVersionDefault = "26.8.203"
	// codeartsIdeVersionDefault is the fallback host IDE (VSCode) version used
	// when the update endpoint fetch fails.
	codeartsIdeVersionDefault = "1.135.0"

	// codeartsRemoteVersionRefresh is how often the latest plugin and host IDE
	// versions are re-fetched from their remote sources.
	codeartsRemoteVersionRefresh = 24 * time.Hour
)

// codeartsChatURLParsed is the parsed v1 chat endpoint used to build request
// signatures. SignRequest derives the host, canonical URI and query from
// req.URL, so requests prepared for the v1 path must carry a non-nil URL.
var codeartsChatURLParsed, _ = url.Parse(codeartsChatURL)

// CodeArtsExecutor executes chat completions against the HuaweiCloud CodeArts API.
type CodeArtsExecutor struct {
	cfg *config.Config
}

// NewCodeArtsExecutor constructs a new executor instance.
func NewCodeArtsExecutor(cfg *config.Config) *CodeArtsExecutor {
	return &CodeArtsExecutor{cfg: cfg}
}

// Identifier returns the executor's provider key.
func (e *CodeArtsExecutor) Identifier() string { return "codearts" }

var (
	codeArtsPluginVersionMu    sync.Mutex
	codeArtsPluginVersionVal   string
	codeArtsPluginVersionFresh time.Time
)

var (
	codeArtsIdeVersionMu    sync.Mutex
	codeArtsIdeVersionVal   string
	codeArtsIdeVersionFresh time.Time
)

// codeArtsPluginVersion returns the latest CodeArts plugin version fetched from
// the VSCode marketplace, cached for codeartsRemoteVersionRefresh. Falls back to
// the last known value (or the default) when the fetch fails. The proxy runs on
// a server that does not install VSCode, so the version is resolved remotely.
func (e *CodeArtsExecutor) codeArtsPluginVersion() string {
	codeArtsPluginVersionMu.Lock()
	defer codeArtsPluginVersionMu.Unlock()

	if time.Since(codeArtsPluginVersionFresh) < codeartsRemoteVersionRefresh {
		if codeArtsPluginVersionVal != "" {
			return codeArtsPluginVersionVal
		}
		return codeartsPluginVersionDefault
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	v, err := fetchLatestCodeArtsPluginVersion(ctx, e.cfg)
	codeArtsPluginVersionFresh = time.Now()
	if err != nil || v == "" {
		log.Warnf("codearts: failed to fetch plugin version from marketplace: %v", err)
	} else {
		codeArtsPluginVersionVal = v
		log.Infof("codearts: using latest plugin version %s from marketplace", v)
	}

	if codeArtsPluginVersionVal != "" {
		return codeArtsPluginVersionVal
	}
	return codeartsPluginVersionDefault
}

// fetchLatestCodeArtsPluginVersion queries the VSCode marketplace for the latest
// huaweicloud.vscode-codebot extension version.
func fetchLatestCodeArtsPluginVersion(ctx context.Context, cfg *config.Config) (string, error) {
	const payload = `{"filters":[{"criteria":[{"filterType":7,"value":"huaweicloud.vscode-codebot"}],"pageNumber":1,"pageSize":10,"sortBy":0,"sortOrder":0}],"flags":914}`

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codeartsMarketplaceURL, bytes.NewReader([]byte(payload)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json;api-version=3.0-preview.1")

	client := helps.NewProxyAwareHTTPClient(ctx, cfg, nil, 10*time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("marketplace returned %d", resp.StatusCode)
	}

	ext := gjson.GetBytes(body, "results.0.extensions.0")
	if !ext.Exists() {
		return "", fmt.Errorf("marketplace returned no extension")
	}
	if ext.Get("publisher.publisherName").String() != "HuaweiCloud" || ext.Get("extensionName").String() != "vscode-codebot" {
		return "", fmt.Errorf("marketplace returned unexpected extension")
	}
	return ext.Get("versions.0.version").String(), nil
}

// codeArtsIdeVersion returns the latest stable VSCode version fetched from
// Microsoft's update endpoint, cached for codeartsRemoteVersionRefresh. The
// CodeArts upstream uses it as the host IDE version; the proxy runs on a server
// without VSCode, so the version is resolved remotely.
func (e *CodeArtsExecutor) codeArtsIdeVersion() string {
	codeArtsIdeVersionMu.Lock()
	defer codeArtsIdeVersionMu.Unlock()

	if time.Since(codeArtsIdeVersionFresh) < codeartsRemoteVersionRefresh {
		if codeArtsIdeVersionVal != "" {
			return codeArtsIdeVersionVal
		}
		return codeartsIdeVersionDefault
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	v, err := fetchLatestVSCodeVersion(ctx, e.cfg)
	codeArtsIdeVersionFresh = time.Now()
	if err != nil || v == "" {
		log.Warnf("codearts: failed to fetch latest VSCode version: %v", err)
	} else {
		codeArtsIdeVersionVal = v
		log.Infof("codearts: using latest VSCode version %s from update endpoint", v)
	}

	if codeArtsIdeVersionVal != "" {
		return codeArtsIdeVersionVal
	}
	return codeartsIdeVersionDefault
}

// fetchLatestVSCodeVersion queries Microsoft's update endpoint for the latest
// stable VSCode release version.
func fetchLatestVSCodeVersion(ctx context.Context, cfg *config.Config) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, codeartsVSCodeUpdateURL, nil)
	if err != nil {
		return "", err
	}
	client := helps.NewProxyAwareHTTPClient(ctx, cfg, nil, 10*time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("VSCode update endpoint returned %d", resp.StatusCode)
	}
	versions := gjson.ParseBytes(body).Array()
	if len(versions) == 0 || versions[0].String() == "" {
		return "", fmt.Errorf("VSCode update endpoint returned no versions")
	}
	return versions[0].String(), nil
}

// PrepareRequest sets CodeArts-specific headers and signs the request.
func (e *CodeArtsExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if auth == nil || auth.Metadata == nil {
		return fmt.Errorf("codearts: missing auth metadata")
	}

	ak, _ := auth.Metadata["ak"].(string)
	sk, _ := auth.Metadata["sk"].(string)
	securityToken, _ := auth.Metadata["security_token"].(string)

	if ak == "" || sk == "" {
		return fmt.Errorf("codearts: missing AK/SK credentials")
	}

	var bodyBytes []byte
	if req.Body != nil {
		bodyBytes, _ = io.ReadAll(req.Body)
		req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		req.ContentLength = int64(len(bodyBytes))
	}

	traceID := generateTraceID()

	req.Header.Set("User-Agent", codeArtsUserAgent)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Agent-Type", codeartsAgentType)
	req.Header.Set("Client-Version", "Vscode_"+e.codeArtsPluginVersion())
	req.Header.Set("Heartbeat-Enable", codeartsHeartbeatEnable)
	req.Header.Set("Ide-Name", codeartsIdeName)
	req.Header.Set("Ide-Version", e.codeArtsIdeVersion())
	req.Header.Set("Is-Confidential", codeartsIsConfidential)
	req.Header.Set("Plugin-Name", codeartsPluginName)
	req.Header.Set("Plugin-Version", e.codeArtsPluginVersion())
	req.Header.Set("X-Language", codeartsLanguage)
	req.Header.Set("X-Snap-Traceid", traceID)

	codearts.SignRequest(req, bodyBytes, ak, sk, securityToken)

	log.Debugf("codearts: signing request url=%s, body_len=%d, ak=%s, headers=%v",
		req.URL.String(), len(bodyBytes), ak[:min(4, len(ak))]+"...", req.Header)
	return nil
}

// HttpRequest executes a signed HTTP request to CodeArts.
func (e *CodeArtsExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	client := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 5*time.Minute)

	if err := e.PrepareRequest(req, auth); err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codearts: request failed: %w", err)
	}
	return resp, nil
}

// buildCodeArtsV2Request builds the signed headers for /api/v2/chat/completions.
// Auth: x-auth-token (STS security token) + AK/SK SDK-HMAC-SHA256 signature,
// no Agent-Type header (matching the official AgentKernel).
func (e *CodeArtsExecutor) buildCodeArtsV2Request(auth *cliproxyauth.Auth, bodyBytes []byte, traceID string) (http.Header, error) {
	if auth == nil || auth.Metadata == nil {
		return nil, fmt.Errorf("codearts: missing auth metadata")
	}
	ak, _ := auth.Metadata["ak"].(string)
	sk, _ := auth.Metadata["sk"].(string)
	securityToken, _ := auth.Metadata["security_token"].(string)
	if ak == "" || sk == "" {
		return nil, fmt.Errorf("codearts: missing AK/SK credentials")
	}
	if traceID == "" {
		traceID = generateTraceID()
	}

	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "text/event-stream")
	h.Set("x-auth-token", securityToken)
	h.Set("x-snap-traceid", traceID)
	h.Set("X-Language", codeartsLanguage)
	h.Set("app-id", "CodeAgent3.0")
	h.Set("is_confidential", codeartsIsConfidential)

	u, err := url.Parse(codeartsChatV2URL)
	if err != nil {
		return nil, fmt.Errorf("codearts: parse v2 url: %w", err)
	}
	tmpReq := &http.Request{Header: h, URL: u, Method: http.MethodPost}
	codearts.SignRequest(tmpReq, bodyBytes, ak, sk, securityToken)
	return tmpReq.Header, nil
}

// sendCodeArtsChat sends the chat request preferring the v2 endpoint and
// falling back to the legacy v1 endpoint when v2 is unavailable.
func (e *CodeArtsExecutor) sendCodeArtsChat(ctx context.Context, auth *cliproxyauth.Auth, v1Headers http.Header, v1Payload []byte, v2Headers http.Header, v2Payload []byte) (*http.Response, string, error) {
	if len(v2Payload) > 0 && v2Headers != nil {
		helps.RecordUpstreamRequest(ctx, e.cfg, auth, "codearts", http.MethodPost, codeartsChatV2URL, v2Headers.Clone(), v2Payload)
		resp, errDo := helps.DoStream(ctx, e.cfg, helps.UpstreamRequest{
			Provider:       e.Identifier(),
			Auth:           auth,
			Method:         http.MethodPost,
			URL:            codeartsChatV2URL,
			Headers:        v2Headers,
			Body:           v2Payload,
			Client:         helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0),
			SkipRequestLog: true,
		})
		if errDo == nil {
			log.Debugf("codearts: using v2 chat endpoint")
			return resp, codeartsChatV2URL, nil
		}
		log.Warnf("codearts: v2 chat endpoint failed (%v), falling back to v1", errDo)
	}

	helps.RecordUpstreamRequest(ctx, e.cfg, auth, "codearts", http.MethodPost, codeartsChatURL, v1Headers.Clone(), v1Payload)
	resp, errDo := helps.DoStream(ctx, e.cfg, helps.UpstreamRequest{
		Provider:       e.Identifier(),
		Auth:           auth,
		Method:         http.MethodPost,
		URL:            codeartsChatURL,
		Headers:        v1Headers,
		Body:           v1Payload,
		Client:         helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0),
		SkipRequestLog: true,
	})
	if errDo != nil {
		if ue, ok := errDo.(helps.UpstreamStatusError); ok {
			return nil, codeartsChatURL, statusErr{code: ue.Code, msg: fmt.Sprintf("codearts: API returned %d: %s", ue.Code, ue.Msg)}
		}
		return nil, codeartsChatURL, errDo
	}
	return resp, codeartsChatURL, nil
}

const (
	// codeartsModelsCacheTTL is the cache window for the dynamically fetched model list.
	codeartsModelsCacheTTL = time.Hour
	// codeartsModelsFailCooldown is the negative cache window applied after a failed
	// fetch to avoid hammering the upstream agent-center API.
	codeartsModelsFailCooldown = 5 * time.Minute
)

// codeartsModelsCacheEntry caches the fetched model list for one account.
type codeartsModelsCacheEntry struct {
	models   []*registry.ModelInfo
	fetched  time.Time
	lastFail time.Time
}

// codeartsModelsCache holds the per-account dynamic model cache (the model list
// is account-specific because each account resolves its own default agent).
var (
	codeartsModelsCacheMu sync.RWMutex
	codeartsModelsCache   = make(map[string]*codeartsModelsCacheEntry)
)

func codeArtsModelsCacheKey(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.ID != "" {
		return auth.ID
	}
	return auth.FileName
}

// codeArtsModelsMarkFail records a failed fetch so the negative cache cooldown
// starts; the next call within the cooldown window returns the static fallback
// without touching the upstream API.
func codeArtsModelsMarkFail(cacheKey string, now time.Time) {
	codeartsModelsCacheMu.Lock()
	if entry := codeartsModelsCache[cacheKey]; entry != nil {
		entry.lastFail = now
	} else {
		codeartsModelsCache[cacheKey] = &codeartsModelsCacheEntry{lastFail: now}
	}
	codeartsModelsCacheMu.Unlock()
}

func codeArtsModelsCacheStore(cacheKey string, now time.Time, models []*registry.ModelInfo) {
	codeartsModelsCacheMu.Lock()
	codeartsModelsCache[cacheKey] = &codeartsModelsCacheEntry{models: models, fetched: now}
	codeartsModelsCacheMu.Unlock()
}

// FetchCodeArtsModels fetches the available CodeArts models dynamically from the
// CodeArts agent-center API (GET /v1/agent-center/agents/detail). The response
// mirrors what the official CodeArts plugin displays in its model picker. Falls
// back to the static registry list when credentials are missing or the request fails.
// Successful fetches are cached for codeartsModelsCacheTTL; failed fetches enter
// a codeartsModelsFailCooldown negative cache to avoid hammering upstream.
func FetchCodeArtsModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	cacheKey := codeArtsModelsCacheKey(auth)

	cacheNow := time.Now()
	codeartsModelsCacheMu.RLock()
	entry := codeartsModelsCache[cacheKey]
	codeartsModelsCacheMu.RUnlock()
	if entry != nil && len(entry.models) > 0 && cacheNow.Sub(entry.fetched) < codeartsModelsCacheTTL {
		return entry.models
	}
	if entry != nil && !entry.lastFail.IsZero() && cacheNow.Sub(entry.lastFail) < codeartsModelsFailCooldown {
		return registry.GetCodeArtsModels()
	}

	token := extractCodeArtsToken(auth)
	if token == nil {
		log.Info("codearts: no AK/SK credentials, skipping dynamic model fetch")
		return registry.GetCodeArtsModels()
	}

	agentID := resolveCodeArtsDefaultAgentID(ctx, auth, cfg)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, codearts.GptsURL+"/detail", nil)
	if err != nil {
		log.Warnf("codearts: failed to build models request: %v", err)
		codeArtsModelsMarkFail(cacheKey, time.Now())
		return registry.GetCodeArtsModels()
	}
	q := req.URL.Query()
	q.Set("agent_id", agentID)
	req.URL.RawQuery = q.Encode()

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-snap-traceid", generateTraceID())
	req.Header.Set("Agent-Type", "AgentCenter")
	req.Header.Set("X-Language", codeartsLanguage)
	req.Header.Set("area", "green")

	// agent-center requests authenticate via the AK/SK signature and
	// X-Security-Token (set by SignRequest); x-auth-token is rejected by APIG.
	codearts.SignRequest(req, nil, token.AK, token.SK, token.SecurityToken)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 30*time.Second)
	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		log.Warnf("codearts: failed to fetch models: %v", errDo)
		codeArtsModelsMarkFail(cacheKey, time.Now())
		return registry.GetCodeArtsModels()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Warnf("codearts: models request returned %d: %s", resp.StatusCode, string(body))
		codeArtsModelsMarkFail(cacheKey, time.Now())
		return registry.GetCodeArtsModels()
	}

	// The detail endpoint returns the agent object with the model list nested
	// under gpts.models (verified against live upstream response).
	models := gjson.GetBytes(body, "gpts.models")
	if !models.Exists() || !models.IsArray() {
		log.Warn("codearts: invalid models response format")
		codeArtsModelsMarkFail(cacheKey, time.Now())
		return registry.GetCodeArtsModels()
	}

	now := time.Now().Unix()
	dynamicModels := make([]*registry.ModelInfo, 0, 8)
	models.ForEach(func(_, value gjson.Result) bool {
		params := value.Get("model_parameters")
		id := params.Get("model_id").String()
		if id == "" {
			id = value.Get("model_alias").String()
		}
		if id == "" {
			return true
		}
		displayName := value.Get("model_name").String()
		if displayName == "" {
			displayName = id
		}
		dynamicModels = append(dynamicModels, &registry.ModelInfo{
			// Model IDs are exposed lowercased so clients can match them case-insensitively.
			ID:                  strings.ToLower(id),
			Name:                strings.ToLower(id),
			DisplayName:         displayName,
			ContextLength:       int(params.Get("context_window").Int()),
			MaxCompletionTokens: int(params.Get("max_tokens").Int()),
			OwnedBy:             "codearts",
			Type:                "codearts",
			Object:              "model",
			Created:             now,
			SupportedEndpoints:  []string{"/chat/completions"},
		})
		return true
	})

	if len(dynamicModels) == 0 {
		log.Warn("codearts: no models returned, using static fallback")
		codeArtsModelsMarkFail(cacheKey, time.Now())
		return registry.GetCodeArtsModels()
	}

	// The default CodeAgent detail only lists the models bound to that agent, so
	// merge the static registry (verified usable models) back in to keep the full
	// account model set. Dynamic entries win on overlapping IDs.
	seen := make(map[string]bool, len(dynamicModels))
	for _, m := range dynamicModels {
		seen[m.ID] = true
	}
	for _, sm := range registry.GetCodeArtsModels() {
		if !seen[sm.ID] {
			dynamicModels = append(dynamicModels, sm)
			seen[sm.ID] = true
		}
	}

	log.Infof("codearts: fetched %d models dynamically (incl. static merge)", len(dynamicModels))
	codeArtsModelsCacheStore(cacheKey, time.Now(), dynamicModels)
	return dynamicModels
}

// resolveCodeArtsDefaultAgentID returns the account's default CodeAgent
// agent_id. Precedence: auth attribute override, dynamic resolution from the
// agent-center useragents endpoint, then the hardcoded fallback.
func resolveCodeArtsDefaultAgentID(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) string {
	if auth != nil && auth.Attributes != nil {
		if aid := strings.TrimSpace(auth.Attributes["agent_id"]); aid != "" {
			return aid
		}
	}
	token := extractCodeArtsToken(auth)
	if token == nil {
		return codearts.DefaultAgentID
	}
	agentID, err := fetchCodeArtsDefaultAgentID(ctx, cfg, auth, token)
	if err != nil || agentID == "" {
		log.Warnf("codearts: failed to resolve default agent id (%v), using fallback", err)
		return codearts.DefaultAgentID
	}
	log.Debugf("codearts: resolved default agent id %s", agentID)
	return agentID
}

// fetchCodeArtsDefaultAgentID queries the agent-center useragents endpoint and
// returns the primary (default) CodeAgent agent_id, or the first agent when no
// primary flag is set.
func fetchCodeArtsDefaultAgentID(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, token *codearts.CodeArtsTokenData) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, codearts.GptsURL+"/useragents?offset=0&limit=100", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-snap-traceid", generateTraceID())
	req.Header.Set("Agent-Type", "AgentCenter")
	req.Header.Set("X-Language", codeartsLanguage)
	codearts.SignRequest(req, nil, token.AK, token.SK, token.SecurityToken)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 30*time.Second)
	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		return "", errDo
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("useragents request returned %d: %s", resp.StatusCode, string(body))
	}

	var out struct {
		Agents []struct {
			AgentID   string `json:"agent_id"`
			AgentName string `json:"agent_name"`
			Primary   bool   `json:"is_primary_agent"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("parse useragents response: %w", err)
	}
	for _, a := range out.Agents {
		if a.Primary && a.AgentID != "" {
			return a.AgentID, nil
		}
	}
	if len(out.Agents) > 0 {
		return out.Agents[0].AgentID, nil
	}
	return "", nil
}

// Execute handles non-streaming chat completions.
func (e *CodeArtsExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	parsed := thinking.ParseSuffix(req.Model)
	baseModel := parsed.ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	agentID := resolveCodeArtsDefaultAgentID(ctx, auth, e.cfg)

	userID := extractUserID(auth)

	// chatID is the 32-hex conversation id sent upstream and echoed back in the
	// completion response, so clients can continue the same conversation.
	chatID := generateChatID()

	// Translate the source format to OpenAI chat completions first (the
	// CodeArts upstream only understands OpenAI format). For non-OpenAI source
	// formats (e.g. claude from /v1/messages), a two-hop translation is needed:
	// SourceFormat → openai → codearts.
	payload := req.Payload
	if opts.SourceFormat != sdktranslator.FormatOpenAI {
		payload = sdktranslator.TranslateRequest(opts.SourceFormat, sdktranslator.FormatOpenAI, baseModel, req.Payload, false)
	}
	v1Payload := buildCodeArtsPayload(payload, baseModel, agentID, userID, chatID, opts)

	headers := make(http.Header)
	// Sign over the real v1 payload so the signature body hash matches what is
	// actually sent (an empty body hash yields APIG.0301 at the upstream).
	tmpReq := &http.Request{
		Method:        http.MethodPost,
		Header:        headers,
		URL:           codeartsChatURLParsed,
		Body:          io.NopCloser(bytes.NewReader(v1Payload)),
		ContentLength: int64(len(v1Payload)),
	}
	if errPrep := e.PrepareRequest(tmpReq, auth); errPrep != nil {
		return resp, errPrep
	}
	headers = tmpReq.Header

	var v2Headers http.Header
	var v2Payload []byte
	// Prefer the v2 /api/v2/chat/completions endpoint for every model. The v2
	// endpoint is the only one that reliably serves real model inference; the
	// legacy v1 endpoint only returns an agent fallback greeting or an upstream
	// error for actual request content. Unmapped model ids are passed through as
	// as-is (v2 accepts the lowercase ids that come back from the model list).
	// v1 is kept purely as a fallback if the v2 request cannot be built.
	if v2p, err := buildCodeArtsV2Payload(payload, baseModel, true); err == nil {
		if v2h, errH := e.buildCodeArtsV2Request(auth, v2p, ""); errH == nil {
			v2Payload = v2p
			v2Headers = v2h
		} else {
			log.Debugf("codearts: v2 request build failed (%v), using v1", errH)
		}
	} else {
		log.Debugf("codearts: v2 payload build failed (%v), using v1", err)
	}

	httpResp, _, errDo := e.sendCodeArtsChat(ctx, auth, headers, v1Payload, v2Headers, v2Payload)
	if errDo != nil {
		return resp, errDo
	}
	defer httpResp.Body.Close()
	log.Debugf("codearts: Execute response status=%d, content_type=%s", httpResp.StatusCode, httpResp.Header.Get("Content-Type"))

	var contentBuilder strings.Builder
	var reasoningBuilder strings.Builder
	var promptTokens, completionTokens int64
	var respModel string
	toolCallsAccumulated := make(map[int]map[string]interface{})

	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	var pendingEvent string
	streamState := &codeartsStreamState{}
	for scanner.Scan() {
		_, data, ok := parseCodeArtsSSELine(scanner.Text(), &pendingEvent)
		if !ok {
			continue
		}
		if data == "[DONE]" || gjson.Get(data, "text").String() == "[DONE]" {
			// Upstream failures can arrive as a "[DONE]" frame carrying an
			// error_code/respCode. Surface them instead of returning an empty
			// answer.
			if errFrame := codeArtsFrameError(data); errFrame != nil {
				return cliproxyexecutor.Response{}, errFrame
			}
			break
		}
		result := streamState.convert(data, "", req.Model)
		if result.Err != nil {
			return cliproxyexecutor.Response{}, result.Err
		}
		if result.ReplaceContent {
			contentBuilder.Reset()
		}
		contentBuilder.WriteString(result.ContentValue)
		reasoningBuilder.WriteString(result.ReasoningValue)
		for _, tc := range result.ToolCalls {
			idx := int(tc["index"].(int64))
			if existing, exists := toolCallsAccumulated[idx]; exists {
				mergeCodeArtsToolCall(existing, tc)
			} else {
				toolCallsAccumulated[idx] = newCodeArtsToolCall(tc)
			}
		}
		if result.ModelName != "" {
			respModel = result.ModelName
		}
		if result.PromptTokens > 0 {
			promptTokens = result.PromptTokens
		}
		if result.CompletionTokens > 0 {
			completionTokens = result.CompletionTokens
		}
	}

	var toolCallsList []map[string]interface{}
	if len(toolCallsAccumulated) > 0 {
		indices := make([]int, 0, len(toolCallsAccumulated))
		for k := range toolCallsAccumulated {
			indices = append(indices, k)
		}
		sort.Ints(indices)
		for _, k := range indices {
			toolCallsList = append(toolCallsList, toolCallsAccumulated[k])
		}
	}

	fullContent := contentBuilder.String()
	if len(toolCallsList) == 0 && fullContent != "" && strings.Contains(fullContent, "<tool_call_id>") {
		xmlToolCalls := parseXMLToolCalls(fullContent)
		if len(xmlToolCalls) > 0 {
			toolCallsList = xmlToolCalls
			stripped := stripXMLToolCalls(fullContent)
			if stripped == "" {
				fullContent = ""
			} else {
				fullContent = stripped
			}
		}
	}

	if respModel == "" {
		respModel = req.Model
	}

	from := opts.SourceFormat
	if from != sdktranslator.FormatOpenAI {
		from = sdktranslator.FormatOpenAI
	}
	to := sdktranslator.FromString("codearts")

	openAIResp := buildOpenAINonStreamResponse(fullContent, reasoningBuilder.String(), respModel, chatID, promptTokens, completionTokens, toolCallsList)
	var param any
	translated := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, req.Payload, openAIResp, &param)

	reporter.Publish(ctx, usage.Detail{
		InputTokens:  promptTokens,
		OutputTokens: completionTokens,
	})
	reporter.EnsurePublished(ctx)

	return cliproxyexecutor.Response{Payload: translated}, nil
}

// ExecuteStream handles streaming chat completions.
func (e *CodeArtsExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	parsed := thinking.ParseSuffix(req.Model)
	baseModel := parsed.ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	agentID := resolveCodeArtsDefaultAgentID(ctx, auth, e.cfg)

	userID := extractUserID(auth)

	// chatID is the 32-hex conversation id sent upstream and exposed to clients
	// via the X-Codearts-Chat-Id response header.
	chatID := generateChatID()

	// Translate the source format to OpenAI chat completions first (the
	// CodeArts upstream only understands OpenAI format). For non-OpenAI source
	// formats (e.g. claude from /v1/messages), a two-hop translation is needed:
	// SourceFormat → openai → codearts.
	payload := req.Payload
	if opts.SourceFormat != sdktranslator.FormatOpenAI {
		payload = sdktranslator.TranslateRequest(opts.SourceFormat, sdktranslator.FormatOpenAI, baseModel, req.Payload, true)
	}
	v1Payload := buildCodeArtsPayload(payload, baseModel, agentID, userID, chatID, opts)

	headers := make(http.Header)
	// Sign over the real v1 payload so the signature body hash matches what is
	// actually sent (an empty body hash yields APIG.0301 at the upstream).
	tmpReq := &http.Request{
		Method:        http.MethodPost,
		Header:        headers,
		URL:           codeartsChatURLParsed,
		Body:          io.NopCloser(bytes.NewReader(v1Payload)),
		ContentLength: int64(len(v1Payload)),
	}
	if errPrep := e.PrepareRequest(tmpReq, auth); errPrep != nil {
		return nil, errPrep
	}
	headers = tmpReq.Header

	var v2Headers http.Header
	var v2Payload []byte
	// Prefer the v2 /api/v2/chat/completions endpoint for every model. The v2
	// endpoint is the only one that reliably serves real model inference; the
	// legacy v1 endpoint only returns an agent fallback greeting or an upstream
	// error for actual request content. Unmapped model ids are passed through as
	// as-is (v2 accepts the lowercase ids that come back from the model list).
	// v1 is kept purely as a fallback if the v2 request cannot be built.
	if v2p, err := buildCodeArtsV2Payload(payload, baseModel, true); err == nil {
		if v2h, errH := e.buildCodeArtsV2Request(auth, v2p, ""); errH == nil {
			v2Payload = v2p
			v2Headers = v2h
		} else {
			log.Debugf("codearts: v2 request build failed (%v), using v1", errH)
		}
	} else {
		log.Debugf("codearts: v2 payload build failed (%v), using v1", err)
	}

	httpResp, _, errDo := e.sendCodeArtsChat(ctx, auth, headers, v1Payload, v2Headers, v2Payload)
	if errDo != nil {
		return nil, errDo
	}

	log.Debugf("codearts: stream response status=%d, content_type=%s, content_length=%d",
		httpResp.StatusCode, httpResp.Header.Get("Content-Type"), httpResp.ContentLength)

	chunks := make(chan cliproxyexecutor.StreamChunk, 64)

	go func() {
		defer close(chunks)
		defer httpResp.Body.Close()

		from := opts.SourceFormat
		if from != sdktranslator.FormatOpenAI {
			from = sdktranslator.FormatOpenAI
		}
		to := sdktranslator.FromString("codearts")
		var streamParam any
		var totalPromptTokens, totalCompletionTokens int64
		var lineCount int
		var dataLineCount int
		var firstNonEmptyLine string
		var accumulatedContent strings.Builder
		var hasToolCalls bool
		respModel := req.Model

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
		var pendingEvent string
		streamState := &codeartsStreamState{}
		for scanner.Scan() {
			line := scanner.Text()
			lineCount++
			if firstNonEmptyLine == "" {
				firstNonEmptyLine = line
			}
			ev, data, ok := parseCodeArtsSSELine(line, &pendingEvent)
			if !ok {
				continue
			}
			if data == "[DONE]" || gjson.Get(data, "text").String() == "[DONE]" {
				// Upstream failures can arrive as a "[DONE]" frame carrying an
				// error_code/respCode. Surface them instead of ending the stream
				// silently with whatever content was accumulated.
				if errFrame := codeArtsFrameError(data); errFrame != nil {
					log.Warnf("codearts: upstream error frame: %v", errFrame)
					chunks <- cliproxyexecutor.StreamChunk{Err: errFrame}
				}
				break
			}
			dataLineCount++

			result := streamState.convert(data, ev, respModel)
			if result.Err != nil {
				log.Warnf("codearts: chunk error: %v", result.Err)
				continue
			}
			if result.ModelName != "" {
				respModel = result.ModelName
			}
			if result.PromptTokens > 0 {
				totalPromptTokens = result.PromptTokens
			}
			if result.CompletionTokens > 0 {
				totalCompletionTokens = result.CompletionTokens
			}

			if result.HasToolCalls {
				hasToolCalls = true
			} else if result.HasContent {
				if result.ReplaceContent {
					accumulatedContent.Reset()
				}
				accumulatedContent.WriteString(result.ContentValue)
			}

			chunk := buildCodeArtsOpenAIChunk(streamState, &result)
			if chunk == nil {
				continue
			}
			translatedChunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, req.Payload, chunk, &streamParam)
			for _, tc := range translatedChunks {
				if len(tc) > 0 {
					chunks <- cliproxyexecutor.StreamChunk{Payload: tc}
				}
			}
		}

		if !hasToolCalls && accumulatedContent.Len() > 0 && strings.Contains(accumulatedContent.String(), "<tool_call_id>") {
			xmlToolCalls := parseXMLToolCalls(accumulatedContent.String())
			if len(xmlToolCalls) > 0 {
				hasToolCalls = true
				for i, tc := range xmlToolCalls {
					chunk := buildToolCallStreamChunk(respModel, i, tc)
					translatedChunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, req.Payload, chunk, &streamParam)
					for _, tChunk := range translatedChunks {
						if len(tChunk) > 0 {
							chunks <- cliproxyexecutor.StreamChunk{Payload: tChunk}
						}
					}
				}
			}
		}

		if hasToolCalls {
			finishChunk := buildFinishReasonStreamChunk(respModel, "tool_calls")
			translatedChunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, req.Payload, finishChunk, &streamParam)
			for _, tChunk := range translatedChunks {
				if len(tChunk) > 0 {
					chunks <- cliproxyexecutor.StreamChunk{Payload: tChunk}
				}
			}
		}

		if dataLineCount == 0 {
			log.Warnf("codearts: stream ended with no data lines (total_lines=%d, first_non_empty=%q)", lineCount, firstNonEmptyLine)
		}

		if err := scanner.Err(); err != nil {
			log.Warnf("codearts: stream scanner error: %v", err)
			chunks <- cliproxyexecutor.StreamChunk{Err: err}
		}

		reporter.Publish(ctx, usage.Detail{
			InputTokens:  totalPromptTokens,
			OutputTokens: totalCompletionTokens,
		})
		reporter.EnsurePublished(ctx)

	}()

	// Expose the conversation id to clients.
	httpResp.Header.Set("X-Codearts-Chat-Id", chatID)

	return &cliproxyexecutor.StreamResult{
		Headers: httpResp.Header,
		Chunks:  chunks,
	}, nil
}

// CountTokens is not supported by CodeArts.
func (e *CodeArtsExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("codearts: token counting not supported")
}

// Refresh refreshes the CodeArts security token.
func (e *CodeArtsExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil || auth.Metadata == nil {
		return nil, fmt.Errorf("codearts: no metadata to refresh")
	}

	currentToken := extractCodeArtsToken(auth)
	if currentToken == nil {
		return nil, fmt.Errorf("codearts: no valid token data found for refresh")
	}

	if !codearts.NeedsRefresh(currentToken) {
		return auth, nil
	}

	caAuth := codearts.NewCodeArtsAuth(nil)
	var newToken *codearts.CodeArtsTokenData
	var err error
	if currentToken.RefreshToken != "" && currentToken.CodeVerifier != "" {
		// Prefer the refresh_token grant (snap-manager / STS, DPoP) over the
		// legacy AK/SK-based /v2/login/refresh flow.
		newToken, err = caAuth.RefreshWithRefreshToken(ctx, currentToken)
		if err != nil {
			log.Warnf("codearts: refresh_token renewal failed (%v), falling back to AK/SK refresh", err)
			newToken, err = caAuth.RefreshToken(ctx, currentToken)
		}
	} else {
		newToken, err = caAuth.RefreshToken(ctx, currentToken)
	}
	if err != nil {
		return nil, fmt.Errorf("codearts: refresh failed: %w", err)
	}

	updated := auth.Clone()
	updated.Metadata["ak"] = newToken.AK
	updated.Metadata["sk"] = newToken.SK
	updated.Metadata["security_token"] = newToken.SecurityToken
	updated.Metadata["expires_at"] = newToken.ExpiresAt.Format(time.RFC3339)
	if newToken.XAuthToken != "" {
		updated.Metadata["x_auth_token"] = newToken.XAuthToken
	}
	if newToken.RefreshToken != "" {
		updated.Metadata["refresh_token"] = newToken.RefreshToken
	}
	if newToken.CodeVerifier != "" {
		updated.Metadata["code_verifier"] = newToken.CodeVerifier
	}

	log.Infof("codearts: successfully refreshed token, expires at %s", newToken.ExpiresAt.Format(time.RFC3339))
	return updated, nil
}

// extractCodeArtsToken extracts token data from auth metadata.
func extractCodeArtsToken(auth *cliproxyauth.Auth) *codearts.CodeArtsTokenData {
	if auth == nil || auth.Metadata == nil {
		return nil
	}

	ak, _ := auth.Metadata["ak"].(string)
	sk, _ := auth.Metadata["sk"].(string)
	if ak == "" || sk == "" {
		return nil
	}

	token := &codearts.CodeArtsTokenData{
		AK:            ak,
		SK:            sk,
		SecurityToken: metadataStr(auth.Metadata, "security_token"),
		XAuthToken:    metadataStr(auth.Metadata, "x_auth_token"),
		RefreshToken:  metadataStr(auth.Metadata, "refresh_token"),
		CodeVerifier:  metadataStr(auth.Metadata, "code_verifier"),
		Email:         metadataStr(auth.Metadata, "email"),
	}

	if expiresStr := metadataStr(auth.Metadata, "expires_at"); expiresStr != "" {
		if t, err := time.Parse(time.RFC3339, expiresStr); err == nil {
			token.ExpiresAt = t
		}
	}

	return token
}

func metadataStr(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func extractUserID(auth *cliproxyauth.Auth) string {
	if auth.Metadata != nil {
		if uid, ok := auth.Metadata["user_id"].(string); ok {
			return uid
		}
		if did, ok := auth.Metadata["domain_id"].(string); ok {
			return did
		}
	}
	return ""
}

func generateTraceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%032d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func generateChatID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%032d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func generateToolCallID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("call_%019d", time.Now().UnixNano())
	}
	return "call_" + hex.EncodeToString(b)
}

const toolsSystemPromptTemplate = "# Available Tools\n\nYou have access to the following tools. You MUST respond with tool calls using the exact XML format specified below.\n\n%s\n\n# Tool Call Format\n\nWhen you need to use a tool, you MUST output the tool call in the following XML format:\n\n<tool_call_id>call_<random_hex_24chars></tool_call_id>\n<tool_name>function_name_here</tool_name>\n<tool_arguments>\n{\"param1\": \"value1\", \"param2\": \"value2\"}\n</tool_arguments>\n\nRules:\n- Each tool call MUST have a unique tool_call_id starting with \"call_\" followed by 24 random hex characters.\n- tool_arguments MUST be valid JSON matching the function's parameters schema.\n- You may make multiple tool calls in a single response.\n- When you want to call tools, output ONLY the tool call XML blocks, do NOT output any other text.\n- Do NOT wrap tool calls in markdown code blocks.\n- The tool_call_id MUST be unique for each tool call."

func buildToolsSystemPrompt(tools gjson.Result) string {
	var toolDefs []string
	for _, tool := range tools.Array() {
		if tool.Get("type").String() != "function" {
			continue
		}
		fn := tool.Get("function")
		name := fn.Get("name").String()
		desc := fn.Get("description").String()
		params := fn.Get("parameters").Raw
		if params == "" {
			params = "{}"
		}
		toolDefs = append(toolDefs, fmt.Sprintf("## %s\n%s\nParameters: %s", name, desc, params))
	}
	if len(toolDefs) == 0 {
		return ""
	}
	return fmt.Sprintf(toolsSystemPromptTemplate, strings.Join(toolDefs, "\n\n"))
}

func parseXMLToolCalls(text string) []map[string]interface{} {
	var results []map[string]interface{}
	segments := strings.Split(text, "<tool_call_id>")
	for _, seg := range segments[1:] {
		idEnd := strings.Index(seg, "</tool_call_id>")
		if idEnd < 0 {
			continue
		}
		tcID := strings.TrimSpace(seg[:idEnd])

		rest := seg[idEnd+len("</tool_call_id>"):]
		nameStart := strings.Index(rest, "<tool_name>")
		if nameStart < 0 {
			continue
		}
		nameStart += len("<tool_name>")
		nameEnd := strings.Index(rest, "</tool_name>")
		if nameEnd < 0 || nameEnd < nameStart {
			continue
		}
		tcName := strings.TrimSpace(rest[nameStart:nameEnd])

		argsRest := rest[nameEnd+len("</tool_name>"):]
		argsStart := strings.Index(argsRest, "<tool_arguments>")
		if argsStart < 0 {
			continue
		}
		argsStart += len("<tool_arguments>")
		argsEnd := strings.Index(argsRest, "</tool_arguments>")
		if argsEnd < 0 || argsEnd < argsStart {
			continue
		}
		argsStr := strings.TrimSpace(argsRest[argsStart:argsEnd])

		if tcID == "" {
			tcID = generateToolCallID()
		}
		results = append(results, map[string]interface{}{
			"id":   tcID,
			"type": "function",
			"function": map[string]interface{}{
				"name":      tcName,
				"arguments": argsStr,
			},
		})
	}
	return results
}

func stripXMLToolCalls(text string) string {
	result := text
	for strings.Contains(result, "<tool_call_id>") && strings.Contains(result, "</tool_arguments>") {
		start := strings.Index(result, "<tool_call_id>")
		end := strings.Index(result, "</tool_arguments>") + len("</tool_arguments>")
		if end <= start {
			break
		}
		result = result[:start] + result[end:]
	}
	return strings.TrimSpace(result)
}

func buildToolCallStreamChunk(model string, index int, toolCall map[string]interface{}) []byte {
	tc := map[string]interface{}{
		"index":    index,
		"id":       toolCall["id"],
		"type":     "function",
		"function": toolCall["function"],
	}
	chunk := map[string]interface{}{
		"id":      "chatcmpl-codearts",
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"delta": map[string]interface{}{
					"tool_calls": []map[string]interface{}{tc},
				},
			},
		},
	}
	result, _ := json.Marshal(chunk)
	return result
}

func buildFinishReasonStreamChunk(model string, finishReason string) []byte {
	chunk := map[string]interface{}{
		"id":      "chatcmpl-codearts",
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"delta":         map[string]interface{}{},
				"finish_reason": finishReason,
			},
		},
	}
	result, _ := json.Marshal(chunk)
	return result
}

// codeArtsCanonicalModels maps user-side lowercase model IDs to the exact-case
// InferHub-registered names required by /api/v2/chat/completions.
var codeArtsCanonicalModels = map[string]string{
	"snap-chat":            "GLM-5.2",
	"glm-5.2":              "GLM-5.2",
	"glm-5.1":              "GLM-5.1",
	"glm-4.7":              "GLM-4.7",
	"qwen3-vl-235b":        "Qwen3-VL-235B",
	"qwen3.5-397b-a17b-vl": "Qwen3.5-397B-A17B-VL",
	"qwen3.6-27b-vl":       "Qwen3.6-27B-VL",
}

// canonicalCodeArtsModel returns the exact-case InferHub-registered model name
// required by the /api/v2/chat/completions endpoint. The legacy /v1/chat/chat
// endpoint accepts lowercase ids directly.
func canonicalCodeArtsModel(id string) string {
	if v, ok := codeArtsCanonicalModels[strings.ToLower(strings.TrimSpace(id))]; ok {
		return v
	}
	return id
}

// flattenCodeArtsMessages converts an OpenAI-format messages array into the
// CodeArts content-block format, prefixing each role as the upstream expects.
func flattenCodeArtsMessages(openaiPayload []byte) ([]map[string]string, bool) {
	messages := gjson.GetBytes(openaiPayload, "messages")
	if !messages.Exists() {
		log.Warn("codearts: no messages found in payload")
		return nil, false
	}

	var codeArtsMessages []map[string]string
	for _, msg := range messages.Array() {
		role := msg.Get("role").String()
		content := extractTextContent(msg.Get("content"))

		var formattedContent string
		switch role {
		case "system":
			formattedContent = "[System]\n" + content
		case "assistant":
			toolCalls := msg.Get("tool_calls")
			if toolCalls.Exists() && len(toolCalls.Array()) > 0 {
				var parts []string
				if content != "" {
					parts = append(parts, content)
				}
				for _, tc := range toolCalls.Array() {
					name := tc.Get("function.name").String()
					id := tc.Get("id").String()
					args := tc.Get("function.arguments").String()
					parts = append(parts, fmt.Sprintf("[Tool Call: %s] (id: %s)\n%s", name, id, args))
				}
				formattedContent = "[Assistant]\n" + strings.Join(parts, "\n")
			} else {
				formattedContent = "[Assistant]\n" + content
			}
		case "tool":
			toolName := msg.Get("name").String()
			toolID := msg.Get("tool_call_id").String()
			if toolName == "" {
				toolName = "unknown"
			}
			formattedContent = fmt.Sprintf("[Tool Result: %s] (id: %s)\n%s", toolName, toolID, content)
		case "user":
			formattedContent = content
		default:
			formattedContent = content
		}

		codeArtsMessages = append(codeArtsMessages, map[string]string{
			"type":    "text",
			"content": formattedContent,
		})
	}
	return codeArtsMessages, true
}

// buildCodeArtsV2Payload converts the OpenAI-format payload to the
// /api/v2/chat/completions OpenAI-compatible body. The whole conversation is
// folded into a single user message (matching the official AgentKernel flow).
func buildCodeArtsV2Payload(openaiPayload []byte, modelName string, stream bool) ([]byte, error) {
	codeArtsMessages, ok := flattenCodeArtsMessages(openaiPayload)
	if !ok {
		return nil, fmt.Errorf("codearts: no messages found in payload")
	}

	parts := make([]string, 0, len(codeArtsMessages)+1)
	if tools := gjson.GetBytes(openaiPayload, "tools"); tools.Exists() {
		if toolsPrompt := buildToolsSystemPrompt(tools); toolsPrompt != "" {
			parts = append(parts, "[System]\n"+toolsPrompt)
		}
	}
	for _, m := range codeArtsMessages {
		if m["content"] != "" {
			parts = append(parts, m["content"])
		}
	}

	body := map[string]any{
		"model":    canonicalCodeArtsModel(modelName),
		"stream":   stream,
		"messages": []map[string]any{{"role": "user", "content": strings.Join(parts, "\n\n")}},
	}
	result, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("codearts: marshal v2 payload: %w", err)
	}
	return result, nil
}

// buildCodeArtsPayload converts the OpenAI-format payload to CodeArts format.
func buildCodeArtsPayload(openaiPayload []byte, modelName, agentID, userID, chatID string, opts cliproxyexecutor.Options) []byte {
	codeArtsMessages, ok := flattenCodeArtsMessages(openaiPayload)
	if !ok {
		return openaiPayload
	}

	taskParameters := map[string]interface{}{
		"is_intent_recognition":   false,
		"W3_Search":               false,
		"codebase_search":         false,
		"related_question":        true,
		"preferred_language":      "zh-cn",
		"enable_code_interpreter": false,
		"projectLevelPrompt":      "",
		"contexts":                []interface{}{},
		"expert_rules":            []interface{}{},
		"ide":                     "CodeArts Agent",
		"routerVersion":           "v2",
		"isNewClient":             true,
		"features":                map[string]interface{}{"support_end_tag": true},
	}

	if tools := gjson.GetBytes(openaiPayload, "tools"); tools.Exists() {
		taskParameters["tools"] = tools.Value()
		toolsPrompt := buildToolsSystemPrompt(tools)
		if toolsPrompt != "" {
			hasSystem := false
			for i, msg := range codeArtsMessages {
				if strings.HasPrefix(msg["content"], "[System]") {
					codeArtsMessages[i]["content"] = msg["content"] + "\n\n" + toolsPrompt
					hasSystem = true
					break
				}
			}
			if !hasSystem {
				codeArtsMessages = append(
					[]map[string]string{{"type": "text", "content": "[System]\n" + toolsPrompt}},
					codeArtsMessages...,
				)
			}
		}
	}
	if temp := gjson.GetBytes(openaiPayload, "temperature"); temp.Exists() {
		taskParameters["temperature"] = temp.Value()
	}

	request := map[string]interface{}{
		"chat_id":               chatID,
		"messages":              codeArtsMessages,
		"client":                "IDE",
		"task":                  "chat",
		"task_parameters":       taskParameters,
		"batch_task_parameters": []interface{}{},
		"attempt":               1,
		"user_id":               userID,
		"parent_message_id":     "",
		"is_delta_response":     true,
		"model_id":              modelName,
	}

	result, err := json.Marshal(request)
	if err != nil {
		log.Errorf("codearts: failed to marshal payload: %v", err)
		return openaiPayload
	}
	return result
}

// codeartsStreamState carries per-stream state needed to translate CodeArts
// text-snapshot (replace-semantics) frames into incremental OpenAI deltas.
type codeartsStreamState struct {
	lastFullContent string
	contentStarted  bool
}

// codeartsStreamResult is the normalized result of parsing one CodeArts SSE
// data frame, independent of the v1 (delta) / v2 (choices) wire format.
type codeartsStreamResult struct {
	HasContent       bool
	ReplaceContent   bool
	ContentValue     string
	ReasoningValue   string
	HasToolCalls     bool
	ToolCalls        []map[string]interface{}
	Role             string
	FinishReason     string
	PromptTokens     int64
	CompletionTokens int64
	ModelName        string
	Err              error
}

// parseCodeArtsSSELine parses one CodeArts SSE line, handling event: prefixes,
// data: prefixes, heartbeat/comment lines, and blank lines. When a data line is
// seen it returns the current event name and the JSON payload.
func parseCodeArtsSSELine(line string, pendingEvent *string) (event, data string, ok bool) {
	line = strings.TrimRight(line, "\r")
	switch {
	case strings.HasPrefix(line, "event:"):
		*pendingEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		return "", "", false
	case strings.HasPrefix(line, "data:"):
		d := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if d == "" {
			return "", "", false
		}
		ev := *pendingEvent
		*pendingEvent = ""
		return ev, d, true
	case strings.HasPrefix(line, ":"):
		// Heartbeat / comment line.
		return "", "", false
	case line == "":
		*pendingEvent = ""
		return "", "", false
	}
	return "", "", false
}

// codeArtsFrameError extracts an upstream error from a CodeArts SSE data frame.
// Upstream failures arrive either as a top-level error_code (numeric or string,
// e.g. "ChatAgent.00001105" / "InferHub.002002009.404") with an error_msg, or
// inside a respCode field, sometimes combined with a text "[DONE]" marker.
func codeArtsFrameError(data string) error {
	msg := gjson.Get(data, "error_msg").String()
	if rc := gjson.Get(data, "respCode"); rc.Exists() && rc.Int() != 0 {
		if msg == "" {
			msg = "upstream error"
		}
		return fmt.Errorf("CodeArts error %d: %s", rc.Int(), msg)
	}
	if ec := gjson.Get(data, "error_code"); ec.Exists() && ec.String() != "" && ec.String() != "0" {
		if msg == "" {
			msg = "upstream error"
		}
		return fmt.Errorf("CodeArts error %s: %s", ec.String(), msg)
	}
	return nil
}

// convert normalizes one CodeArts SSE data frame. It supports the legacy v1
// delta frames (delta.content / delta.reasoning_content / delta.tool_calls),
// text full-snapshot frames (replace semantics), v2 OpenAI-compatible frames
// (choices[].delta / finish_reason), terminal output arrays, usage-only final
// frames, and structured QA answers.
func (s *codeartsStreamState) convert(data, event, model string) codeartsStreamResult {
	res := codeartsStreamResult{ModelName: model}

	if errFrame := codeArtsFrameError(data); errFrame != nil {
		res.Err = errFrame
		return res
	}

	// usage / model accounting shared by every frame shape.
	if u := gjson.Get(data, "usage"); u.Exists() {
		res.PromptTokens = u.Get("prompt_tokens").Int()
		res.CompletionTokens = u.Get("completion_tokens").Int()
	}
	if pt := gjson.Get(data, "prompt_tokens").Int(); pt > 0 {
		res.PromptTokens = pt
	}
	if ct := gjson.Get(data, "completion_tokens").Int(); ct > 0 {
		res.CompletionTokens = ct
	}
	if mn := gjson.Get(data, "model_name").String(); mn != "" {
		res.ModelName = mn
	} else if mn := gjson.Get(data, "model").String(); mn != "" {
		res.ModelName = mn
	}

	// v2 OpenAI-compatible frames: choices[].delta / finish_reason.
	if choices := gjson.Get(data, "choices"); choices.Exists() && choices.IsArray() && len(choices.Array()) > 0 {
		c0 := choices.Array()[0]
		if fr := c0.Get("finish_reason"); fr.Exists() && fr.Type != gjson.Null {
			res.FinishReason = fr.String()
		}
		if d := c0.Get("delta"); d.Exists() && d.IsObject() {
			s.applyDelta(&res, d)
		}
		if res.HasContent || res.HasToolCalls || res.ReasoningValue != "" || res.FinishReason != "" {
			return res
		}
	}

	// Legacy v1 delta frames.
	if d := gjson.Get(data, "delta"); d.Exists() && d.IsObject() {
		s.applyDelta(&res, d)
		if res.HasContent || res.HasToolCalls || res.ReasoningValue != "" {
			return res
		}
	}

	// text full snapshot (replace semantics).
	if t := gjson.Get(data, "text").String(); t != "" {
		trimmed := strings.TrimSpace(t)
		if trimmed == "[DONE]" || strings.EqualFold(trimmed, "[done]") || strings.EqualFold(trimmed, "done") {
			if res.FinishReason == "" {
				res.FinishReason = "stop"
			}
			return res
		}
		s.applySnapshot(&res, t)
		return res
	}

	// terminal output array: [{type:"output_text",text:"..."}]
	if out := gjson.Get(data, "output"); out.Exists() && out.IsArray() {
		var sb strings.Builder
		for _, item := range out.Array() {
			if item.Get("type").String() == "output_text" {
				sb.WriteString(item.Get("text").String())
			}
		}
		if sb.Len() > 0 {
			s.applySnapshot(&res, sb.String())
			return res
		}
	}

	// structured QA: extract the answer only when no real content has started,
	// so the whole QA JSON is never dumped as the reply. The trailing
	// related_question_answer block is naturally ignored (no top-level question).
	if !s.contentStarted && res.ContentValue == "" && res.ReasoningValue == "" {
		if isCodeArtsStructuredQA(data) {
			if ans := gjson.Get(data, "answer").String(); ans != "" {
				res.ContentValue = ans
				res.HasContent = true
				s.contentStarted = true
				return res
			}
		}
	}

	// usage-only final frame ends the stream.
	if res.FinishReason == "" && !gjson.Get(data, "delta").Exists() && !gjson.Get(data, "choices").Exists() && gjson.Get(data, "usage").Exists() {
		res.FinishReason = "stop"
	}

	// "done"-style event names end the stream.
	if res.FinishReason == "" && (event == "done" || event == "end" || event == "finish") {
		res.FinishReason = "stop"
	}

	return res
}

// applyDelta folds a v1/v2 delta object into the result.
func (s *codeartsStreamState) applyDelta(res *codeartsStreamResult, d gjson.Result) {
	if c := translatorcommon.TextFromContentBlocks(d.Get("content")); c != "" {
		res.ContentValue = c
		res.HasContent = true
		s.contentStarted = true
	}
	if r := d.Get("reasoning_content").String(); r != "" {
		res.ReasoningValue = r
		s.contentStarted = true
	}
	if tcList := d.Get("tool_calls"); tcList.Exists() && tcList.IsArray() && len(tcList.Array()) > 0 {
		for _, tc := range tcList.Array() {
			fn := map[string]interface{}{
				"name":      tc.Get("function.name").String(),
				"arguments": tc.Get("function.arguments").String(),
			}
			res.ToolCalls = append(res.ToolCalls, map[string]interface{}{
				"index":    tc.Get("index").Int(),
				"id":       tc.Get("id").String(),
				"type":     tc.Get("type").String(),
				"function": fn,
			})
		}
		res.HasToolCalls = true
	}
	if role := d.Get("role").String(); role != "" && !res.HasContent && !res.HasToolCalls && res.ReasoningValue == "" {
		res.Role = role
	}
}

// applySnapshot folds a full-text snapshot frame (replace semantics).
func (s *codeartsStreamState) applySnapshot(res *codeartsStreamResult, full string) {
	res.ContentValue = full
	res.HasContent = true
	res.ReplaceContent = true
	s.contentStarted = true
}

// isCodeArtsStructuredQA reports whether the frame is a structured QA payload
// (question + options + answer), as opposed to a plain text frame.
func isCodeArtsStructuredQA(data string) bool {
	p := gjson.Parse(data)
	return p.Get("question").Exists() && p.Get("question").Type == gjson.String &&
		p.Get("answer").Exists() && p.Get("answer").Type == gjson.String
}

// buildCodeArtsOpenAIChunk renders one OpenAI-format stream chunk from a
// normalized CodeArts frame. For text-snapshot frames only the incremental
// suffix is emitted so clients appending deltas do not duplicate content.
func buildCodeArtsOpenAIChunk(state *codeartsStreamState, res *codeartsStreamResult) []byte {
	delta := make(map[string]interface{})

	var contentDelta string
	if res.ReplaceContent {
		contentDelta = res.ContentValue
		if strings.HasPrefix(contentDelta, state.lastFullContent) {
			contentDelta = strings.TrimPrefix(contentDelta, state.lastFullContent)
		}
		state.lastFullContent = res.ContentValue
	} else if res.HasContent {
		contentDelta = res.ContentValue
		state.lastFullContent += contentDelta
	}

	if res.HasContent && contentDelta != "" {
		delta["content"] = contentDelta
	} else if res.ReasoningValue != "" || res.HasToolCalls || res.Role != "" {
		delta["content"] = ""
	}
	if res.ReasoningValue != "" {
		delta["reasoning_content"] = res.ReasoningValue
	}
	if res.HasToolCalls {
		tcs := make([]map[string]interface{}, 0, len(res.ToolCalls))
		for _, tc := range res.ToolCalls {
			fn, _ := tc["function"].(map[string]interface{})
			tcs = append(tcs, map[string]interface{}{
				"index":    tc["index"],
				"id":       tc["id"],
				"type":     tc["type"],
				"function": fn,
			})
		}
		delta["tool_calls"] = tcs
	}
	if res.Role != "" {
		delta["role"] = res.Role
	}

	if len(delta) == 0 && res.FinishReason == "" {
		return nil
	}

	chunk := map[string]interface{}{
		"id":      "chatcmpl-codearts",
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   res.ModelName,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"delta":         delta,
				"finish_reason": nil,
			},
		},
	}
	if res.FinishReason != "" {
		chunk["choices"].([]map[string]interface{})[0]["finish_reason"] = res.FinishReason
	}
	if res.PromptTokens > 0 || res.CompletionTokens > 0 {
		chunk["usage"] = map[string]interface{}{
			"prompt_tokens":     res.PromptTokens,
			"completion_tokens": res.CompletionTokens,
			"total_tokens":      res.PromptTokens + res.CompletionTokens,
		}
	}
	result, err := json.Marshal(chunk)
	if err != nil {
		return nil
	}
	return result
}

// newCodeArtsToolCall materializes a fresh tool-call accumulator entry.
func newCodeArtsToolCall(tc map[string]interface{}) map[string]interface{} {
	fn, _ := tc["function"].(map[string]interface{})
	return map[string]interface{}{
		"id":   tc["id"],
		"type": tc["type"],
		"function": map[string]interface{}{
			"name":      fn["name"],
			"arguments": fn["arguments"],
		},
	}
}

// mergeCodeArtsToolCall merges a streaming tool-call fragment into the
// accumulated entry, appending partial argument deltas.
func mergeCodeArtsToolCall(existing map[string]interface{}, tc map[string]interface{}) {
	if id, ok := tc["id"].(string); ok && id != "" {
		existing["id"] = id
	}
	if typ, ok := tc["type"].(string); ok && typ != "" {
		existing["type"] = typ
	}
	fn, _ := existing["function"].(map[string]interface{})
	tcFn, _ := tc["function"].(map[string]interface{})
	if name, ok := tcFn["name"].(string); ok && name != "" {
		fn["name"] = name
	}
	if args, ok := tcFn["arguments"].(string); ok && args != "" {
		if cur, ok := fn["arguments"].(string); ok && cur != "" {
			fn["arguments"] = cur + args
		} else {
			fn["arguments"] = args
		}
	}
}

// buildOpenAINonStreamResponse builds a complete OpenAI non-stream response.
// The chatID is the 32-hex conversation id also sent upstream, exposed so
// clients can continue the same conversation.
func buildOpenAINonStreamResponse(content, reasoning, model, chatID string, promptTokens, completionTokens int64, toolCalls []map[string]interface{}) []byte {
	message := map[string]interface{}{
		"role": "assistant",
	}
	if content != "" {
		message["content"] = content
	} else {
		message["content"] = nil
	}
	if reasoning != "" {
		message["reasoning_content"] = reasoning
	}

	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
		message["tool_calls"] = toolCalls
	}

	resp := map[string]interface{}{
		"id":      "chatcmpl-codearts",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"chat_id": chatID,
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       message,
				"finish_reason": finishReason,
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		},
	}

	result, _ := json.Marshal(resp)
	return result
}
