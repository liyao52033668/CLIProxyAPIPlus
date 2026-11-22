package helps

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// DeleteJSONField removes a top-level or nested JSON field from a payload.
func DeleteJSONField(body []byte, key string) []byte {
	if key == "" || len(body) == 0 {
		return body
	}
	updated, err := sjson.DeleteBytes(body, key)
	if err != nil {
		return body
	}
	return updated
}

// ParseRetryDelay extracts the retry delay from a Google API 429 error response.
func ParseRetryDelay(errorBody []byte) (*time.Duration, error) {
	details := gjson.GetBytes(errorBody, "error.details")
	if details.Exists() && details.IsArray() {
		for _, detail := range details.Array() {
			if detail.Get("@type").String() != "type.googleapis.com/google.rpc.RetryInfo" {
				continue
			}
			retryDelay := detail.Get("retryDelay").String()
			if retryDelay == "" {
				continue
			}
			duration, err := time.ParseDuration(retryDelay)
			if err != nil {
				return nil, fmt.Errorf("failed to parse duration")
			}
			return &duration, nil
		}

		for _, detail := range details.Array() {
			if detail.Get("@type").String() != "type.googleapis.com/google.rpc.ErrorInfo" {
				continue
			}
			quotaResetDelay := detail.Get("metadata.quotaResetDelay").String()
			if quotaResetDelay == "" {
				continue
			}
			duration, err := time.ParseDuration(quotaResetDelay)
			if err == nil {
				return &duration, nil
			}
		}
	}

	message := gjson.GetBytes(errorBody, "error.message").String()
	if message != "" {
		re := regexp.MustCompile(`after\s+(\d+)s\.?`)
		if matches := re.FindStringSubmatch(message); len(matches) > 1 {
			seconds, err := strconv.Atoi(matches[1])
			if err == nil {
				duration := time.Duration(seconds) * time.Second
				return &duration, nil
			}
		}
		reHuman := regexp.MustCompile(`after\s+((?:\d+h)?(?:\d+m)?(?:\d+s)?)\.?`)
		if matches := reHuman.FindStringSubmatch(strings.ToLower(message)); len(matches) > 1 {
			duration, err := time.ParseDuration(matches[1])
			if err == nil && duration > 0 {
				return &duration, nil
			}
		}
	}

	return nil, fmt.Errorf("no RetryInfo found")
}

// ParseRetryDelayWithHeaders extracts the retry delay from a Google API 429
// error response body (preserving explicit zero-delay hints used by
// Antigravity), falling back to an HTTP Retry-After header (preserving explicit
// 0-second hints) when the body carries no retry delay hint.
func ParseRetryDelayWithHeaders(headers http.Header, errorBody []byte, now time.Time) (*time.Duration, error) {
	if delay, errParse := ParseRetryDelay(errorBody); errParse == nil && delay != nil {
		return delay, nil
	}
	if delay := parseRetryAfterHeaderDuration(headers, now, true); delay != nil {
		return delay, nil
	}
	return nil, fmt.Errorf("no RetryInfo found")
}

// parseRetryAfterHeaderDuration reads the Retry-After header as either a
// positive number of seconds or an HTTP date. When allowZeroSeconds is false a
// non-positive delay is rejected, matching Gemini's positive-only semantics.
func parseRetryAfterHeaderDuration(headers http.Header, now time.Time, allowZeroSeconds bool) *time.Duration {
	if headers == nil {
		return nil
	}
	raw := strings.TrimSpace(headers.Get("Retry-After"))
	if raw == "" {
		return nil
	}
	if seconds, errParse := strconv.ParseInt(raw, 10, 64); errParse == nil {
		if seconds < 0 || (seconds == 0 && !allowZeroSeconds) {
			return nil
		}
		d := time.Duration(seconds) * time.Second
		return &d
	}
	if parsed, errHTTP := http.ParseTime(raw); errHTTP == nil {
		if !parsed.After(now) {
			return nil
		}
		d := parsed.Sub(now)
		return &d
	}
	return nil
}
