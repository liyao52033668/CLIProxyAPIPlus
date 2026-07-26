package quota

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/cpa/dto/apicall"
)

func mergeHeaders(base map[string]string, overrides map[string]string) map[string]string {
	// Apply provider default headers first, then override with identity headers so account params win.
	if len(base) == 0 && len(overrides) == 0 {
		return nil
	}
	headers := make(map[string]string, len(base)+len(overrides))
	for key, value := range base {
		headers[key] = value
	}
	for key, value := range overrides {
		headers[key] = value
	}
	return headers
}

func targetHTTPError(response *apicall.Response) error {
	return fmt.Errorf("HTTP %d: %s", response.StatusCode, targetErrorMessage(response))
}

func targetErrorMessage(response *apicall.Response) string {
	// Prefer structured body parsing, then fall back to body_text, to surface real upstream errors.
	for _, data := range [][]byte{response.Body, []byte(strings.TrimSpace(response.BodyText))} {
		if message := targetErrorMessageFromBytes(data); message != "" {
			return message
		}
	}
	return strings.TrimSpace(response.BodyText)
}

func targetErrorMessageFromBytes(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		return strings.TrimSpace(text)
	}
	// Common sibling-project error shapes include message/error/detail, and nested error objects are supported.
	object := rawObject(data)
	if object == nil {
		return strings.TrimSpace(string(data))
	}
	if message := stringField(object, "message", "error_description", "detail"); message != "" {
		return message
	}
	if errorText := stringField(object, "error"); errorText != "" {
		return errorText
	}
	if nested := objectField(object, "error"); nested != nil {
		if message := stringField(nested, "message", "detail", "error_description"); message != "" {
			return message
		}
	}
	return strings.TrimSpace(string(data))
}
