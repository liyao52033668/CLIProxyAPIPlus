package main

import (
	"strings"
	"testing"
)

// fixtureSources is a miniature but representative upstream layout: a literal
// model-id module, an entitlement object reached through a member reference, a
// model-config object table, the catalog with picker order and context windows,
// and the model -> root agent maps.
func fixtureSources() map[string]string {
	return map[string]string{
		"freebuff-model-ids.ts": `
export const FREEBUFF_ALPHA_MODEL_ID = 'vendor/alpha'
export const FREEBUFF_PAUSED_MODEL_ID = 'vendor/paused'
`,
		"freebuff-model-entitlements.ts": `
export const FREEBUFF_BETA_ENTITLEMENT = {
  modelId: 'vendor/beta',
} as const

export const FREEBUFF_BETA_MODEL_ID = FREEBUFF_BETA_ENTITLEMENT.modelId
`,
		"model-config.ts": `
export const gammaModels = {
  gammaV1: 'vendor/gamma',
} as const
`,
		"freebuff-models.ts": `
import { FREEBUFF_ALPHA_MODEL_ID } from './freebuff-model-ids'
import { gammaModels } from './model-config'

export const FREEBUFF_GAMMA_MODEL_ID = gammaModels.gammaV1

const ALPHA_REASONING_EFFORTS = ['low', 'high'] as const

const ALPHA_MODEL = {
  id: FREEBUFF_ALPHA_MODEL_ID,
  displayName: 'Alpha',
  efforts: ALPHA_REASONING_EFFORTS,
}

const BETA_MODEL = {
  id: FREEBUFF_BETA_MODEL_ID,
  displayName: 'Beta',
}

const GAMMA_MODEL = {
  id: FREEBUFF_GAMMA_MODEL_ID,
  displayName: 'Gamma',
}

const PAUSED_MODEL = {
  id: FREEBUFF_PAUSED_MODEL_ID,
  displayName: 'Paused',
}

export const SUPPORTED_FREEBUFF_MODELS = [
  PAUSED_MODEL,
  ALPHA_MODEL,
  BETA_MODEL,
  GAMMA_MODEL,
] as const

export const FREEBUFF_MODELS = [ALPHA_MODEL, GAMMA_MODEL] as const

export const FREEBUFF_MODEL_CONTEXT_WINDOWS: Record<string, number> = {
  [FREEBUFF_ALPHA_MODEL_ID]: 1_000_000,
}

export const FREEBUFF_DEFAULT_CONTEXT_WINDOW = 131_072

export const FREEBUFF_PAUSED_FREE_MODEL_IDS: readonly string[] = [
  FREEBUFF_PAUSED_MODEL_ID,
]

export const DEFAULT_FREEBUFF_MODEL_ID: FreebuffModelId = FREEBUFF_ALPHA_MODEL_ID
`,
		"free-agents.ts": `
export const FREEBUFF_WEB_BASE3_AGENT_ID_BY_MODEL: Record<string, string> = {
  [FREEBUFF_ALPHA_MODEL_ID]: 'base3-free-alpha',
  [FREEBUFF_GAMMA_MODEL_ID]: 'base3-free-gamma',
}

export const FREEBUFF_CLI_BASE3_AGENT_ID_BY_MODEL: Record<string, string> = {
  // Same root id as the Web map for a model both surfaces offer.
  [FREEBUFF_ALPHA_MODEL_ID]: 'base3-free-alpha',
  [FREEBUFF_BETA_MODEL_ID]: 'base3-free-beta',
}

// A model present only in SUPPORTED (Beta) has no picker entry, and the paused
// model has no agent root at all.
export const FREEBUFF_ROOT_AGENT_IDS = [
  'base2-free-alpha',
  'base2-free-beta',
  'base2-free-gamma',
]
`,
	}
}

func TestBuildCatalogResolvesIndirectionsAndOrder(t *testing.T) {
	entries, err := buildCatalog(fixtureSources())
	if err != nil {
		t.Fatalf("buildCatalog: %v", err)
	}

	byID := map[string]catalogEntry{}
	for _, e := range entries {
		byID[e.ID] = e
	}

	// The paused model must be dropped, and the member-reference model kept.
	if _, ok := byID["vendor/paused"]; ok {
		t.Fatal("paused model should be excluded from the catalog")
	}
	alpha, ok := byID["vendor/alpha"]
	if !ok {
		t.Fatalf("literal model id missing; got %v", keysOf(byID))
	}
	beta, ok := byID["vendor/beta"]
	if !ok {
		t.Fatalf("member-reference model id missing; got %v", keysOf(byID))
	}
	gamma, ok := byID["vendor/gamma"]
	if !ok {
		t.Fatalf("object-table model id missing; got %v", keysOf(byID))
	}

	// The upstream default leads, then picker order, then the remainder.
	if entries[0].ID != "vendor/alpha" {
		t.Fatalf("entries[0].ID = %q, want the upstream default vendor/alpha", entries[0].ID)
	}
	if entries[1].ID != "vendor/gamma" {
		t.Fatalf("entries[1].ID = %q, want the picker-order vendor/gamma", entries[1].ID)
	}

	// Agent ids come from the merged Web/CLI base3 maps.
	if alpha.AgentID != "base3-free-alpha" || alpha.LegacyAgentID != "base2-free-alpha" {
		t.Fatalf("alpha agents = %q / %q", alpha.AgentID, alpha.LegacyAgentID)
	}
	if beta.AgentID != "base3-free-beta" || beta.LegacyAgentID != "base2-free-beta" {
		t.Fatalf("beta agents = %q / %q", beta.AgentID, beta.LegacyAgentID)
	}

	// Picker membership follows FREEBUFF_MODELS, not SUPPORTED.
	if !alpha.InPicker || !gamma.InPicker {
		t.Fatalf("alpha/gamma should be picker models: %+v %+v", alpha, gamma)
	}
	if beta.InPicker {
		t.Fatal("beta is not in FREEBUFF_MODELS and must not be marked as a picker model")
	}

	// Context windows come from the table, falling back to the default.
	if alpha.ContextLength != 1_000_000 {
		t.Fatalf("alpha context = %d, want 1000000", alpha.ContextLength)
	}
	if gamma.ContextLength != 131_072 {
		t.Fatalf("gamma context = %d, want the fallback 131072", gamma.ContextLength)
	}
	if len(alpha.Thinking) != 2 || alpha.Thinking[0] != "low" || alpha.Thinking[1] != "high" {
		t.Fatalf("alpha thinking = %v, want [low high]", alpha.Thinking)
	}
}

func TestBuildCatalogRejectsEmptyResult(t *testing.T) {
	src := fixtureSources()
	// Pause every model that has a root agent, so nothing survives filtering.
	src["freebuff-models.ts"] = strings.Replace(src["freebuff-models.ts"],
		"export const FREEBUFF_PAUSED_FREE_MODEL_IDS: readonly string[] = [\n  FREEBUFF_PAUSED_MODEL_ID,\n]",
		"export const FREEBUFF_PAUSED_FREE_MODEL_IDS: readonly string[] = [\n  FREEBUFF_PAUSED_MODEL_ID,\n  FREEBUFF_ALPHA_MODEL_ID,\n  FREEBUFF_BETA_MODEL_ID,\n  FREEBUFF_GAMMA_MODEL_ID,\n]", 1)

	// The generator must fail loudly rather than emit an empty directory.
	if _, err := buildCatalog(src); err == nil {
		t.Fatal("buildCatalog should fail when nothing survives filtering")
	}
}

func TestBase2Twin(t *testing.T) {
	cases := map[string]string{
		"base3-free-glm-5-3-flash": "base2-free-glm-5-3-flash",
		"base3-free-mimo":          "base2-free-mimo",
		"base2-free-alpha":         "",
		"freebuff-desktop-thread":  "",
	}
	for in, want := range cases {
		if got := base2Twin(in); got != want {
			t.Errorf("base2Twin(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateURLRejectsUntrustedHosts(t *testing.T) {
	bad := []string{
		"http://localhost/foo",
		"https://127.0.0.1/foo",
		"https://example.com/foo",
		"file:///etc/passwd",
		"https://raw.githubusercontent.com.evil.test/x",
	}
	for _, raw := range bad {
		if err := validateURL(raw); err == nil {
			t.Errorf("validateURL(%q) should have been rejected", raw)
		}
	}
	good := "https://raw.githubusercontent.com/CodebuffAI/codebuff/main/common/src/constants/freebuff-models.ts"
	if err := validateURL(good); err != nil {
		t.Errorf("validateURL(%q) = %v, want nil", good, err)
	}
}

func keysOf(m map[string]catalogEntry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
