package common

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestMergeAdjacentGeminiContentsReordersTrailingText(t *testing.T) {
	contents := [][]byte{
		[]byte(`{"role":"user","parts":[{"functionResponse":{"name":"read","response":{"result":"ok"}}}]}`),
		[]byte(`{"role":"user","parts":[{"text":"<system-reminder>reminder</system-reminder>"}]}`),
	}
	merged := MergeAdjacentGeminiContents(contents)
	if len(merged) != 1 {
		t.Fatalf("expected 1 merged turn, got %d", len(merged))
	}
	parts := gjson.GetBytes(merged[0], "parts").Array()
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parts))
	}
	if got := parts[0].Get("text").String(); got != "<system-reminder>reminder</system-reminder>" {
		t.Fatalf("expected parts[0] to be text, got %q", got)
	}
	if got := parts[1].Get("functionResponse.name").String(); got != "read" {
		t.Fatalf("expected parts[1] to be functionResponse, got %q", got)
	}
}

func TestReorderGeminiUserParts(t *testing.T) {
	t.Run("returns unchanged when no functionResponse", func(t *testing.T) {
		parts := [][]byte{
			[]byte(`{"text":"hello"}`),
			[]byte(`{"inline_data":{"mime_type":"image/png","data":"abc"}}`),
		}
		reordered := ReorderGeminiUserParts(parts)
		if len(reordered) != 2 || gjson.GetBytes(reordered[0], "text").String() != "hello" {
			t.Fatalf("unexpected parts: %v", reordered)
		}
	})

	t.Run("returns unchanged when text already precedes functionResponse", func(t *testing.T) {
		parts := [][]byte{
			[]byte(`{"text":"context"}`),
			[]byte(`{"functionResponse":{"name":"read","response":{"result":"ok"}}}`),
		}
		reordered := ReorderGeminiUserParts(parts)
		if len(reordered) != 2 || gjson.GetBytes(reordered[0], "text").String() != "context" {
			t.Fatalf("unexpected parts: %v", reordered)
		}
	})

	t.Run("reorders trailing text before functionResponse", func(t *testing.T) {
		parts := [][]byte{
			[]byte(`{"functionResponse":{"name":"read","response":{"result":"ok"}}}`),
			[]byte(`{"text":"<system-reminder>reminder</system-reminder>"}`),
		}
		reordered := ReorderGeminiUserParts(parts)
		if len(reordered) != 2 {
			t.Fatalf("expected 2 parts, got %d", len(reordered))
		}
		if got := gjson.GetBytes(reordered[0], "text").String(); got != "<system-reminder>reminder</system-reminder>" {
			t.Fatalf("expected parts[0] to be text, got %q", got)
		}
		if got := gjson.GetBytes(reordered[1], "functionResponse.name").String(); got != "read" {
			t.Fatalf("expected parts[1] to be functionResponse, got %q", got)
		}
	})

	t.Run("preserves relative order with multiple functionResponses and texts", func(t *testing.T) {
		parts := [][]byte{
			[]byte(`{"text":"leading"}`),
			[]byte(`{"functionResponse":{"name":"tool1","response":{"result":"1"}}}`),
			[]byte(`{"functionResponse":{"name":"tool2","response":{"result":"2"}}}`),
			[]byte(`{"text":"trailing"}`),
		}
		reordered := ReorderGeminiUserParts(parts)
		if len(reordered) != 4 {
			t.Fatalf("expected 4 parts, got %d", len(reordered))
		}
		if got := gjson.GetBytes(reordered[0], "text").String(); got != "leading" {
			t.Fatalf("parts[0] text = %q, want leading", got)
		}
		if got := gjson.GetBytes(reordered[1], "text").String(); got != "trailing" {
			t.Fatalf("parts[1] text = %q, want trailing", got)
		}
		if got := gjson.GetBytes(reordered[2], "functionResponse.name").String(); got != "tool1" {
			t.Fatalf("parts[2] name = %q, want tool1", got)
		}
		if got := gjson.GetBytes(reordered[3], "functionResponse.name").String(); got != "tool2" {
			t.Fatalf("parts[3] name = %q, want tool2", got)
		}
	})
}

func TestContainsJSONRef(t *testing.T) {
	tests := []struct {
		name string
		json string
		want bool
	}{
		{
			name: "top-level $ref object",
			json: `{"$ref": "#/components/schemas/ErrorModel"}`,
			want: true,
		},
		{
			name: "nested $ref in object",
			json: `{"responses":{"400":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/ErrorModel"}}}}}}`,
			want: true,
		},
		{
			name: "nested $ref in array",
			json: `[{"schema":{"$ref":"#/components/schemas/ErrorModel"}}]`,
			want: true,
		},
		{
			name: "plain object without $ref",
			json: `{"temperature": 72, "city": "Seattle"}`,
			want: false,
		},
		{
			name: "plain array without $ref",
			json: `[1, 2, {"name": "test"}]`,
			want: false,
		},
		{
			name: "$ref with non-string value is not a schema ref",
			json: `{"$ref": 123}`,
			want: false,
		},
		{
			name: "primitive string containing $ref text is not a schema ref",
			json: `"this is just a string containing $ref"`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContainsJSONRef(gjson.Parse(tt.json))
			if got != tt.want {
				t.Errorf("ContainsJSONRef() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSetGeminiFunctionResponseResult(t *testing.T) {
	t.Run("preserves $ref as string under result", func(t *testing.T) {
		part := []byte(`{"functionResponse":{"name":"test"}}`)
		res := gjson.Parse(`{"schema":{"$ref":"#/components/schemas/ErrorModel"}}`)
		updated := SetGeminiFunctionResponseResult(part, "functionResponse.response.result", res)
		val := gjson.GetBytes(updated, "functionResponse.response.result")
		if val.Type != gjson.String {
			t.Fatalf("expected string type, got %s (raw: %s)", val.Type, val.Raw)
		}
	})

	t.Run("keeps non-$ref object as raw JSON under result", func(t *testing.T) {
		part := []byte(`{"functionResponse":{"name":"test"}}`)
		res := gjson.Parse(`{"ok":true,"code":200}`)
		updated := SetGeminiFunctionResponseResult(part, "functionResponse.response.result", res)
		val := gjson.GetBytes(updated, "functionResponse.response.result")
		if !val.IsObject() {
			t.Fatalf("expected object type, got %s (raw: %s)", val.Type, val.Raw)
		}
		if !val.Get("ok").Bool() {
			t.Fatalf("expected ok=true, got %v", val.Get("ok"))
		}
	})

	t.Run("places stringified $ref under .result when path ends with response", func(t *testing.T) {
		part := []byte(`{"functionResponse":{"name":"test"}}`)
		res := gjson.Parse(`{"schema":{"$ref":"#/components/schemas/ErrorModel"}}`)
		updated := SetGeminiFunctionResponseResult(part, "functionResponse.response", res)
		val := gjson.GetBytes(updated, "functionResponse.response.result")
		if val.Type != gjson.String {
			t.Fatalf("expected string type under response.result, got %s (raw: %s)", val.Type, val.Raw)
		}
	})
}
