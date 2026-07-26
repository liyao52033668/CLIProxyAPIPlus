package helps

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
)

func TestRecordAPIResponseMetadataStoresHeadersWhenRequestLogDisabled(t *testing.T) {
	ctx := logging.WithResponseHeadersHolder(context.Background())
	headers := http.Header{}
	headers.Add("X-Upstream-Request-Id", "upstream-req-1")

	RecordAPIResponseMetadata(ctx, &config.Config{}, http.StatusOK, headers)
	headers.Set("X-Upstream-Request-Id", "mutated")

	got := logging.GetResponseHeaders(ctx)
	if got.Get("X-Upstream-Request-Id") != "upstream-req-1" {
		t.Fatalf("response header = %q, want %q", got.Get("X-Upstream-Request-Id"), "upstream-req-1")
	}
}

func TestRecordAPIRequestDefersAndClonesBodyWhenRequestLogDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	body := []byte(`{"model":"original"}`)

	RecordAPIRequest(ctx, &config.Config{}, UpstreamRequestLog{
		URL:       "https://example.test/v1/responses",
		Method:    http.MethodPost,
		Headers:   http.Header{"Authorization": []string{"Bearer sk-test-secret"}},
		Body:      body,
		Provider:  "openai",
		AuthType:  "api_key",
		AuthValue: "sk-test-secret",
	})
	copy(body, []byte(`{"model":"mutated!"}`))

	value, exists := ginCtx.Get(logging.DeferredAPIRequestContextKey)
	if !exists {
		t.Fatal("deferred API request was not stored")
	}
	requests, ok := value.([]logging.DeferredAPIRequest)
	if !ok || len(requests) != 1 {
		t.Fatalf("deferred API requests = %#v, want one request", value)
	}
	built := requests[0]()
	if !bytes.Contains(built, []byte(`{"model":"original"}`)) {
		t.Fatalf("deferred request body = %q, want cloned original body", built)
	}
	if bytes.Contains(built, []byte("sk-test-secret")) {
		t.Fatal("deferred request leaked the full API key")
	}
}

func TestRecordAPIRequestSkipsDeferredCaptureInCommercialMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	RecordAPIRequest(ctx, &config.Config{CommercialMode: true}, UpstreamRequestLog{Body: []byte("payload")})

	if _, exists := ginCtx.Get(logging.DeferredAPIRequestContextKey); exists {
		t.Fatal("commercial mode stored a deferred API request")
	}
}

func TestAppendAPIResponseChunkExtendsAggregateIncrementally(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}

	aggregation := &apiResponseAggregation{data: make([]byte, 0, 64<<10)}
	backing := &aggregation.data[:cap(aggregation.data)][0]
	ginCtx.Set(apiResponseAggregationKey, aggregation)

	RecordAPIRequest(ctx, cfg, UpstreamRequestLog{Body: []byte("request")})
	RecordAPIResponseMetadata(ctx, cfg, http.StatusOK, http.Header{"Content-Type": []string{"text/event-stream"}})
	for range 256 {
		AppendAPIResponseChunk(ctx, cfg, []byte("data: chunk"))
	}

	value, exists := ginCtx.Get(apiResponseKey)
	if !exists {
		t.Fatal("API_RESPONSE was not stored")
	}
	captured, ok := value.([]byte)
	if !ok {
		t.Fatalf("API_RESPONSE = %T, want []byte", value)
	}
	if len(captured) == 0 {
		t.Fatal("API_RESPONSE was empty")
	}
	if &captured[0] != backing {
		t.Fatal("API_RESPONSE backing storage changed despite sufficient capacity")
	}
	if got := bytes.Count(captured, []byte("=== API RESPONSE 1 ===")); got != 1 {
		t.Fatalf("response intro count = %d, want 1", got)
	}
	if got := bytes.Count(captured, []byte("data: chunk")); got != 256 {
		t.Fatalf("response chunk count = %d, want 256", got)
	}
	attempts := getAttempts(ginCtx)
	if len(attempts) != 1 || attempts[0].responseSynced != attempts[0].response.Len() {
		t.Fatalf("response sync state = %#v, want latest response fully synchronized", attempts)
	}
}

func TestAppendAPIResponseChunkPreservesAttemptSeparators(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}

	RecordAPIRequest(ctx, cfg, UpstreamRequestLog{Body: []byte("first request")})
	AppendAPIResponseChunk(ctx, cfg, []byte("first response"))
	firstValue, firstExists := ginCtx.Get(apiResponseKey)
	firstResponse, firstOK := firstValue.([]byte)
	if !firstExists || !firstOK || !bytes.Contains(firstResponse, []byte("first response")) {
		t.Fatalf("first API_RESPONSE = %#v, want immediately readable response", firstValue)
	}

	RecordAPIRequest(ctx, cfg, UpstreamRequestLog{Body: []byte("second request")})
	AppendAPIResponseChunk(ctx, cfg, []byte("second response"))

	value, exists := ginCtx.Get(apiResponseKey)
	captured, ok := value.([]byte)
	if !exists || !ok {
		t.Fatalf("API_RESPONSE = %#v, want []byte", value)
	}
	if !bytes.Contains(captured, []byte("first response\n\n=== API RESPONSE 2 ===")) {
		t.Fatalf("API_RESPONSE attempt separator missing: %q", captured)
	}
	if got := bytes.Count(captured, []byte("=== API RESPONSE")); got != 2 {
		t.Fatalf("response attempt count = %d, want 2", got)
	}
}
