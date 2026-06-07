package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

func TestQoderExecutorExecute_DoesNotUseUnknownThinkingProvider(t *testing.T) {
	var logOutput bytes.Buffer
	previousLogOutput := log.StandardLogger().Out
	previousLogLevel := log.GetLevel()
	log.SetOutput(&logOutput)
	log.SetLevel(log.DebugLevel)
	defer log.SetOutput(previousLogOutput)
	defer log.SetLevel(previousLogLevel)

	e := NewQoderExecutor(&config.Config{})
	_, err := e.Execute(context.Background(), nil, cliproxyexecutor.Request{
		Model:   "qwen3.7-max(high)",
		Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err == nil {
		t.Fatal("expected missing access token error")
	}
	if !strings.Contains(err.Error(), "missing access token") {
		t.Fatalf("Execute() error = %v, want missing access token", err)
	}
	if strings.Contains(logOutput.String(), "thinking: unknown provider, passthrough") {
		t.Fatalf("unexpected unknown thinking provider log: %s", logOutput.String())
	}
}

func TestQoderExecutorBuildOpenAIBody_AppliesThinkingSuffix(t *testing.T) {
	e := NewQoderExecutor(&config.Config{})
	body, baseModel, err := e.buildOpenAIBody(cliproxyexecutor.Request{
		Model:   "qwen3.7-max(high)",
		Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("buildOpenAIBody() error = %v", err)
	}
	if baseModel != "qwen3.7-max" {
		t.Fatalf("baseModel = %q, want %q", baseModel, "qwen3.7-max")
	}
	if got := gjson.GetBytes(body, "reasoning_effort").String(); got != "high" {
		t.Fatalf("reasoning_effort = %q, want %q, body=%s", got, "high", string(body))
	}
}

type qoderRoundTripFunc func(*http.Request) (*http.Response, error)

func (f qoderRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestFetchQoderModelCatalog_BuildsModelsAndContracts(t *testing.T) {
	modelsResult := qoderModelArray([]byte(`{"chat":[{"key":" qwen3.7-max ","display_name":"Qwen3.7-Max","source":"quota_free","is_reasoning":true,"aliyun_user_type":"personal_basic"},{"key":"qoder-new","display_name":"Qoder New","source":"quota_paid","is_reasoning":0,"aliyun_user_type":"team_enterprise"}]}`))
	catalog := fetchQoderModelCatalog(modelsResult, 123)
	if len(catalog.Models) != 2 {
		t.Fatalf("len(catalog.Models) = %d, want %d", len(catalog.Models), 2)
	}
	if len(catalog.Contracts) != 2 {
		t.Fatalf("len(catalog.Contracts) = %d, want %d", len(catalog.Contracts), 2)
	}

	byID := make(map[string]*registry.ModelInfo, len(catalog.Models))
	for _, model := range catalog.Models {
		if model == nil {
			continue
		}
		byID[model.ID] = model
	}
	if byID["qwen3.7-max"] == nil {
		t.Fatal("expected qwen3.7-max model in catalog")
	}
	if byID["qwen3.7-max"].Thinking == nil {
		t.Fatal("expected known qoder model to retain static thinking metadata")
	}
	if byID["qoder-new"] == nil {
		t.Fatal("expected qoder-new model in catalog")
	}
	if byID["qoder-new"].DisplayName != "Qoder New" {
		t.Fatalf("qoder-new display name = %q, want %q", byID["qoder-new"].DisplayName, "Qoder New")
	}

	wantKnown := qoderModelContract{Source: "quota_free", IsReasoning: true, AliyunUserType: "personal_basic"}
	if got := catalog.Contracts["qwen3.7-max"]; got != wantKnown {
		t.Fatalf("contract[qwen3.7-max] = %#v, want %#v", got, wantKnown)
	}
	wantNew := qoderModelContract{Source: "quota_paid", IsReasoning: false, AliyunUserType: "team_enterprise"}
	if got := catalog.Contracts["qoder-new"]; got != wantNew {
		t.Fatalf("contract[qoder-new] = %#v, want %#v", got, wantNew)
	}
}

func TestFetchQoderModels_UsesDynamicModelList(t *testing.T) {
	requestCount := 0
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", qoderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestCount++
		switch requestCount {
		case 1:
			if req.Method != http.MethodPost {
				t.Fatalf("exchange method = %q, want %q", req.Method, http.MethodPost)
			}
			if req.URL.String() != qoder.CenterBase+"/algo/api/v3/user/jobToken?Encode=1" {
				t.Fatalf("exchange url = %q, want %q", req.URL.String(), qoder.CenterBase+"/algo/api/v3/user/jobToken?Encode=1")
			}
			encodedBody, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("ReadAll(exchange body) error = %v", err)
			}
			outer := gjson.ParseBytes(decodeQoderBodyForTest(t, string(encodedBody)))
			inner := gjson.Parse(outer.Get("payload").String())
			if got := inner.Get("personalToken").String(); got != "" {
				t.Fatalf("personalToken = %q, want empty for browser token", got)
			}
			if got := inner.Get("securityOauthToken").String(); got != "token" {
				t.Fatalf("securityOauthToken = %q, want %q", got, "token")
			}
			if got := inner.Get("refreshToken").String(); got != "" {
				t.Fatalf("refreshToken = %q, want empty string during exchange", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"id":"user-1","name":"Qoder User","securityOauthToken":"session-token","refreshToken":"refresh-token","userType":"personal_standard"}`)),
				Request:    req,
			}, nil
		case 2:
			if req.Method != http.MethodGet {
				t.Fatalf("model list method = %q, want %q", req.Method, http.MethodGet)
			}
			if req.URL.String() != qoder.ChatBase+qoder.ModelListPath+"?Encode=1" {
				t.Fatalf("model list url = %q, want %q", req.URL.String(), qoder.ChatBase+qoder.ModelListPath+"?Encode=1")
			}
			if got := req.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer COSY.") {
				t.Fatalf("Authorization = %q, want COSY bearer signature", got)
			}
			if got := req.Header.Get("Cosy-Machinetoken"); got == "" || got == "machine-1" {
				t.Fatalf("Cosy-Machinetoken = %q, want exchanged/generated token", got)
			}
			if got := req.Header.Get("Cosy-Version"); got == "" {
				t.Fatal("expected Cosy-Version header")
			}
			if got := req.Header.Get("Cosy-Clienttype"); got != "5" {
				t.Fatalf("Cosy-Clienttype = %q, want %q", got, "5")
			}
			encodedResponse := customBase64Encode([]byte(`{"chat":[{"key":"qwen3.7-max","display_name":"Qwen3.7-Max"},{"key":"qoder-new","display_name":"Qoder New"}]}`))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"body":"` + encodedResponse + `"}`)),
				Request:    req,
			}, nil
		default:
			t.Fatalf("unexpected request %d: %s %s", requestCount, req.Method, req.URL.String())
			return nil, nil
		}
	}))

	models := FetchQoderModels(ctx, &cliproxyauth.Auth{
		Metadata: map[string]any{"access_token": "token", "refresh_token": "stale-refresh", "uid": "user-1", "machine_id": "machine-1"},
	}, &config.Config{})
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want %d", len(models), 2)
	}

	byID := make(map[string]*registry.ModelInfo, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		byID[model.ID] = model
	}
	if byID["qwen3.7-max"] == nil {
		t.Fatal("expected qwen3.7-max from dynamic model list")
	}
	if byID["qwen3.7-max"].Thinking == nil {
		t.Fatal("expected known qoder model to retain static thinking metadata")
	}
	if byID["qoder-new"] == nil {
		t.Fatal("expected qoder-new from dynamic model list")
	}
	if byID["qoder-new"].DisplayName != "Qoder New" {
		t.Fatalf("qoder-new display name = %q, want %q", byID["qoder-new"].DisplayName, "Qoder New")
	}
	if byID["qoder-new"].Type != "qoder" {
		t.Fatalf("qoder-new type = %q, want %q", byID["qoder-new"].Type, "qoder")
	}
}

func TestFetchQoderModels_FallsBackToStaticModelsOnAPIError(t *testing.T) {
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", qoderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"upstream failure"}`)),
			Request:    req,
		}, nil
	}))

	models := FetchQoderModels(ctx, &cliproxyauth.Auth{
		Metadata: map[string]any{"access_token": "token"},
	}, &config.Config{})

	if len(models) != len(registry.GetQoderModels()) {
		t.Fatalf("len(models) = %d, want static fallback size %d", len(models), len(registry.GetQoderModels()))
	}
	seenKnown := false
	for _, model := range models {
		if model != nil && model.ID == "qmodel_latest" {
			seenKnown = true
			break
		}
	}
	if !seenKnown {
		t.Fatal("expected static fallback to include qmodel_latest")
	}
}

func TestFetchQoderModels_UsesCosySignature(t *testing.T) {
	requestCount := 0
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", qoderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestCount++
		switch requestCount {
		case 1:
			if req.URL.String() != qoder.CenterBase+"/algo/api/v3/user/jobToken?Encode=1" {
				t.Fatalf("exchange url = %q, want %q", req.URL.String(), qoder.CenterBase+"/algo/api/v3/user/jobToken?Encode=1")
			}
			encodedBody, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("ReadAll(exchange body) error = %v", err)
			}
			outer := gjson.ParseBytes(decodeQoderBodyForTest(t, string(encodedBody)))
			inner := gjson.Parse(outer.Get("payload").String())
			if got := inner.Get("personalToken").String(); got != "qdr_pat_token" {
				t.Fatalf("personalToken = %q, want %q", got, "qdr_pat_token")
			}
			if got := inner.Get("securityOauthToken").String(); got != "" {
				t.Fatalf("securityOauthToken = %q, want empty for PAT exchange", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"id":"u1","name":"Qoder User","securityOauthToken":"session-token","refreshToken":"refresh-token","userType":"personal_standard"}`)),
				Request:    req,
			}, nil
		case 2:
			if req.Method != http.MethodGet {
				t.Fatalf("method = %q, want %q", req.Method, http.MethodGet)
			}
			if req.URL.String() != qoder.ChatBase+qoder.ModelListPath+"?Encode=1" {
				t.Fatalf("request url = %q, want %q", req.URL.String(), qoder.ChatBase+qoder.ModelListPath+"?Encode=1")
			}
			if got := req.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer COSY.") {
				t.Fatalf("Authorization = %q, want COSY bearer signature", got)
			}
			if got := req.Header.Get("Cosy-Key"); got == "" {
				t.Fatal("expected Cosy-Key header")
			}
			if got := req.Header.Get("Cosy-Date"); got == "" {
				t.Fatal("expected Cosy-Date header")
			}
			wantSigPath := strings.TrimPrefix(qoder.ModelListPath, "/algo")
			if got := req.Header.Get("Cosy-Sigpath"); got != wantSigPath {
				t.Fatalf("Cosy-Sigpath = %q, want %q", got, wantSigPath)
			}
			if got := req.Header.Get("Cosy-Bodyhash"); got == "" {
				t.Fatal("expected Cosy-Bodyhash header")
			}
			if got := req.Header.Get("Cosy-Machinetoken"); got == "" || got == "m1" {
				t.Fatalf("Cosy-Machinetoken = %q, want generated session token", got)
			}
			encodedResponse := customBase64Encode([]byte(`{"chat":[{"key":"qwen3.7-max","display_name":"Qwen3.7-Max"}]}`))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(encodedResponse)),
				Request:    req,
			}, nil
		default:
			t.Fatalf("unexpected request %d: %s %s", requestCount, req.Method, req.URL.String())
			return nil, nil
		}
	}))

	models := FetchQoderModels(ctx, &cliproxyauth.Auth{
		Metadata: map[string]any{"auth_method": "pat", "personal_access_token": "qdr_pat_token", "uid": "u1", "machine_id": "m1"},
	}, &config.Config{})
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want %d", len(models), 1)
	}
}

func TestQoderExecutorExecute_RefreshesExpiredSessionFromModelList(t *testing.T) {
	requestCount := 0
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", qoderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestCount++
		switch requestCount {
		case 1:
			if req.Method != http.MethodGet {
				t.Fatalf("first request method = %q, want %q", req.Method, http.MethodGet)
			}
			if req.URL.String() != qoder.ChatBase+qoder.ModelListPath+"?Encode=1" {
				t.Fatalf("first request url = %q, want %q", req.URL.String(), qoder.ChatBase+qoder.ModelListPath+"?Encode=1")
			}
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"code":"105","message":"Login expired"}`)),
				Request:    req,
			}, nil
		case 2:
			if req.Method != http.MethodPost {
				t.Fatalf("exchange method = %q, want %q", req.Method, http.MethodPost)
			}
			if req.URL.String() != qoder.CenterBase+"/algo/api/v3/user/jobToken?Encode=1" {
				t.Fatalf("exchange url = %q, want %q", req.URL.String(), qoder.CenterBase+"/algo/api/v3/user/jobToken?Encode=1")
			}
			encodedBody, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("ReadAll(exchange body) error = %v", err)
			}
			outer := gjson.ParseBytes(decodeQoderBodyForTest(t, string(encodedBody)))
			inner := gjson.Parse(outer.Get("payload").String())
			if got := inner.Get("securityOauthToken").String(); got != "source-token" {
				t.Fatalf("securityOauthToken = %q, want %q", got, "source-token")
			}
			if got := inner.Get("refreshToken").String(); got != "" {
				t.Fatalf("refreshToken = %q, want empty string during re-exchange", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"id":"u1","name":"Qoder User","securityOauthToken":"fresh-session-token","refreshToken":"fresh-refresh-token","userType":"personal_standard"}`)),
				Request:    req,
			}, nil
		case 3:
			if req.Method != http.MethodGet {
				t.Fatalf("model list retry method = %q, want %q", req.Method, http.MethodGet)
			}
			if got := req.Header.Get("Cosy-Machinetoken"); got == "" || got == "stale-machine-token" {
				t.Fatalf("Cosy-Machinetoken = %q, want refreshed session token", got)
			}
			encodedResponse := customBase64Encode([]byte(`{"chat":[{"key":"qwen3.7-max","display_name":"Qwen3.7-Max","source":"quota_free"}]}`))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"body":"` + encodedResponse + `"}`)),
				Request:    req,
			}, nil
		case 4:
			if req.Method != http.MethodPost {
				t.Fatalf("chat method = %q, want %q", req.Method, http.MethodPost)
			}
			if req.URL.String() != qoder.ChatBase+qoder.ChatPath+"?"+qoder.ChatQueryExtra {
				t.Fatalf("chat url = %q, want %q", req.URL.String(), qoder.ChatBase+qoder.ChatPath+"?"+qoder.ChatQueryExtra)
			}
			if got := req.Header.Get("x-model-source"); got != "quota_free" {
				t.Fatalf("x-model-source = %q, want %q", got, "quota_free")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("data: {\"body\":\"{\\\"choices\\\":[{\\\"delta\\\":{\\\"content\\\":\\\"ok\\\"},\\\"finish_reason\\\":\\\"stop\\\"}]}\"}\n\ndata: [DONE]\n")),
				Request:    req,
			}, nil
		default:
			t.Fatalf("unexpected request %d: %s %s", requestCount, req.Method, req.URL.String())
			return nil, nil
		}
	}))

	auth := &cliproxyauth.Auth{
		ID: "qoder-auth-refresh",
		Metadata: map[string]any{
			"access_token":         "source-token",
			"security_oauth_token": "stale-session-token",
			"refresh_token":        "stale-refresh-token",
			"user_type":            "personal_standard",
			"uid":                  "u1",
			"machine_id":           "m1",
			"machine_token":        "stale-machine-token",
			"machine_type":         "stale-machine-type",
		},
	}
	models := FetchQoderModels(ctx, auth, &config.Config{})
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want %d", len(models), 1)
	}

	e := NewQoderExecutor(&config.Config{})
	_, err := e.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "qwen3.7-max",
		Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestQoderExecutorExecute_UsesCachedContractWithoutModelListFetch(t *testing.T) {
	const authID = "qoder-auth"
	ClearQoderModelContracts(authID)
	defer ClearQoderModelContracts(authID)
	StoreQoderModelContracts(authID, map[string]qoderModelContract{
		"qwen3.7-max": {
			Source:         "quota_free",
			IsReasoning:    true,
			AliyunUserType: "personal_basic",
		},
	})

	requestCount := 0
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", qoderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestCount++
		switch requestCount {
		case 1:
			if req.URL.String() != qoder.CenterBase+"/algo/api/v3/user/jobToken?Encode=1" {
				t.Fatalf("exchange url = %q, want %q", req.URL.String(), qoder.CenterBase+"/algo/api/v3/user/jobToken?Encode=1")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"id":"u1","name":"Qoder User","securityOauthToken":"session-token","refreshToken":"refresh-token","userType":"personal_standard"}`)),
				Request:    req,
			}, nil
		case 2:
			if req.URL.String() != qoder.ChatBase+qoder.ChatPath+"?"+qoder.ChatQueryExtra {
				t.Fatalf("chat url = %q, want %q", req.URL.String(), qoder.ChatBase+qoder.ChatPath+"?"+qoder.ChatQueryExtra)
			}
			if got := req.Header.Get("x-model-source"); got != "quota_free" {
				t.Fatalf("x-model-source = %q, want %q", got, "quota_free")
			}
			encodedBody, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("ReadAll(req.Body) error = %v", err)
			}
			decodedBody := decodeQoderBodyForTest(t, string(encodedBody))
			if got := gjson.GetBytes(decodedBody, "model_config.source").String(); got != "quota_free" {
				t.Fatalf("model_config.source = %q, want %q", got, "quota_free")
			}
			if got := gjson.GetBytes(decodedBody, "model_config.is_reasoning").Bool(); !got {
				t.Fatal("expected model_config.is_reasoning = true")
			}
			if got := gjson.GetBytes(decodedBody, "chat_context.extra.modelConfig.source").String(); got != "quota_free" {
				t.Fatalf("chat_context.extra.modelConfig.source = %q, want %q", got, "quota_free")
			}
			if got := gjson.GetBytes(decodedBody, "chat_context.extra.modelConfig.is_reasoning").Bool(); !got {
				t.Fatal("expected chat_context.extra.modelConfig.is_reasoning = true")
			}
			if got := gjson.GetBytes(decodedBody, "aliyun_user_type").String(); got != "personal_basic" {
				t.Fatalf("aliyun_user_type = %q, want %q", got, "personal_basic")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("data: {\"body\":\"{\\\"choices\\\":[{\\\"delta\\\":{\\\"content\\\":\\\"ok\\\"},\\\"finish_reason\\\":\\\"stop\\\"}]}\"}\n\ndata: [DONE]\n")),
				Request:    req,
			}, nil
		default:
			t.Fatalf("unexpected request %d: %s %s", requestCount, req.Method, req.URL.String())
			return nil, nil
		}
	}))

	e := NewQoderExecutor(&config.Config{})
	_, err := e.Execute(ctx, &cliproxyauth.Auth{
		ID:       authID,
		Metadata: map[string]any{"access_token": "token", "uid": "u1", "machine_id": "m1"},
	}, cliproxyexecutor.Request{
		Model:   "qwen3.7-max",
		Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestQoderExecutorExecute_UsesDynamicModelContract(t *testing.T) {
	const authID = "qoder-dynamic-auth"
	ClearQoderModelContracts(authID)
	defer ClearQoderModelContracts(authID)

	requestCount := 0
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", qoderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestCount++
		switch requestCount {
		case 1:
			if req.URL.String() != qoder.CenterBase+"/algo/api/v3/user/jobToken?Encode=1" {
				t.Fatalf("exchange url = %q, want %q", req.URL.String(), qoder.CenterBase+"/algo/api/v3/user/jobToken?Encode=1")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"id":"u1","name":"Qoder User","securityOauthToken":"session-token","refreshToken":"refresh-token","userType":"personal_standard"}`)),
				Request:    req,
			}, nil
		case 2:
			if req.URL.String() != qoder.ChatBase+qoder.ModelListPath+"?Encode=1" {
				t.Fatalf("model list url = %q, want %q", req.URL.String(), qoder.ChatBase+qoder.ModelListPath+"?Encode=1")
			}
			encodedResponse := customBase64Encode([]byte(`{"chat":[{"key":"qwen3.7-max","display_name":"Qwen3.7-Max","source":"quota_free","is_reasoning":true,"aliyun_user_type":"personal_basic"}]}`))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"body":"` + encodedResponse + `"}`)),
				Request:    req,
			}, nil
		case 3:
			if req.URL.String() != qoder.ChatBase+qoder.ChatPath+"?"+qoder.ChatQueryExtra {
				t.Fatalf("chat url = %q, want %q", req.URL.String(), qoder.ChatBase+qoder.ChatPath+"?"+qoder.ChatQueryExtra)
			}
			if got := req.Header.Get("x-model-source"); got != "quota_free" {
				t.Fatalf("x-model-source = %q, want %q", got, "quota_free")
			}
			encodedBody, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("ReadAll(req.Body) error = %v", err)
			}
			decodedBody := decodeQoderBodyForTest(t, string(encodedBody))
			if got := gjson.GetBytes(decodedBody, "model_config.source").String(); got != "quota_free" {
				t.Fatalf("model_config.source = %q, want %q", got, "quota_free")
			}
			if got := gjson.GetBytes(decodedBody, "model_config.is_reasoning").Bool(); !got {
				t.Fatal("expected model_config.is_reasoning = true")
			}
			if got := gjson.GetBytes(decodedBody, "chat_context.extra.modelConfig.is_reasoning").Bool(); !got {
				t.Fatal("expected chat_context.extra.modelConfig.is_reasoning = true")
			}
			if got := gjson.GetBytes(decodedBody, "aliyun_user_type").String(); got != "personal_basic" {
				t.Fatalf("aliyun_user_type = %q, want %q", got, "personal_basic")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("data: {\"body\":\"{\\\"choices\\\":[{\\\"delta\\\":{\\\"content\\\":\\\"ok\\\"},\\\"finish_reason\\\":\\\"stop\\\"}]}\"}\n\ndata: [DONE]\n")),
				Request:    req,
			}, nil
		default:
			t.Fatalf("unexpected request %d: %s", requestCount, req.URL.String())
			return nil, nil
		}
	}))

	auth := &cliproxyauth.Auth{
		ID:       authID,
		Metadata: map[string]any{"access_token": "token", "uid": "u1", "machine_id": "m1"},
	}
	models := FetchQoderModels(ctx, auth, &config.Config{})
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want %d", len(models), 1)
	}

	e := NewQoderExecutor(&config.Config{})
	_, err := e.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "qwen3.7-max",
		Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestQoderModelContractCache_StoresNormalizesAndClears(t *testing.T) {
	ClearQoderModelContracts("auth-a")
	ClearQoderModelContracts("auth-b")

	if got, ok := LoadQoderModelContract(" auth-a ", " qwen3.7-max "); ok || got != (qoderModelContract{}) {
		t.Fatalf("LoadQoderModelContract() before store = (%#v, %v), want zero value and false", got, ok)
	}

	wantA := qoderModelContract{Source: "quota_free", IsReasoning: true, AliyunUserType: "personal_basic"}
	wantB := qoderModelContract{Source: "quota_paid", IsReasoning: false, AliyunUserType: "team_enterprise"}
	StoreQoderModelContracts(" AUTH-A ", map[string]qoderModelContract{" QWEN3.7-MAX ": wantA})
	StoreQoderModelContracts(" auth-b ", map[string]qoderModelContract{"qwen3.7-max": wantB})

	if got, ok := LoadQoderModelContract("auth-a", "qwen3.7-max"); !ok || got != wantA {
		t.Fatalf("LoadQoderModelContract(auth-a) = (%#v, %v), want (%#v, true)", got, ok, wantA)
	}
	if got, ok := LoadQoderModelContract("  aUtH-A  ", "  qWeN3.7-MaX  "); !ok || got != wantA {
		t.Fatalf("LoadQoderModelContract(normalized auth-a) = (%#v, %v), want (%#v, true)", got, ok, wantA)
	}
	if got, ok := LoadQoderModelContract("AUTH-B", "QWEN3.7-MAX"); !ok || got != wantB {
		t.Fatalf("LoadQoderModelContract(auth-b) = (%#v, %v), want (%#v, true)", got, ok, wantB)
	}

	ClearQoderModelContracts(" auth-a ")
	if got, ok := LoadQoderModelContract("auth-a", "qwen3.7-max"); ok || got != (qoderModelContract{}) {
		t.Fatalf("LoadQoderModelContract(auth-a) after clear = (%#v, %v), want zero value and false", got, ok)
	}
	if got, ok := LoadQoderModelContract("auth-b", "qwen3.7-max"); !ok || got != wantB {
		t.Fatalf("LoadQoderModelContract(auth-b) after clearing auth-a = (%#v, %v), want (%#v, true)", got, ok, wantB)
	}

	ClearQoderModelContracts("auth-b")
}

func TestQoderExecutorBuildQoderRequestBody_DoesNotSetQoderCLISessionType(t *testing.T) {
	e := NewQoderExecutor(&config.Config{})
	body := e.buildQoderRequestBody([]byte(`{"messages":[{"role":"user","content":"hi"}]}`), "qwen3.7-max", qoderModelContract{})
	if got, ok := body["session_type"]; ok {
		t.Fatalf("session_type = %#v, want field omitted", got)
	}
}

func TestQoderExecutorExecute_LogsRequestDiagnostics(t *testing.T) {
	var logOutput bytes.Buffer
	previousLogOutput := log.StandardLogger().Out
	previousLogLevel := log.GetLevel()
	log.SetOutput(&logOutput)
	log.SetLevel(log.DebugLevel)
	defer log.SetOutput(previousLogOutput)
	defer log.SetLevel(previousLogLevel)

	requestCount := 0
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", qoderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestCount++
		switch requestCount {
		case 1:
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"id":"u1","name":"Qoder User","securityOauthToken":"session-token","refreshToken":"refresh-token","userType":"personal_standard"}`)),
				Request:    req,
			}, nil
		case 2:
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(strings.Join([]string{
					`data: {"body":"{\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}"}`,
					`data: {"body":"[DONE]"}`,
				}, "\n"))),
				Request: req,
			}, nil
		default:
			t.Fatalf("unexpected request %d: %s %s", requestCount, req.Method, req.URL.String())
			return nil, nil
		}
	}))

	e := NewQoderExecutor(&config.Config{})
	_, err := e.Execute(ctx, &cliproxyauth.Auth{
		Metadata: map[string]any{
			"auth_method":           "pat",
			"personal_access_token": "qdr_pat_token",
			"uid":                   "u1",
			"machine_id":            "m1",
		},
	}, cliproxyexecutor.Request{
		Model: "qwen3.7-max",
		Payload: []byte(`{
			"messages":[{"role":"user","content":"read the repo and update the config loader"}],
			"tools":[
				{"type":"function","function":{"name":"glob","description":"find files","parameters":{"type":"object"}}},
				{"type":"function","function":{"name":"read","description":"read file","parameters":{"type":"object"}}}
			]
		}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	logText := logOutput.String()
	if !strings.Contains(logText, "qoder executor: request diagnostics") {
		t.Fatalf("expected request diagnostics log, got: %s", logText)
	}
	for _, want := range []string{"model=qwen3.7-max", "messages=1", "tools=2", "body_bytes="} {
		if !strings.Contains(logText, want) {
			t.Fatalf("expected request diagnostics log to contain %q, got: %s", want, logText)
		}
	}
}

func TestQoderExecutorExecute_LogsUpstreamErrorDiagnostics(t *testing.T) {
	var logOutput bytes.Buffer
	previousLogOutput := log.StandardLogger().Out
	previousLogLevel := log.GetLevel()
	log.SetOutput(&logOutput)
	log.SetLevel(log.DebugLevel)
	defer log.SetOutput(previousLogOutput)
	defer log.SetLevel(previousLogLevel)

	requestCount := 0
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", qoderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestCount++
		switch requestCount {
		case 1:
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"id":"u1","name":"Qoder User","securityOauthToken":"session-token","refreshToken":"refresh-token","userType":"personal_standard"}`)),
				Request:    req,
			}, nil
		case 2:
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"tool context too large"}}`)),
				Request:    req,
			}, nil
		default:
			t.Fatalf("unexpected request %d: %s %s", requestCount, req.Method, req.URL.String())
			return nil, nil
		}
	}))

	e := NewQoderExecutor(&config.Config{})
	_, err := e.Execute(ctx, &cliproxyauth.Auth{
		Metadata: map[string]any{
			"auth_method":           "pat",
			"personal_access_token": "qdr_pat_token",
			"uid":                   "u1",
			"machine_id":            "m1",
		},
	}, cliproxyexecutor.Request{
		Model:   "qmodel_latest",
		Payload: []byte(`{"messages":[{"role":"user","content":"read the full repo and update multiple files"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: "qwen3.7-max",
		},
	})
	if err == nil {
		t.Fatal("expected upstream 500 error")
	}

	logText := logOutput.String()
	if !strings.Contains(logText, "qoder executor: upstream error diagnostics") {
		t.Fatalf("expected upstream error diagnostics log, got: %s", logText)
	}
	for _, want := range []string{"status=500", "requested_model=qwen3.7-max", "upstream_model=qmodel_latest", "body_bytes=", "tool context too large"} {
		if !strings.Contains(logText, want) {
			t.Fatalf("expected upstream error diagnostics log to contain %q, got: %s", want, logText)
		}
	}
}

func TestQoderExecutorExecuteStream_LogsBootstrapDiagnostics(t *testing.T) {
	var logOutput bytes.Buffer
	previousLogOutput := log.StandardLogger().Out
	previousLogLevel := log.GetLevel()
	log.SetOutput(&logOutput)
	log.SetLevel(log.DebugLevel)
	defer log.SetOutput(previousLogOutput)
	defer log.SetLevel(previousLogLevel)

	requestCount := 0
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", qoderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestCount++
		switch requestCount {
		case 1:
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"id":"u1","name":"Qoder User","securityOauthToken":"session-token","refreshToken":"refresh-token","userType":"personal_standard"}`)),
				Request:    req,
			}, nil
		case 2:
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(strings.Join([]string{
					``,
					`data: {"body":"{\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}"}`,
					`data: {"body":"[DONE]"}`,
				}, "\n"))),
				Request: req,
			}, nil
		default:
			t.Fatalf("unexpected request %d: %s %s", requestCount, req.Method, req.URL.String())
			return nil, nil
		}
	}))

	e := NewQoderExecutor(&config.Config{})
	streamResult, err := e.ExecuteStream(ctx, &cliproxyauth.Auth{
		Metadata: map[string]any{
			"auth_method":           "pat",
			"personal_access_token": "qdr_pat_token",
			"uid":                   "u1",
			"machine_id":            "m1",
		},
	}, cliproxyexecutor.Request{
		Model:   "qmodel_latest",
		Payload: []byte(`{"messages":[{"role":"user","content":"read the full repo"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: "qwen3.7-max",
		},
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
		}
	}

	logText := logOutput.String()
	for _, want := range []string{
		"qoder executor: stream bootstrap diagnostics stage=stream_loop_exit",
		"ctx_err=none",
		"scanner_err=none",
		"requested_model=qwen3.7-max",
		"upstream_model=qmodel_latest",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("expected stream bootstrap diagnostics log to contain %q, got: %s", want, logText)
		}
	}
}

func TestQoderExecutorExecute_NonStreamUsesStreamTransportSemantics(t *testing.T) {
	requestCount := 0
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", qoderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestCount++
		switch requestCount {
		case 1:
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"id":"u1","name":"Qoder User","securityOauthToken":"session-token","refreshToken":"refresh-token","userType":"personal_standard"}`)),
				Request:    req,
			}, nil
		case 2:
			encodedResponse := customBase64Encode([]byte(`{"chat":[{"key":"qwen3.7-max","display_name":"Qwen3.7-Max"}]}`))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"body":"` + encodedResponse + `"}`)),
				Request:    req,
			}, nil
		case 3:
			if got := req.Header.Get("Accept"); got != "text/event-stream" {
				t.Fatalf("Accept = %q, want %q", got, "text/event-stream")
			}
			if got := req.Header.Get("Cache-Control"); got != "no-cache" {
				t.Fatalf("Cache-Control = %q, want %q", got, "no-cache")
			}
			parts := strings.Split(req.Header.Get("Authorization"), ".")
			if len(parts) != 3 {
				t.Fatalf("Authorization format = %q, want Bearer COSY payload.signature", req.Header.Get("Authorization"))
			}
			payloadJSON, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(parts[1], "Bearer COSY."))
			if err != nil {
				t.Fatalf("DecodeString(payload) error = %v", err)
			}
			if got := gjson.GetBytes(payloadJSON, "ideVersion").String(); got != "" {
				t.Fatalf("ideVersion = %q, want empty string", got)
			}
			encodedBody, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("ReadAll(req.Body) error = %v", err)
			}
			decodedBody := decodeQoderBodyForTest(t, string(encodedBody))
			if got := gjson.GetBytes(decodedBody, "stream").Bool(); !got {
				t.Fatalf("stream = %v, want true for non-stream Execute transport", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("data: {\"body\":\"{\\\"choices\\\":[{\\\"delta\\\":{\\\"content\\\":\\\"ok\\\"},\\\"finish_reason\\\":\\\"stop\\\"}]}\"}\n\ndata: [DONE]\n")),
				Request:    req,
			}, nil
		default:
			t.Fatalf("unexpected request %d: %s", requestCount, req.URL.String())
			return nil, nil
		}
	}))

	e := NewQoderExecutor(&config.Config{})
	_, err := e.Execute(ctx, &cliproxyauth.Auth{
		Metadata: map[string]any{"access_token": "token", "uid": "u1", "machine_id": "m1"},
	}, cliproxyexecutor.Request{
		Model:   "qwen3.7-max",
		Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestQoderExecutorBuildQoderRequestBody_CopiesIncomingTools(t *testing.T) {
	e := NewQoderExecutor(&config.Config{})
	body := e.buildQoderRequestBody([]byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"get_weather","description":"Get weather"}}]}`), "qwen3.7-max", qoderModelContract{})
	if got := gjson.GetBytes(mustMarshalJSON(t, body), "tools.0.function.name").String(); got != "get_weather" {
		t.Fatalf("tools[0].function.name = %q, want %q", got, "get_weather")
	}
}

func TestQoderExecutorBuildQoderRequestBody_DefaultsToolsToEmptyArray(t *testing.T) {
	e := NewQoderExecutor(&config.Config{})
	body := e.buildQoderRequestBody([]byte(`{"messages":[{"role":"user","content":"hi"}]}`), "qwen3.7-max", qoderModelContract{})
	data := mustMarshalJSON(t, body)
	if got := gjson.GetBytes(data, "tools").Type.String(); got != "JSON" {
		t.Fatalf("tools type = %q, want JSON array", got)
	}
	if got := gjson.GetBytes(data, "tools.#").Int(); got != 0 {
		t.Fatalf("tools length = %d, want %d", got, 0)
	}
}

func TestQoderExecutorBuildQoderRequestBody_ReusesSessionIdentifiersFromMetadataUserID(t *testing.T) {
	e := NewQoderExecutor(&config.Config{})
	payload := []byte(`{"metadata":{"user_id":"{\"session_id\":\"trae-session-1\"}"},"messages":[{"role":"user","content":"hi"}]}`)
	first := mustMarshalJSON(t, e.buildQoderRequestBody(payload, "qwen3.7-max", qoderModelContract{}))
	second := mustMarshalJSON(t, e.buildQoderRequestBody(payload, "qwen3.7-max", qoderModelContract{}))
	firstSessionID := gjson.GetBytes(first, "session_id").String()
	secondSessionID := gjson.GetBytes(second, "session_id").String()
	if firstSessionID == "" || secondSessionID == "" {
		t.Fatalf("session_id must be populated, got %q and %q", firstSessionID, secondSessionID)
	}
	if firstSessionID != secondSessionID {
		t.Fatalf("session_id = %q then %q, want stable value for same incoming session", firstSessionID, secondSessionID)
	}
	firstChatRecordID := gjson.GetBytes(first, "chat_record_id").String()
	secondChatRecordID := gjson.GetBytes(second, "chat_record_id").String()
	if firstChatRecordID == "" || secondChatRecordID == "" {
		t.Fatalf("chat_record_id must be populated, got %q and %q", firstChatRecordID, secondChatRecordID)
	}
	if firstChatRecordID != secondChatRecordID {
		t.Fatalf("chat_record_id = %q then %q, want stable value for same incoming session", firstChatRecordID, secondChatRecordID)
	}
}

func TestQoderExecutorBuildQoderRequestBody_SetsBusinessNameFromPrompt(t *testing.T) {
	e := NewQoderExecutor(&config.Config{})
	prompt := "123456789012345678901234567890EXTRA"
	body := e.buildQoderRequestBody([]byte(`{"messages":[{"role":"user","content":"`+prompt+`"}]}`), "qwen3.7-max", qoderModelContract{})
	if got := gjson.GetBytes(mustMarshalJSON(t, body), "business.name").String(); got != "123456789012345678901234567890" {
		t.Fatalf("business.name = %q, want %q", got, "123456789012345678901234567890")
	}
}

func TestQoderExecutorBuildQoderRequestBody_PreservesConversationTurns(t *testing.T) {
	e := NewQoderExecutor(&config.Config{})
	body := e.buildQoderRequestBody([]byte(`{"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"reply"},{"role":"user","content":"second"}]}`), "qwen3.7-max", qoderModelContract{})
	data := mustMarshalJSON(t, body)
	if got := gjson.GetBytes(data, "messages.#").Int(); got != 3 {
		t.Fatalf("messages count = %d, want %d", got, 3)
	}
	if got := gjson.GetBytes(data, "messages.0.contents.0.text").String(); got != "first" {
		t.Fatalf("messages[0] text = %q, want %q", got, "first")
	}
	if got := gjson.GetBytes(data, "messages.1.content").String(); got != "reply" {
		t.Fatalf("messages[1].content = %q, want %q", got, "reply")
	}
	if got := gjson.GetBytes(data, "messages.2.contents.0.text").String(); got != "second" {
		t.Fatalf("messages[2] text = %q, want %q", got, "second")
	}
}

func TestQoderExecutorBuildQoderRequestBody_NormalizesArrayUserContent(t *testing.T) {
	e := NewQoderExecutor(&config.Config{})
	body := e.buildQoderRequestBody([]byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"text","text":"world"}]}]}`), "qwen3.7-max", qoderModelContract{})
	data := mustMarshalJSON(t, body)
	want := "hello\n\nworld"
	if got := gjson.GetBytes(data, "chat_context.text.text").String(); got != want {
		t.Fatalf("chat_context.text.text = %q, want %q", got, want)
	}
	if got := gjson.GetBytes(data, "messages.0.contents.0.text").String(); got != want {
		t.Fatalf("messages[0].contents[0].text = %q, want %q", got, want)
	}
}

func TestQoderExecutorBuildQoderRequestBody_DowngradesAssistantToolCallsWhenToolsDisabled(t *testing.T) {
	e := NewQoderExecutor(&config.Config{})
	body := e.buildQoderRequestBody([]byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]}]}`), "qwen3.7-max", qoderModelContract{})
	data := mustMarshalJSON(t, body)
	got := gjson.GetBytes(data, "messages.0.content").String()
	if !strings.HasPrefix(got, "Tool calls:\n[") {
		t.Fatalf("messages[0].content = %q, want Tool calls summary prefix", got)
	}
	if !strings.Contains(got, `"name":"get_weather"`) {
		t.Fatalf("messages[0].content = %q, want tool name included", got)
	}
	if !strings.Contains(got, `"arguments":"{\"city\":\"Paris\"}"`) {
		t.Fatalf("messages[0].content = %q, want tool arguments included", got)
	}
	if got := gjson.GetBytes(data, "messages.0.tool_calls").Exists(); got {
		t.Fatal("expected messages[0].tool_calls omitted when request tools are disabled")
	}
}

func TestQoderExecutorBuildQoderRequestBody_RendersToolResultAsUserWhenToolsDisabled(t *testing.T) {
	e := NewQoderExecutor(&config.Config{})
	body := e.buildQoderRequestBody([]byte(`{"messages":[{"role":"tool","name":"get_weather","tool_call_id":"call_1","content":"sunny"}]}`), "qwen3.7-max", qoderModelContract{})
	data := mustMarshalJSON(t, body)
	want := "Tool result (get_weather) [call_1]:\nsunny"
	if got := gjson.GetBytes(data, "messages.0.role").String(); got != "user" {
		t.Fatalf("messages[0].role = %q, want %q", got, "user")
	}
	if got := gjson.GetBytes(data, "messages.0.contents.0.text").String(); got != want {
		t.Fatalf("messages[0].contents[0].text = %q, want %q", got, want)
	}
}

func TestQoderExecutorBuildQoderRequestBody_SummarizesUnresolvedToolCallsWhenToolsEnabled(t *testing.T) {
	e := NewQoderExecutor(&config.Config{})
	body := e.buildQoderRequestBody([]byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},{"role":"user","content":"continue"}],"tools":[{"type":"function","function":{"name":"get_weather"}}]}`), "qwen3.7-max", qoderModelContract{})
	data := mustMarshalJSON(t, body)
	want := "Previously planned but unexecuted tool calls: get_weather."
	if got := gjson.GetBytes(data, "messages.0.content").String(); got != want {
		t.Fatalf("messages[0].content = %q, want %q", got, want)
	}
	if got := gjson.GetBytes(data, "messages.0.tool_calls").Exists(); got {
		t.Fatal("expected unresolved assistant tool_calls to be summarized instead of preserved")
	}
}

func TestQoderExecutorBuildQoderRequestBody_ParsesResolvedToolCallsFromAssistantText(t *testing.T) {
	e := NewQoderExecutor(&config.Config{})
	body := e.buildQoderRequestBody([]byte(`{"messages":[{"role":"assistant","content":"Tool calls:\n[{\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"{\\\"city\\\":\\\"Paris\\\"}\"}}]"},{"role":"tool","name":"get_weather","tool_call_id":"call_1","content":"sunny"}],"tools":[{"type":"function","function":{"name":"get_weather"}}]}`), "qwen3.7-max", qoderModelContract{})
	data := mustMarshalJSON(t, body)
	if got := gjson.GetBytes(data, "messages.0.content").String(); got != "" {
		t.Fatalf("messages[0].content = %q, want empty after structured tool_call extraction", got)
	}
	if got := gjson.GetBytes(data, "messages.0.tool_calls.0.function.name").String(); got != "get_weather" {
		t.Fatalf("messages[0].tool_calls[0].function.name = %q, want %q", got, "get_weather")
	}
	if got := gjson.GetBytes(data, "messages.1.role").String(); got != "tool" {
		t.Fatalf("messages[1].role = %q, want %q", got, "tool")
	}
}

func TestQoderExecutorBuildQoderRequestBody_SummarizesUnresolvedToolCallsFromAssistantText(t *testing.T) {
	e := NewQoderExecutor(&config.Config{})
	body := e.buildQoderRequestBody([]byte(`{"messages":[{"role":"assistant","content":"Tool calls:\n[{\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"{}\"}}]"},{"role":"user","content":"continue"}],"tools":[{"type":"function","function":{"name":"get_weather"}}]}`), "qwen3.7-max", qoderModelContract{})
	data := mustMarshalJSON(t, body)
	want := "Previously planned but unexecuted tool calls: get_weather."
	if got := gjson.GetBytes(data, "messages.0.content").String(); got != want {
		t.Fatalf("messages[0].content = %q, want %q", got, want)
	}
	if got := gjson.GetBytes(data, "messages.0.tool_calls").Exists(); got {
		t.Fatal("expected unresolved text tool calls to be summarized instead of preserved")
	}
}

func TestQoderExecutorBuildQoderRequestBody_NormalizesStructuredToolCallArguments(t *testing.T) {
	e := NewQoderExecutor(&config.Config{})
	body := e.buildQoderRequestBody([]byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":{"city":"Paris"}}}]},{"role":"tool","name":"get_weather","tool_call_id":"call_1","content":"sunny"}],"tools":[{"type":"function","function":{"name":"get_weather"}}]}`), "qwen3.7-max", qoderModelContract{})
	data := mustMarshalJSON(t, body)
	if got := gjson.GetBytes(data, "messages.0.tool_calls.0.function.arguments").Raw; got != `"{\"city\":\"Paris\"}"` {
		t.Fatalf("messages[0].tool_calls[0].function.arguments raw = %s, want JSON string form", got)
	}
}

func TestQoderExecutorBuildQoderRequestBody_NormalizesImageContentParts(t *testing.T) {
	e := NewQoderExecutor(&config.Config{})
	body := e.buildQoderRequestBody([]byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]}`), "qwen3.7-max", qoderModelContract{})
	data := mustMarshalJSON(t, body)
	want := "look\n\n[image] https://example.com/a.png"
	if got := gjson.GetBytes(data, "messages.0.contents.0.text").String(); got != want {
		t.Fatalf("messages[0].contents[0].text = %q, want %q", got, want)
	}
}

func TestQoderCreds_DecodesEscapedAccessToken(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": "abc%40def%2Bghi%25jkl",
			"uid":          "u1",
			"machine_id":   "m1",
		},
	}
	creds := qoderCreds(auth)
	if creds.accessToken != "abc@def+ghi%jkl" {
		t.Fatalf("accessToken = %q, want %q", creds.accessToken, "abc@def+ghi%jkl")
	}
}

func TestQoderCreds_UsesPATTokenMetadata(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"auth_method":           "pat",
			"personal_access_token": "qdr_pat_token",
			"uid":                   "u1",
			"machine_id":            "m1",
		},
	}
	creds := qoderCreds(auth)
	if creds.accessToken != "qdr_pat_token" {
		t.Fatalf("accessToken = %q, want %q", creds.accessToken, "qdr_pat_token")
	}
}

func mustMarshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return data
}

func decodeQoderBodyForTest(t *testing.T, encoded string) []byte {
	t.Helper()
	stdAlphabetIndex := make(map[rune]rune, len(qoder.CustomAlphabet)+1)
	for i, c := range qoder.CustomAlphabet {
		stdAlphabetIndex[c] = rune(qoder.StdAlphabet[i])
	}
	stdAlphabetIndex[qoder.CustomPad] = '='

	var stdBuilder strings.Builder
	for _, c := range encoded {
		mapped, ok := stdAlphabetIndex[c]
		if !ok {
			t.Fatalf("unexpected encoded char %q", c)
		}
		stdBuilder.WriteRune(mapped)
	}
	std := stdBuilder.String()
	n := len(std)
	a := n / 3
	rearranged := std[n-a:] + std[a:n-a] + std[:a]
	decoded, err := base64.StdEncoding.DecodeString(rearranged)
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	return decoded
}
