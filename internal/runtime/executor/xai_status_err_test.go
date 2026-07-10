package executor

import (
	"net/http"
	"testing"
	"time"
)

func TestXAIStatusErr_FreeUsageExhaustedSets24hRetryAfter(t *testing.T) {
	err := xaiStatusErr(http.StatusTooManyRequests, []byte(`{"code":"subscription:free-usage-exhausted","error":"You have exhausted the included free usage"}`))

	if err.RetryAfter() == nil {
		t.Fatal("expected RetryAfter for free-usage-exhausted")
	}
	if *err.RetryAfter() != 24*time.Hour {
		t.Fatalf("RetryAfter = %v, want 24h", *err.RetryAfter())
	}
}

func TestXAIStatusErr_Generic429HasNoRetryAfter(t *testing.T) {
	err := xaiStatusErr(http.StatusTooManyRequests, []byte(`{"error":"rate limit"}`))

	if err.RetryAfter() != nil {
		t.Fatalf("expected nil RetryAfter for generic 429, got %v", *err.RetryAfter())
	}
}

func TestXAIStatusErr_Non429HasNoRetryAfter(t *testing.T) {
	err := xaiStatusErr(http.StatusBadRequest, []byte(`{"code":"subscription:free-usage-exhausted"}`))

	if err.RetryAfter() != nil {
		t.Fatalf("expected nil RetryAfter for 400, got %v", *err.RetryAfter())
	}
}
