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
