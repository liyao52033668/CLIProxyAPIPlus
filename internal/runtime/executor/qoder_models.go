package executor

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

var sharedQoderModelContractCache = newQoderModelContractCache()

type QoderModelCatalog struct {
	Models    []*registry.ModelInfo
	Contracts map[string]QoderModelContract
}

func FetchQoderModelCatalog(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) QoderModelCatalog {
	modelsResult, ok := fetchQoderModelArray(ctx, auth, cfg)
	if !ok {
		return QoderModelCatalog{}
	}

	catalog := fetchQoderModelCatalog(modelsResult, time.Now().Unix())
	log.Infof("qoder: fetched %d models from model list API", len(catalog.Models))
	if len(catalog.Models) == 0 {
		log.Warn("qoder: no models parsed from model list API")
		return QoderModelCatalog{}
	}
	return catalog
}

func FetchQoderModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	authID := QoderModelContractAuthID(auth)
	catalog := FetchQoderModelCatalog(ctx, auth, cfg)
	if len(catalog.Models) == 0 {
		ClearQoderModelContracts(authID)
		return registry.GetQoderModels()
	}

	StoreQoderModelContracts(authID, catalog.Contracts)
	return catalog.Models
}

type QoderModelContract struct {
	Source         string
	IsReasoning    bool
	AliyunUserType string
}

type qoderModelContract = QoderModelContract

func fetchQoderModelContract(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config, modelKey string) QoderModelContract {
	_ = ctx
	_ = cfg
	contract, ok := LoadQoderModelContract(QoderModelContractAuthID(auth), modelKey)
	if !ok {
		return QoderModelContract{}
	}
	return contract
}

func fetchQoderModelArray(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) (gjson.Result, bool) {
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := qoderEnsureSession(ctx, auth, cfg); err != nil {
			log.Infof("qoder: no usable session found, using static model list: %v", err)
			return gjson.Result{}, false
		}

		httpClient := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 15*time.Second)
		req, err := buildQoderCosyHTTPRequest(ctx, auth, http.MethodGet, qoder.ChatBase+qoder.ModelListPath+"?Encode=1", nil)
		if err != nil {
			log.Warnf("qoder: failed to create model list request: %v", err)
			return gjson.Result{}, false
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Cosy-Clienttype", "5")

		resp, err := httpClient.Do(req)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				log.Warnf("qoder: fetch models canceled: %v", err)
			} else {
				log.Warnf("qoder: using static models (model list fetch failed: %v)", err)
			}
			return gjson.Result{}, false
		}

		body, err := io.ReadAll(resp.Body)
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("qoder: close model list response body error: %v", errClose)
		}
		if err != nil {
			log.Warnf("qoder: failed to read model list response: %v", err)
			return gjson.Result{}, false
		}
		if resp.StatusCode != http.StatusOK {
			bodyText := strings.TrimSpace(string(body))
			if len(bodyText) > 1024 {
				bodyText = bodyText[:1024] + "..."
			}
			if bodyText == "" {
				log.Warnf("qoder: model list API returned status %d", resp.StatusCode)
			} else {
				log.Warnf("qoder: model list API returned status %d: %s", resp.StatusCode, bodyText)
			}
			if attempt == 0 && qoderIsLoginExpiredResponse(resp.StatusCode, body) {
				qoderClearSession(auth)
				continue
			}
			return gjson.Result{}, false
		}

		modelsResult := qoderModelArray(body)
		if !modelsResult.Exists() || !modelsResult.IsArray() {
			log.Warn("qoder: model list response missing models array")
			return gjson.Result{}, false
		}
		return modelsResult, true
	}
	return gjson.Result{}, false
}

func qoderModelArray(body []byte) gjson.Result {
	root := qoderParseMaybeEncodedJSON(body)
	if root.IsArray() {
		return root
	}
	if encodedBody := strings.TrimSpace(root.Get("body").String()); encodedBody != "" {
		decoded := qoderDecodeMaybeEncodedString(encodedBody)
		if len(decoded) > 0 {
			root = qoderParseMaybeEncodedJSON(decoded)
			if root.IsArray() {
				return root
			}
		}
	}
	for _, path := range []string{"chat", "data", "models", "data.models", "data.list", "data.items", "result.models", "result.list"} {
		result := root.Get(path)
		if result.Exists() && result.IsArray() {
			return result
		}
	}
	return gjson.Result{}
}

func qoderParseMaybeEncodedJSON(body []byte) gjson.Result {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return gjson.Result{}
	}
	if gjson.Valid(trimmed) {
		return gjson.Parse(trimmed)
	}
	decoded := qoderDecodeMaybeEncodedString(trimmed)
	if len(decoded) == 0 {
		return gjson.Result{}
	}
	decodedText := strings.TrimSpace(string(decoded))
	if !gjson.Valid(decodedText) {
		return gjson.Result{}
	}
	return gjson.Parse(decodedText)
}

func qoderDecodeMaybeEncodedString(encoded string) []byte {
	if encoded == "" {
		return nil
	}
	stdAlphabetIndex := make(map[rune]rune, len(qoder.CustomAlphabet)+1)
	for i, c := range qoder.CustomAlphabet {
		stdAlphabetIndex[c] = rune(qoder.StdAlphabet[i])
	}
	stdAlphabetIndex[qoder.CustomPad] = '='

	var stdBuilder strings.Builder
	for _, c := range encoded {
		mapped, ok := stdAlphabetIndex[c]
		if !ok {
			return nil
		}
		stdBuilder.WriteRune(mapped)
	}
	std := stdBuilder.String()
	n := len(std)
	if n == 0 {
		return nil
	}
	a := n / 3
	if a == 0 {
		return nil
	}
	rearranged := std[n-a:] + std[a:n-a] + std[:a]
	decoded, err := base64.StdEncoding.DecodeString(rearranged)
	if err != nil {
		return nil
	}
	return decoded
}

func QoderModelContractAuthID(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	return auth.ID
}

func fetchQoderModelCatalog(modelsResult gjson.Result, now int64) QoderModelCatalog {
	catalog := QoderModelCatalog{Contracts: make(map[string]QoderModelContract)}
	seen := make(map[string]struct{})
	modelsResult.ForEach(func(_, value gjson.Result) bool {
		model := qoderModelInfoFromResult(value, now)
		if model == nil {
			return true
		}
		key := normalizeQoderModelContractCacheKey(model.ID)
		if key == "" {
			return true
		}
		if _, ok := seen[key]; ok {
			return true
		}
		seen[key] = struct{}{}
		catalog.Models = append(catalog.Models, model)
		catalog.Contracts[key] = QoderModelContractFromResult(value)
		return true
	})
	return catalog
}

func qoderModelInfoFromResult(value gjson.Result, now int64) *registry.ModelInfo {
	id := strings.TrimSpace(firstNonEmptyResult(value,
		"key",
		"id",
		"model_key",
		"modelKey",
		"model",
		"name",
	))
	if id == "" {
		return nil
	}

	displayName := strings.TrimSpace(firstNonEmptyResult(value,
		"display_name",
		"displayName",
		"name",
		"label",
	))

	staticInfo := registry.LookupModelInfo(id, "qoder")
	if staticInfo == nil && displayName != "" {
		for _, candidate := range registry.GetQoderModels() {
			if candidate == nil {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(candidate.DisplayName), displayName) {
				staticInfo = candidate
				break
			}
		}
	}
	var model *registry.ModelInfo
	if staticInfo != nil {
		model = staticInfo
	} else {
		model = &registry.ModelInfo{
			ID:      id,
			Object:  "model",
			Created: now,
			OwnedBy: "qoder",
			Type:    "qoder",
		}
	}
	model.ID = id
	if model.Object == "" {
		model.Object = "model"
	}
	if model.OwnedBy == "" {
		model.OwnedBy = "qoder"
	}
	if model.Type == "" {
		model.Type = "qoder"
	}
	if model.Created == 0 {
		model.Created = now
	}

	if displayName == "" {
		displayName = id
	}
	model.DisplayName = displayName

	description := strings.TrimSpace(firstNonEmptyResult(value,
		"description",
		"desc",
		"description_en",
		"descriptionEn",
	))
	if description != "" {
		model.Description = description
	} else if strings.TrimSpace(model.Description) == "" {
		model.Description = displayName + " via Qoder"
	}

	if contextLength := firstPositiveIntResult(value,
		"context_length",
		"contextLength",
		"max_input_tokens",
		"maxInputTokens",
	); contextLength > 0 {
		model.ContextLength = contextLength
	}
	if maxCompletionTokens := firstPositiveIntResult(value,
		"max_output_tokens",
		"maxOutputTokens",
		"max_completion_tokens",
		"maxCompletionTokens",
	); maxCompletionTokens > 0 {
		model.MaxCompletionTokens = maxCompletionTokens
	}

	return model
}

func QoderModelContractFromResult(value gjson.Result) QoderModelContract {
	contract := QoderModelContract{
		Source:         strings.TrimSpace(firstNonEmptyResult(value, "source", "model_source", "modelSource")),
		AliyunUserType: strings.TrimSpace(firstNonEmptyResult(value, "aliyun_user_type", "aliyunUserType", "user_type", "userType")),
	}
	for _, path := range []string{"is_reasoning", "isReasoning", "reasoning", "supports_reasoning", "supportsReasoning"} {
		result := value.Get(path)
		if !result.Exists() {
			continue
		}
		contract.IsReasoning = qoderTruthyResult(result)
		break
	}
	return contract
}

func qoderTruthyResult(result gjson.Result) bool {
	switch result.Type {
	case gjson.True:
		return true
	case gjson.False, gjson.Null:
		return false
	case gjson.Number:
		return result.Int() != 0
	default:
		text := strings.TrimSpace(strings.ToLower(result.String()))
		if text == "" {
			return false
		}
		if n, err := strconv.Atoi(text); err == nil {
			return n != 0
		}
		return text == "true" || text == "yes" || text == "on"
	}
}

func firstNonEmptyResult(value gjson.Result, paths ...string) string {
	for _, path := range paths {
		candidate := strings.TrimSpace(value.Get(path).String())
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func firstPositiveIntResult(value gjson.Result, paths ...string) int {
	for _, path := range paths {
		candidate := int(value.Get(path).Int())
		if candidate > 0 {
			return candidate
		}
	}
	return 0
}
