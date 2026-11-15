package executor

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	xaiEmptyStreamMessage      = "xai stream error: empty upstream stream"
	xaiIncompleteStreamMessage = "xai stream error: stream closed before response.completed or response.incomplete"
)

// xaiRequestScopedStreamError marks a failure caused by this specific request
// (empty or truncated upstream stream) so the auth conductor does not cool the
// credential down: retrying on another credential cannot help.
type xaiRequestScopedStreamError struct {
	statusErr
}

func (xaiRequestScopedStreamError) IsRequestScoped() bool { return true }

func newXAIEmptyStreamError() xaiRequestScopedStreamError {
	return xaiRequestScopedStreamError{statusErr: statusErr{
		code: http.StatusBadGateway,
		msg:  xaiEmptyStreamMessage,
	}}
}

func newXAIIncompleteStreamError() xaiRequestScopedStreamError {
	return xaiRequestScopedStreamError{statusErr: statusErr{
		code: http.StatusBadGateway,
		msg:  xaiIncompleteStreamMessage,
	}}
}

// xaiTerminalFailureErr classifies a terminal SSE event (an `error` event or a
// `response.failed` payload) into an upstream-shaped statusErr so the caller
// reports the real cause instead of a generic timeout.
func xaiTerminalFailureErr(eventName string, eventData []byte) (statusErr, bool) {
	doc, ok := xaiTerminalFailureDoc(eventName, eventData)
	if !ok || doc == nil {
		return statusErr{}, false
	}
	status := xaiTerminalFailureStatus(eventData, doc)
	return xaiStatusErr(status, doc), true
}

func xaiTerminalFailureDoc(eventName string, eventData []byte) ([]byte, bool) {
	trimmed := bytes.TrimSpace(eventData)
	if len(trimmed) == 0 {
		return nil, false
	}
	if !gjson.ValidBytes(trimmed) {
		if strings.EqualFold(strings.TrimSpace(eventName), "error") {
			doc, _ := sjson.SetBytes([]byte(`{"error":{}}`), "error.message", string(trimmed))
			return doc, true
		}
		return nil, false
	}

	eventType := strings.TrimSpace(gjson.GetBytes(trimmed, "type").String())
	if eventType == "" {
		normalizedEventName := strings.TrimSpace(eventName)
		if normalizedEventName == "error" || normalizedEventName == "response.failed" {
			eventType = normalizedEventName
		} else if errField := gjson.GetBytes(trimmed, "error"); errField.Exists() && errField.Type != gjson.Null && !gjson.GetBytes(trimmed, "output").Exists() {
			if errField.IsObject() || strings.TrimSpace(errField.String()) != "" {
				eventType = "error"
			}
		}
	}

	var doc []byte
	switch eventType {
	case "error":
		doc = xaiTerminalErrorDoc(trimmed, "error")
		if doc == nil {
			doc = xaiTerminalErrorDoc(trimmed, "body.error")
		}
		if doc == nil {
			doc = xaiTerminalTopLevelErrorDoc(trimmed)
		}
	case "response.failed":
		doc = xaiTerminalErrorDoc(trimmed, "response.error")
		if doc == nil {
			doc = xaiTerminalErrorDoc(trimmed, "error")
		}
	default:
		return nil, false
	}

	if doc == nil {
		doc = []byte(`{"error":{"message":"upstream stream failed without error details"}}`)
	}
	if seq := gjson.GetBytes(trimmed, "sequence_number"); seq.Exists() && seq.Type == gjson.Number {
		doc, _ = sjson.SetBytes(doc, "sequence_number", seq.Int())
	}
	return doc, true
}

func xaiTerminalErrorDoc(eventData []byte, path string) []byte {
	errorResult := gjson.GetBytes(eventData, path)
	if !errorResult.Exists() || errorResult.Type == gjson.Null {
		return nil
	}

	doc := []byte(`{"error":{}}`)
	hasField := false
	if errorResult.IsObject() {
		if len(errorResult.Map()) > 0 {
			doc, _ = sjson.SetRawBytes(doc, "error", []byte(errorResult.Raw))
			hasField = true
		}
	} else if message := strings.TrimSpace(errorResult.String()); message != "" {
		doc, _ = sjson.SetBytes(doc, "error.message", message)
		hasField = true
	}

	if strings.TrimSpace(gjson.GetBytes(doc, "error.code").String()) == "" {
		for _, codePath := range []string{"response.error.code", "code", "body.error.code"} {
			if code := strings.TrimSpace(gjson.GetBytes(eventData, codePath).String()); code != "" {
				doc, _ = sjson.SetBytes(doc, "error.code", code)
				hasField = true
				break
			}
		}
	}
	if strings.TrimSpace(gjson.GetBytes(doc, "error.type").String()) == "" {
		for _, typePath := range []string{"response.error.type", "error_type", "body.error.type"} {
			if errType := strings.TrimSpace(gjson.GetBytes(eventData, typePath).String()); errType != "" {
				doc, _ = sjson.SetBytes(doc, "error.type", errType)
				hasField = true
				break
			}
		}
	}
	if strings.TrimSpace(gjson.GetBytes(doc, "error.param").String()) == "" {
		for _, paramPath := range []string{"response.error.param", "param", "body.error.param"} {
			if param := strings.TrimSpace(gjson.GetBytes(eventData, paramPath).String()); param != "" {
				doc, _ = sjson.SetBytes(doc, "error.param", param)
				hasField = true
				break
			}
		}
	}
	if strings.TrimSpace(gjson.GetBytes(doc, "error.message").String()) == "" {
		for _, msgPath := range []string{"response.error.message", "message", "body.error.message"} {
			if message := strings.TrimSpace(gjson.GetBytes(eventData, msgPath).String()); message != "" {
				doc, _ = sjson.SetBytes(doc, "error.message", message)
				hasField = true
				break
			}
		}
	}
	if strings.TrimSpace(gjson.GetBytes(doc, "error.message").String()) == "" {
		if code := strings.TrimSpace(gjson.GetBytes(doc, "error.code").String()); code != "" {
			doc, _ = sjson.SetBytes(doc, "error.message", code)
			hasField = true
		} else if errType := strings.TrimSpace(gjson.GetBytes(doc, "error.type").String()); errType != "" {
			doc, _ = sjson.SetBytes(doc, "error.message", errType)
			hasField = true
		}
	}
	if !hasField {
		return nil
	}
	return doc
}

func xaiTerminalTopLevelErrorDoc(eventData []byte) []byte {
	message := strings.TrimSpace(gjson.GetBytes(eventData, "message").String())
	code := strings.TrimSpace(gjson.GetBytes(eventData, "code").String())
	errorType := strings.TrimSpace(gjson.GetBytes(eventData, "error_type").String())
	param := strings.TrimSpace(gjson.GetBytes(eventData, "param").String())
	if message == "" && code == "" && errorType == "" && param == "" {
		return nil
	}

	doc := []byte(`{"error":{}}`)
	if message != "" {
		doc, _ = sjson.SetBytes(doc, "error.message", message)
	}
	if code != "" {
		doc, _ = sjson.SetBytes(doc, "error.code", code)
	}
	if errorType != "" {
		doc, _ = sjson.SetBytes(doc, "error.type", errorType)
	}
	if param != "" {
		doc, _ = sjson.SetBytes(doc, "error.param", param)
	}
	if strings.TrimSpace(gjson.GetBytes(doc, "error.message").String()) == "" {
		if code != "" {
			doc, _ = sjson.SetBytes(doc, "error.message", code)
		} else if errorType != "" {
			doc, _ = sjson.SetBytes(doc, "error.message", errorType)
		}
	}
	return doc
}

// xaiTerminalFailureStatus maps an upstream terminal error payload to an HTTP
// status. Unknown shapes fall back to 502 so the client sees a gateway error
// rather than a misleading request timeout.
func xaiTerminalFailureStatus(eventData, doc []byte) int {
	for _, path := range []string{"status", "status_code", "error.status", "error.status_code", "response.error.status", "response.error.status_code", "body.status", "body.error.status"} {
		res := gjson.GetBytes(eventData, path)
		switch res.Type {
		case gjson.Number:
			if status := int(res.Int()); status >= 400 && status <= 599 {
				return status
			}
		case gjson.String:
			if status, errAtoi := strconv.Atoi(strings.TrimSpace(res.String())); errAtoi == nil && status >= 400 && status <= 599 {
				return status
			}
		}
	}

	errorType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(doc, "error.type").String()))
	errorCode := strings.ToLower(strings.TrimSpace(gjson.GetBytes(doc, "error.code").String()))
	errorMsg := strings.ToLower(strings.TrimSpace(gjson.GetBytes(doc, "error.message").String()))
	if status, errAtoi := strconv.Atoi(errorCode); errAtoi == nil && status >= 400 && status <= 599 {
		return status
	}

	switch {
	case errorType == "authentication_error",
		errorCode == "invalid_api_key",
		errorCode == "unauthorized",
		strings.HasPrefix(errorCode, "unauthenticated"),
		strings.Contains(errorCode, "bad-credentials"),
		strings.Contains(errorMsg, "access token could not be validated"):
		return http.StatusUnauthorized
	case errorType == "permission_error",
		errorCode == "forbidden",
		errorCode == "permission_denied",
		errorCode == "permission-denied",
		strings.HasPrefix(errorCode, "permission_denied:"),
		strings.HasPrefix(errorCode, "permission-denied:"):
		return http.StatusForbidden
	case errorType == "not_found_error",
		errorCode == "not_found",
		errorCode == "model_not_found":
		return http.StatusNotFound
	case errorType == "rate_limit_error",
		errorCode == "rate_limit_exceeded",
		errorCode == "rate_limit",
		strings.Contains(errorCode, "free-usage-exhausted"),
		strings.Contains(errorCode, "resource_exhausted"),
		strings.Contains(errorCode, "resource-exhausted"),
		strings.Contains(errorMsg, "free-usage-exhausted"),
		strings.Contains(errorMsg, "included free usage"):
		return http.StatusTooManyRequests
	case errorCode == "cyber_policy",
		errorCode == "content_policy_violation",
		errorType == "invalid_request_error",
		errorType == "invalid_request",
		errorType == "bad_request_error",
		errorType == "image_generation_user_error",
		errorCode == "invalid_value",
		errorCode == "invalid_request_error",
		errorCode == "invalid_argument",
		errorCode == "invalid-argument",
		errorCode == "context_length_exceeded",
		errorCode == "context_too_large":
		return http.StatusBadRequest
	case errorType == "service_unavailable_error",
		errorCode == "server_is_overloaded",
		errorCode == "service_unavailable":
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadGateway
	}
}
