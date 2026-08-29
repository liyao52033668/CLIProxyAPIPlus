package executor

import (
	"bytes"
	"compress/gzip"
	"slices"
	"testing"
	"time"

	cursorproto "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/proto"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestSplitCursorClientType(t *testing.T) {
	cases := []struct {
		in            string
		wantType      string
		wantModel     string
		defaultIsType string // expected type when no prefix matches
	}{
		{in: "sand/grok-4", wantType: "sand", wantModel: "grok-4"},
		{in: "bot/grok-4", wantType: "sand", wantModel: "grok-4"},
		{in: "grokbot/Grok-4", wantType: "sand", wantModel: "Grok-4"},
		{in: "cli/claude-4-sonnet", wantType: "cli", wantModel: "claude-4-sonnet"},
	}
	for _, tc := range cases {
		gotType, gotModel := splitCursorClientType(tc.in)
		if gotType != tc.wantType || gotModel != tc.wantModel {
			t.Errorf("splitCursorClientType(%q) = (%q, %q), want (%q, %q)",
				tc.in, gotType, gotModel, tc.wantType, tc.wantModel)
		}
	}
	// Plain names pass through with the configured default identity.
	gotType, gotModel := splitCursorClientType("claude-4-sonnet")
	if gotModel != "claude-4-sonnet" || gotType != cursorClientTypeSetting() {
		t.Errorf("splitCursorClientType(plain) = (%q, %q), want default type %q",
			gotType, gotModel, cursorClientTypeSetting())
	}
}

func TestCursorNearestEffort(t *testing.T) {
	allowed := []string{"low", "medium", "high"}
	cases := []struct{ in, want string }{
		{"high", "high"},
		{"low", "low"},
		{"xhigh", "high"},  // clamps to the strongest published value
		{"minimal", "low"}, // alias
		{"default", "medium"},
		{"bogus", ""},
	}
	for _, tc := range cases {
		if got := cursorNearestEffort(tc.in, allowed); got != tc.want {
			t.Errorf("cursorNearestEffort(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCursorModelParamsFor(t *testing.T) {
	cursorSetParamOptions("grok-test", map[string][]string{
		"thinking": {"true", "false"},
		"effort":   {"low", "medium", "high"},
	})
	defer cursorModelParamOptions.Delete("grok-test")

	if params := cursorModelParamsFor("grok-test", "none"); len(params) != 1 || params["thinking"] != "false" {
		t.Errorf("none params = %v, want {thinking:false}", params)
	}
	if params := cursorModelParamsFor("grok-test", "high"); params["thinking"] != "true" || params["effort"] != "high" {
		t.Errorf("high params = %v, want thinking:true effort:high", params)
	}
	if params := cursorModelParamsFor("grok-test", "max"); params["effort"] != "high" {
		t.Errorf("max params = %v, want effort clamped to high", params)
	}
	if params := cursorModelParamsFor("unknown-model", "high"); params != nil {
		t.Errorf("unknown model params = %v, want nil", params)
	}
}

func TestCursorLivenessGuards(t *testing.T) {
	// Silence guard: even heartbeats stopped past the idle window.
	l := newCursorLiveness()
	l.markFrame()
	l.lastFrame = time.Now().Add(-1 * time.Hour)
	if _, err := l.check(); err == nil {
		t.Error("expected silence guard to fire")
	}

	// No-response guard: nothing at all arrived within the first timeout.
	nr := newCursorLiveness()
	nr.started = time.Now().Add(-1 * time.Hour)
	if _, err := nr.check(); err == nil {
		t.Error("expected no-response guard to fire")
	}

	// First-output guard: control frames arrived but no user-visible output.
	fo := newCursorLiveness()
	fo.lastFrame = time.Now().Add(-1 * time.Hour)
	if _, err := fo.check(); err == nil {
		t.Error("expected first-output guard to fire")
	}

	// Healthy stream: output seen, frames fresh — no error.
	ok := newCursorLiveness()
	ok.markFrame()
	ok.markOutput()
	if _, err := ok.check(); err != nil {
		t.Errorf("healthy stream errored: %v", err)
	}
}

// --- protobuf builders for catalog parsing tests ---

func tvarint(v uint64) []byte {
	var out []byte
	for v >= 0x80 {
		out = append(out, byte(v)|0x80)
		v >>= 7
	}
	return append(out, byte(v))
}

func tnum(num int, v uint64) []byte {
	return append(tvarint(uint64(num<<3|0)), tvarint(v)...)
}

func tlen(num int, payload []byte) []byte {
	out := tvarint(uint64(num<<3 | 2))
	out = append(out, tvarint(uint64(len(payload)))...)
	return append(out, payload...)
}

func ts(num int, s string) []byte { return tlen(num, []byte(s)) }

func TestParseAvailableModelsResponse(t *testing.T) {
	// One option group per parameter value set.
	boolValues := func(values ...string) []byte {
		var group []byte
		for _, v := range values {
			group = append(group, tlen(1, ts(1, v))...)
		}
		return tlen(1, group) // bool_options
	}
	enumValues := func(values ...string) []byte {
		var group []byte
		for _, v := range values {
			group = append(group, tlen(1, ts(1, v))...)
		}
		return tlen(2, group) // enum_options
	}
	thinkingDef := append(ts(1, "thinking"), tlen(4, boolValues("true", "false"))...)
	effortDef := append(ts(1, "effort"), tlen(4, enumValues("low", "medium", "high"))...)
	model := ts(1, "grok-4.6")
	model = append(model, tnum(9, 1)...)       // supports_thinking
	model = append(model, tnum(15, 300000)...) // context_token_limit
	model = append(model, tlen(29, thinkingDef)...)
	model = append(model, tlen(29, effortDef)...)
	response := tlen(2, model)

	models := parseAvailableModelsResponse(response)
	if len(models) != 1 {
		t.Fatalf("parsed %d models, want 1", len(models))
	}
	info := models[0]
	if info.ID != "grok-4.6" || info.ContextLength != 300000 {
		t.Fatalf("model = %s context = %d, want grok-4.6 / 300000", info.ID, info.ContextLength)
	}
	if info.Thinking == nil || !info.Thinking.ZeroAllowed {
		t.Fatalf("thinking support = %+v, want ZeroAllowed", info.Thinking)
	}
	if len(info.Thinking.Levels) != 3 || info.Thinking.Levels[0] != "low" {
		t.Fatalf("levels = %v, want low/medium/high", info.Thinking.Levels)
	}

	// The cached options table feeds request-time parameter gating.
	raw, ok := cursorModelParamOptions.Load("grok-4.6")
	if !ok {
		t.Fatal("options not cached for parsed model")
	}
	options := raw.(map[string][]string)
	if len(options["thinking"]) != 2 || len(options["effort"]) != 3 {
		t.Fatalf("cached options = %v", options)
	}
}

func TestParseAvailableModelsResponseKeepsDefault(t *testing.T) {
	// "default" is a valid account-level id; it must survive parsing.
	response := tlen(2, ts(1, "default"))
	models := parseAvailableModelsResponse(response)
	if len(models) != 1 || models[0].ID != "default" {
		t.Fatalf("default entry lost: %d models", len(models))
	}
}

func TestMergeCursorModels(t *testing.T) {
	base := []*registry.ModelInfo{
		{ID: "default", ContextLength: 0},
		{ID: "composer-2", ContextLength: 200000, Thinking: &registry.ThinkingSupport{Max: 50000, DynamicAllowed: true}},
	}
	rich := []*registry.ModelInfo{
		// Overlapping id: enriches the base entry in place.
		{ID: "composer-2", ContextLength: 300000, Thinking: &registry.ThinkingSupport{
			Max: 50000, DynamicAllowed: true, ZeroAllowed: true, Levels: []string{"low", "high"},
		}},
		// Picker-only id: appended.
		{ID: "claude-fable-5", ContextLength: 300000},
	}

	merged := mergeCursorModels(base, rich)
	if len(merged) != 3 {
		t.Fatalf("merged %d models, want 3", len(merged))
	}
	ids := make([]string, 0, len(merged))
	for _, m := range merged {
		ids = append(ids, m.ID)
	}
	for _, want := range []string{"default", "composer-2", "claude-fable-5"} {
		if !slices.Contains(ids, want) {
			t.Errorf("merged ids %v missing %q", ids, want)
		}
	}

	var composer *registry.ModelInfo
	for _, m := range merged {
		if m.ID == "composer-2" {
			composer = m
		}
	}
	if composer.ContextLength != 300000 {
		t.Errorf("composer-2 context = %d, want enriched 300000", composer.ContextLength)
	}
	if composer.Thinking == nil || !composer.Thinking.ZeroAllowed || len(composer.Thinking.Levels) != 2 {
		t.Errorf("composer-2 thinking = %+v, want enriched levels/zeroAllowed", composer.Thinking)
	}
}

func TestDecompressConnectPayload(t *testing.T) {
	payload := []byte("hello connect")
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	zw.Close()

	got, err := cursorproto.DecompressConnectPayload(cursorproto.ConnectCompressionFlag, buf.Bytes())
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("decompressed = %q err = %v, want %q", got, err, payload)
	}

	// Uncompressed frames pass through untouched.
	got, err = cursorproto.DecompressConnectPayload(0, payload)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("passthrough = %q err = %v, want %q", got, err, payload)
	}
}
