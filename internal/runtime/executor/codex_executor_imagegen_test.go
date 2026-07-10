package executor

import (
	"testing"

	"net/http"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestEnsureImageGenerationTool_ResponsesLiteMetadataDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"},"input":[{"role":"user","content":"hello"}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.6-sol", nil, nil)

	if string(result) != string(body) {
		t.Fatalf("expected responses-lite body to be unchanged, got %s", string(result))
	}
	if gjson.GetBytes(result, "tools").Exists() {
		t.Fatalf("expected no injected tools for responses-lite request, got %s", gjson.GetBytes(result, "tools").Raw)
	}
}

func TestEnsureImageGenerationTool_ResponsesLiteBooleanMetadataDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":true},"input":"hello"}`)
	result := ensureImageGenerationTool(body, "gpt-5.6-sol", nil, nil)

	if string(result) != string(body) {
		t.Fatalf("expected responses-lite body to be unchanged, got %s", string(result))
	}
}

func TestEnsureImageGenerationTool_ResponsesLiteHeaderDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","input":"hello"}`)
	headers := make(http.Header)
	headers.Set("X-OpenAI-Internal-Codex-Responses-Lite", "true")
	result := ensureImageGenerationTool(body, "gpt-5.6-sol", nil, headers)

	if string(result) != string(body) {
		t.Fatalf("expected responses-lite body to be unchanged, got %s", string(result))
	}
}

func TestEnsureImageGenerationTool_ResponsesLiteFalseMetadataStillInjectsTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"false"},"input":"hello"}`)
	result := ensureImageGenerationTool(body, "gpt-5.6-sol", nil, nil)

	if got := gjson.GetBytes(result, "tools.0.type").String(); got != "image_generation" {
		t.Fatalf("tools.0.type = %q, want image_generation; body=%s", got, result)
	}
}

func TestEnsureImageGenerationTool_NoTools(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"draw a cat"}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	tools := gjson.GetBytes(result, "tools")
	if !tools.IsArray() {
		t.Fatalf("expected tools array, got %v", tools.Type)
	}
	arr := tools.Array()
	if len(arr) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(arr))
	}
	if arr[0].Get("type").String() != "image_generation" {
		t.Fatalf("expected type=image_generation, got %s", arr[0].Get("type").String())
	}
	if arr[0].Get("output_format").String() != "png" {
		t.Fatalf("expected output_format=png, got %s", arr[0].Get("output_format").String())
	}
}

func TestEnsureImageGenerationTool_ExistingToolsWithoutImageGen(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"function","name":"get_weather","parameters":{}}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	tools := gjson.GetBytes(result, "tools")
	arr := tools.Array()
	if len(arr) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(arr))
	}
	if arr[0].Get("type").String() != "function" {
		t.Fatalf("expected first tool type=function, got %s", arr[0].Get("type").String())
	}
	if arr[1].Get("type").String() != "image_generation" {
		t.Fatalf("expected second tool type=image_generation, got %s", arr[1].Get("type").String())
	}
}

func TestEnsureImageGenerationTool_AlreadyPresent(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation","output_format":"webp"},{"type":"function","name":"f1"}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	tools := gjson.GetBytes(result, "tools")
	arr := tools.Array()
	if len(arr) != 2 {
		t.Fatalf("expected 2 tools (no duplicate), got %d", len(arr))
	}
	if arr[0].Get("output_format").String() != "webp" {
		t.Fatalf("expected original output_format=webp preserved, got %s", arr[0].Get("output_format").String())
	}
}

func TestEnsureImageGenerationTool_EmptyToolsArray(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	tools := gjson.GetBytes(result, "tools")
	arr := tools.Array()
	if len(arr) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(arr))
	}
	if arr[0].Get("type").String() != "image_generation" {
		t.Fatalf("expected type=image_generation, got %s", arr[0].Get("type").String())
	}
}

func TestEnsureImageGenerationTool_WebSearchAndImageGen(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"web_search"}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	tools := gjson.GetBytes(result, "tools")
	arr := tools.Array()
	if len(arr) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(arr))
	}
	if arr[0].Get("type").String() != "web_search" {
		t.Fatalf("expected first tool type=web_search, got %s", arr[0].Get("type").String())
	}
	if arr[1].Get("type").String() != "image_generation" {
		t.Fatalf("expected second tool type=image_generation, got %s", arr[1].Get("type").String())
	}
}

func TestEnsureImageGenerationTool_GPT53CodexSparkDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.3-codex-spark","input":"draw a cat"}`)
	result := ensureImageGenerationTool(body, "gpt-5.3-codex-spark", nil, nil)

	if string(result) != string(body) {
		t.Fatalf("expected body to be unchanged, got %s", string(result))
	}
	if gjson.GetBytes(result, "tools").Exists() {
		t.Fatalf("expected no tools for gpt-5.3-codex-spark, got %s", gjson.GetBytes(result, "tools").Raw)
	}
}

func TestEnsureImageGenerationTool_FreeCodexAuthDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"draw a cat"}`)
	freeAuth := &cliproxyauth.Auth{
		Provider:   "codex",
		Attributes: map[string]string{"plan_type": "free"},
	}
	result := ensureImageGenerationTool(body, "gpt-5.4", freeAuth, nil)

	if string(result) != string(body) {
		t.Fatalf("expected body to be unchanged, got %s", string(result))
	}
	if gjson.GetBytes(result, "tools").Exists() {
		t.Fatalf("expected no tools for free codex auth, got %s", gjson.GetBytes(result, "tools").Raw)
	}
}
