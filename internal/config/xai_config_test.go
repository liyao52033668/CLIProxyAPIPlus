package config

import "testing"

func TestParseConfigBytesXAIConfig(t *testing.T) {
	defaultCfg, errDefault := ParseConfigBytes([]byte(`{}`))
	if errDefault != nil {
		t.Fatalf("ParseConfigBytes(default) error = %v", errDefault)
	}
	if defaultCfg.XAI.InjectXSearch {
		t.Fatal("xai.inject-x-search = true by default, want false")
	}

	enabledCfg, errEnabled := ParseConfigBytes([]byte(`xai:
  inject-x-search: true
`))
	if errEnabled != nil {
		t.Fatalf("ParseConfigBytes(enabled) error = %v", errEnabled)
	}
	if !enabledCfg.XAI.InjectXSearch {
		t.Fatal("xai.inject-x-search = false, want true")
	}
}
