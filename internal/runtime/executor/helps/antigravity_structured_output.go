package helps

import (
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// antigravityClaude55Model matches Claude 5.5 model IDs including the effort
// suffix variants. Any effort level must be recognized, otherwise the native
// response schema survives and Antigravity upstreams force VALIDATED tool mode.
var antigravityClaude55Model = regexp.MustCompile(`^claude-(opus|sonnet|haiku)-5-5(?:-(low|medium|high|max|reasoning))?(?:-[0-9]{8})?(?:\[[^\[\]]+\])?$`)

// PrepareAntigravityClaudeStructuredOutput replaces native output schemas with
// best-effort prompt instructions for Claude 5.5, which rejects forced tool use.
// Call after sensitive-word obfuscation and before schema cleaning or payload
// rules. The boolean tells the request builder not to restore VALIDATED tool mode.
func PrepareAntigravityClaudeStructuredOutput(model string, payload []byte) ([]byte, bool) {
	if len(payload) == 0 {
		return payload, false
	}
	updated, changed := prepareAntigravityClaudeStructuredOutputBytes(model, payload)
	if !changed {
		return payload, false
	}
	return updated, true
}

// prepareAntigravityClaudeStructuredOutputBytes replaces native output schemas
// with best-effort prompt instructions in place for Antigravity Claude 5.5.
func prepareAntigravityClaudeStructuredOutputBytes(model string, payload []byte) ([]byte, bool) {
	model = strings.ToLower(thinking.ParseSuffix(strings.TrimSpace(model)).ModelName)
	if !antigravityClaude55Model.MatchString(model) {
		return payload, false
	}

	containers := []string{"request.generationConfig", "request.generation_config"}
	schemaKeys := []string{"responseSchema", "responseJsonSchema", "response_schema", "response_json_schema"}
	schemaRaw := ""
	for _, container := range containers {
		for _, key := range schemaKeys {
			candidate := gjson.GetBytes(payload, container+"."+key)
			if candidate.IsObject() {
				schemaRaw = candidate.Raw
				break
			}
		}
		if schemaRaw != "" {
			break
		}
	}
	if schemaRaw == "" {
		return payload, false
	}

	// Wrap the untouched schema as a format, not as format settings themselves.
	format := gjson.Parse(`{"type":"json_schema","schema":` + schemaRaw + `}`)
	instruction := translatorcommon.BuildClaudeStructuredOutputInstruction(format)
	systemPath := "request.systemInstruction"
	if !gjson.GetBytes(payload, systemPath).Exists() && gjson.GetBytes(payload, "request.system_instruction").Exists() {
		systemPath = "request.system_instruction"
	}
	updated, errSet := sjson.SetBytes(payload, systemPath+".parts.-1.text", instruction)
	if errSet != nil {
		return payload, false
	}

	// Remove only native output leaves; other config fields and aliases stay put.
	for _, container := range containers {
		for _, key := range append(schemaKeys, "responseMimeType", "response_mime_type") {
			updated = DeleteJSONField(updated, container+"."+key)
		}
	}
	for _, container := range []string{"request.toolConfig", "request.tool_config"} {
		for _, key := range []string{"functionCallingConfig", "function_calling_config"} {
			path := container + "." + key
			switch strings.ToUpper(gjson.GetBytes(updated, path+".mode").String()) {
			case "ANY", "VALIDATED":
				updated, _ = sjson.SetBytes(updated, path+".mode", "AUTO")
				updated = DeleteJSONField(updated, path+".allowedFunctionNames")
				updated = DeleteJSONField(updated, path+".allowed_function_names")
			}
		}
	}
	return updated, true
}
