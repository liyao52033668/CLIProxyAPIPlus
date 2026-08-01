package helps

import (
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestProviderSessionUUIDStableAndScoped(t *testing.T) {
	metadata := map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "exec-1"}
	first := ProviderSessionUUID("openrouter", metadata)
	second := ProviderSessionUUID("openrouter", metadata)
	if first == "" || first != second {
		t.Fatalf("expected stable UUID, got %q and %q", first, second)
	}

	otherProvider := ProviderSessionUUID("other", metadata)
	if otherProvider == first {
		t.Fatalf("expected provider-scoped UUID, got %q", otherProvider)
	}
}

func TestProviderSessionUUIDFallsBackToDerivedID(t *testing.T) {
	metadata := map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:root"}
	got := ProviderSessionUUID("openrouter", metadata)
	if got == "" {
		t.Fatalf("expected derived UUID, got empty")
	}
	other := ProviderSessionUUID("openrouter", map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:other"})
	if other == got {
		t.Fatalf("expected different derived UUID, got %q", got)
	}
}

func TestProviderSessionUUIDEmptyWithoutIdentity(t *testing.T) {
	if got := ProviderSessionUUID("openrouter", nil); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	if got := ProviderSessionUUID("", map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "x"}); got != "" {
		t.Fatalf("expected empty for empty provider, got %q", got)
	}
}
