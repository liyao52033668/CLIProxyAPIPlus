package helps

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// payloadRuleMatchesModel reports whether any of the rule's model entries matches
// the upstream model. An empty model list matches everything.
func payloadRuleMatchesModel(rules []config.PayloadModelRule, protocol, fromProtocol string, headers map[string][]string, payload []byte, root string, models []string) bool {
	if len(rules) == 0 {
		return true
	}
	return payloadModelRulesMatch(rules, protocol, fromProtocol, toHTTPHeader(headers), payload, root, models)
}

func toHTTPHeader(headers map[string][]string) map[string][]string {
	return headers
}

// replacePayloadText edits existing strings selected by paths or by
// protocol-defined role text. It returns the updated payload and whether the
// rule produced at least one change.
func applyPayloadReplaceRule(payload []byte, protocol, root string, rule *config.PayloadReplaceRule, ruleIndex int) ([]byte, bool) {
	pathTarget := len(rule.Target.Paths) > 0
	var arrayPath, systemPath string
	var gemini, responses bool
	if !pathTarget {
		switch rule.Target.Role {
		case "system", "developer", "user":
		default:
			return payload, false
		}
		protocol = strings.ToLower(strings.TrimSpace(protocol))
		switch protocol {
		case "openai":
			arrayPath = buildPayloadPath(root, "messages")
		case "claude":
			arrayPath = buildPayloadPath(root, "messages")
			systemPath = buildPayloadPath(root, "system")
		case "codex", "responses", "response", "openai-response", "openai-responses", "meta":
			arrayPath = buildPayloadPath(root, "input")
			systemPath = buildPayloadPath(root, "instructions")
			responses = true
		case "gemini", "antigravity":
			arrayPath = buildPayloadPath(root, "contents")
			systemPath = buildPayloadPath(root, "systemInstruction")
			if !gjson.GetBytes(payload, systemPath).Exists() {
				systemPath = buildPayloadPath(root, "system_instruction")
			}
			gemini = true
		default:
			return payload, false
		}
	}

	var selected map[int]bool
	lastPosition := 0
	if !pathTarget && !rule.Target.All {
		selected = make(map[int]bool, len(rule.Target.Positions))
		for _, position := range rule.Target.Positions {
			selected[position] = true
			if position > lastPosition {
				lastPosition = position
			}
		}
	}
	if len(rule.Replacements) == 0 {
		return payload, false
	}

	type compiledReplacement struct {
		from, to string
		re       *regexp.Regexp
	}
	steps := make([]compiledReplacement, 0, len(rule.Replacements))
	for i := range rule.Replacements {
		source, target, isRegex, errResolve := rule.Replacements[i].Resolve()
		if errResolve != nil {
			continue
		}
		step := compiledReplacement{from: source, to: target}
		if isRegex {
			re, errCompile := regexp.Compile(source)
			if source == "" || errCompile != nil {
				log.Warnf("payload.replace rule %d replacement %d skipped: invalid regex", ruleIndex+1, i+1)
				continue
			}
			step.re = re
		}
		steps = append(steps, step)
	}
	if len(steps) == 0 {
		return payload, false
	}

	replaceText := func(text string) string {
		updated := text
		for _, step := range steps {
			if step.re != nil {
				updated = step.re.ReplaceAllString(updated, step.to)
			} else {
				updated = strings.ReplaceAll(updated, step.from, step.to)
			}
		}
		return updated
	}

	type textChange struct{ path, value string }
	var changes []textChange

	collectChange := func(text, basePath string, messageIndex, blockIndex int) {
		updated := replaceText(text)
		if updated == text {
			return
		}
		// Build paths only once a replacement actually changed the text.
		path := basePath
		if messageIndex >= 0 {
			if gemini {
				path = basePath + "." + strconv.Itoa(messageIndex) + ".parts." + strconv.Itoa(blockIndex) + ".text"
			} else if blockIndex >= 0 {
				path = basePath + "." + strconv.Itoa(messageIndex) + ".content." + strconv.Itoa(blockIndex) + ".text"
			} else {
				path = basePath + "." + strconv.Itoa(messageIndex) + ".content"
			}
		} else if blockIndex >= 0 {
			if gemini {
				path = basePath + ".parts." + strconv.Itoa(blockIndex) + ".text"
			} else {
				path = basePath + "." + strconv.Itoa(blockIndex) + ".text"
			}
		}
		changes = append(changes, textChange{path: path, value: updated})
	}

	collectContent := func(content gjson.Result, basePath string, messageIndex int) {
		if content.Type == gjson.String {
			collectChange(content.String(), basePath, messageIndex, -1)
			return
		}
		if !content.IsArray() {
			return
		}
		for blockIndex, block := range content.Array() {
			if !block.IsObject() {
				continue
			}
			text := block.Get("text")
			if text.Type != gjson.String || block.Get("thought").Bool() {
				continue
			}
			typ := block.Get("type").String()
			if typ == "text" || (responses && typ == "input_text") {
				collectChange(text.String(), basePath, messageIndex, blockIndex)
			}
		}
	}

	collectParts := func(parts gjson.Result, basePath string, messageIndex int) {
		if !parts.IsArray() {
			return
		}
		for partIndex, part := range parts.Array() {
			if !part.IsObject() {
				continue
			}
			text := part.Get("text")
			if text.Type != gjson.String || part.Get("thought").Bool() ||
				part.Get("functionCall").Exists() || part.Get("functionResponse").Exists() {
				continue
			}
			collectChange(text.String(), basePath, messageIndex, partIndex)
		}
	}

	if pathTarget {
		for _, path := range rule.Target.Paths {
			fullPath := buildPayloadPath(root, path)
			visitPayloadReplacePath(payload, fullPath, func(textPath, text string) {
				if updated := replaceText(text); updated != text {
					changes = append(changes, textChange{path: textPath, value: updated})
				}
			})
		}
	} else {
		position := 0
		// A top-level system field precedes explicit system messages and counts as one message.
		if rule.Target.Role == "system" && systemPath != "" {
			if system := gjson.GetBytes(payload, systemPath); system.Exists() {
				position++
				if rule.Target.All || selected[position] {
					if gemini {
						collectParts(system.Get("parts"), systemPath, -1)
					} else {
						collectContent(system, systemPath, -1)
					}
				}
			}
		}
		array := gjson.GetBytes(payload, arrayPath)
		if responses && array.Type == gjson.String {
			if rule.Target.Role == "user" && (rule.Target.All || selected[1]) {
				collectChange(array.String(), arrayPath, -1, -1)
			}
		} else if array.IsArray() && (rule.Target.All || position < lastPosition) {
			// The early exit when position >= lastPosition relies on position monotonicity:
			// positions are incremented sequentially as matching-role messages are encountered.
			// SanitizePayloadReplaceRules validates that positions are positive integers, so
			// the loop can safely exit once it has processed all target positions up to lastPosition.
			for index, message := range array.Array() {
				if !message.IsObject() || message.Get("role").String() != rule.Target.Role {
					continue
				}
				if responses {
					if typ := message.Get("type"); typ.Exists() && (typ.Type != gjson.String || typ.String() != "message") {
						continue
					}
				}
				position++
				if !rule.Target.All && !selected[position] {
					if position > lastPosition {
						break
					}
					continue
				}
				if gemini {
					collectParts(message.Get("parts"), arrayPath, index)
				} else {
					collectContent(message.Get("content"), arrayPath, index)
				}
			}
		}
	}

	updated := payload
	mutated := false
	for _, change := range changes {
		result, errSet := sjson.SetBytes(updated, change.path, change.value)
		if errSet != nil {
			continue
		}
		updated = result
		mutated = true
	}
	return updated, mutated
}

// visitPayloadReplacePath resolves a JSON path against the payload and reports
// every existing string node it selects, together with the concrete write path.
func visitPayloadReplacePath(payload []byte, path string, visit func(textPath, text string)) {
	if path == "" {
		return
	}
	selected := gjson.GetBytes(payload, path)
	if !selected.Exists() {
		return
	}
	if selected.Type == gjson.String {
		visit(path, selected.String())
		return
	}
	// Object and array selections expose every string leaf beneath them.
	selected.ForEach(func(key, value gjson.Result) bool {
		childPath := path + "." + escapeGJSONKey(key.String())
		if value.Type == gjson.String {
			visit(childPath, value.String())
			return true
		}
		visitPayloadReplacePath(payload, childPath, visit)
		return true
	})
}

// escapeGJSONKey escapes a key for gjson/sjson dot-notation paths.
func escapeGJSONKey(key string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `.`, `\.`, `*`, `\*`, `?`, `\?`)
	return replacer.Replace(key)
}
