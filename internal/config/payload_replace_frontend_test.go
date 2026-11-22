package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// frontendReplaceYAML mirrors what the management frontend writes for
// payload.replace, covering all three target modes.
const frontendReplaceYAML = `
payload:
  replace:
    - models:
        - name: "gpt-*"
          protocol: "codex"
      target:
        role: "user"
        positions: [1, 3]
      replacements:
        - "old phrase": "new phrase"
    - models:
        - name: "claude-*"
      target:
        role: "system"
        all: true
      replacements:
        - pattern: "TODO\\(\\d+\\)"
          replacement: "item-$1"
    - models:
        - name: "gemini-*"
          protocol: "gemini"
      target:
        paths:
          - "contents.0.parts.0.text"
      replacements:
        - "a": "b"
`

// TestFrontendReplaceRoundTrip verifies the YAML the management UI emits parses
// into valid rules for every target mode.
func TestFrontendReplaceRoundTrip(t *testing.T) {
	var cfg Config
	if errUnmarshal := yaml.Unmarshal([]byte(frontendReplaceYAML), &cfg); errUnmarshal != nil {
		t.Fatalf("unmarshal: %v", errUnmarshal)
	}
	if len(cfg.Payload.Replace) != 3 {
		t.Fatalf("replace rules = %d, want 3", len(cfg.Payload.Replace))
	}
	for i, rule := range cfg.Payload.Replace {
		if errValidate := rule.Validate(); errValidate != nil {
			t.Fatalf("rule %d invalid: %v", i, errValidate)
		}
	}

	first := cfg.Payload.Replace[0]
	if first.Target.Role != "user" || len(first.Target.Positions) != 2 || first.Target.Positions[0] != 1 {
		t.Fatalf("rule 0 target = %+v", first.Target)
	}

	second := cfg.Payload.Replace[1]
	if !second.Target.All || second.Target.Role != "system" {
		t.Fatalf("rule 1 target = %+v", second.Target)
	}
	src, dst, isRegex, errResolve := second.Replacements[0].Resolve()
	if errResolve != nil || !isRegex || src != `TODO\(\d+\)` || dst != "item-$1" {
		t.Fatalf("rule 1 replacement = %q %q regex=%v err=%v", src, dst, isRegex, errResolve)
	}

	third := cfg.Payload.Replace[2]
	if len(third.Target.Paths) != 1 || third.Target.Role != "" {
		t.Fatalf("rule 2 target = %+v", third.Target)
	}

	if sanitized := SanitizePayloadReplaceRules(cfg.Payload.Replace); len(sanitized) != 3 {
		t.Fatalf("sanitized = %d, want 3", len(sanitized))
	}
}
