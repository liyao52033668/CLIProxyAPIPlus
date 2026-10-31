package executor

import (
	"net/http"
	"testing"
	"time"
)

func TestIsCodexTurnRateLimitError(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "dedicated code", body: `{"error":{"code":"turn_rate_limit_exceeded","message":"slow down"}}`, want: true},
		{name: "nested body code", body: `{"body":{"error":{"code":"turn_rate_limit_exceeded"}}}`, want: true},
		{name: "rate limit typed at capacity", body: `{"error":{"type":"rate_limit_error","message":"model is at capacity"}}`, want: true},
		{name: "other code wins over message", body: `{"error":{"code":"usage_limit_reached","type":"rate_limit_error","message":"at capacity"}}`, want: false},
		{name: "generic rate limit without capacity wording", body: `{"error":{"type":"rate_limit_error","message":"too many requests"}}`, want: false},
		{name: "unrelated", body: `{"error":{"type":"invalid_request_error"}}`, want: false},
		{name: "empty", body: ``, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCodexTurnRateLimitError([]byte(tc.body)); got != tc.want {
				t.Fatalf("isCodexTurnRateLimitError(%s) = %t, want %t", tc.body, got, tc.want)
			}
		})
	}
}

func TestIsCodexTransientCapacityError(t *testing.T) {
	if !isCodexTransientCapacityError([]byte(`{"error":{"code":"turn_rate_limit_exceeded"}}`)) {
		t.Fatal("turn_rate_limit_exceeded must be transient capacity")
	}
	if !isCodexTransientCapacityError([]byte(`{"error":{"message":"model is at capacity"}}`)) {
		t.Fatal("legacy capacity wording must be transient capacity")
	}
	if isCodexTransientCapacityError([]byte(`{"error":{"code":"usage_limit_reached"}}`)) {
		t.Fatal("quota exhaustion must not be transient capacity")
	}
	if isCodexTransientCapacityError(nil) {
		t.Fatal("empty body must not be transient capacity")
	}
}

func TestNewCodexStatusErrTransientRateLimit(t *testing.T) {
	err := newCodexStatusErrWithCooling(http.StatusOK, []byte(`{"error":{"code":"turn_rate_limit_exceeded","message":"per-turn limit"}}`), true)
	if err.code != http.StatusTooManyRequests {
		t.Fatalf("code = %d, want 429", err.code)
	}
	if !err.IsTransientRateLimit() {
		t.Fatal("turn_rate_limit must be marked transient")
	}
	if err.IsCredentialScoped() {
		t.Fatal("transient capacity must not be credential-scoped")
	}
	if err.retryAfter != nil {
		t.Fatalf("retryAfter = %v, want nil without a hint", *err.retryAfter)
	}

	usage := newCodexStatusErrWithCooling(http.StatusOK, []byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":60}}`), false)
	if usage.IsTransientRateLimit() {
		t.Fatal("quota exhaustion must not be marked transient")
	}
	if !usage.IsCredentialScoped() {
		t.Fatal("quota exhaustion must be credential-scoped when model cooling is off")
	}

	headers := http.Header{"Retry-After": []string{"7"}}
	withHeader := newCodexStatusErrWithHeadersAndCooling(http.StatusTooManyRequests, headers, []byte(`{"error":{"code":"turn_rate_limit_exceeded"}}`), true)
	if withHeader.retryAfter == nil || *withHeader.retryAfter != 7*time.Second {
		t.Fatalf("retryAfter = %v, want 7s from the Retry-After header", withHeader.retryAfter)
	}

	quotaWins := newCodexStatusErrWithHeadersAndCooling(http.StatusTooManyRequests, headers, []byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":120}}`), true)
	if quotaWins.retryAfter == nil || *quotaWins.retryAfter != 120*time.Second {
		t.Fatalf("retryAfter = %v, want the 120s quota reset to win over the header", quotaWins.retryAfter)
	}
}
