package auth

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func registerAutoFailoverAuth(t *testing.T, m *Manager, reg *registry.ModelRegistry, auth *Auth, models ...string) {
	t.Helper()
	infos := make([]*registry.ModelInfo, 0, len(models))
	for _, model := range models {
		infos = append(infos, &registry.ModelInfo{ID: model})
	}
	reg.RegisterClient(auth.ID, auth.Provider, infos)
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register %s: %v", auth.ID, err)
	}
}

func TestManagerExecute_AutoModelFailoverSwitchesModelWithinRequest(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "openai",
		executeErrors: map[string]error{
			"auth-model-a": &Error{HTTPStatus: http.StatusTooManyRequests, Message: "model a exhausted"},
		},
	}
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("auth-model-a", "openai", []*registry.ModelInfo{{ID: "gpt-4o"}})
	reg.RegisterClient("auth-model-b", "openai", []*registry.ModelInfo{{ID: "claude-sonnet-4-5"}})
	t.Cleanup(func() {
		reg.UnregisterClient("auth-model-a")
		reg.UnregisterClient("auth-model-b")
	})
	reg.SetHighestPriorityFunc(func(handlerType, modelID string) (int, bool) {
		switch modelID {
		case "gpt-4o":
			return 30, true
		case "claude-sonnet-4-5":
			return 20, true
		default:
			return 0, false
		}
	})
	defer reg.SetHighestPriorityFunc(nil)

	if _, err := m.Register(context.Background(), &Auth{ID: "auth-model-a", Provider: "openai"}); err != nil {
		t.Fatalf("register model a auth: %v", err)
	}
	if _, err := m.Register(context.Background(), &Auth{ID: "auth-model-b", Provider: "openai"}); err != nil {
		t.Fatalf("register model b auth: %v", err)
	}

	resp, err := m.Execute(context.Background(), []string{"openai"}, cliproxyexecutor.Request{Model: "gpt-4o"}, cliproxyexecutor.Options{IsAuto: true})
	if err != nil {
		t.Fatalf("execute error = %v, want success", err)
	}
	if string(resp.Payload) != "auth-model-b" {
		t.Fatalf("payload = %q, want auth-model-b", string(resp.Payload))
	}

	if got := executor.ExecuteCalls(); len(got) != 2 || got[0] != "auth-model-a" || got[1] != "auth-model-b" {
		t.Fatalf("execute calls = %v, want [auth-model-a auth-model-b]", got)
	}
}

func TestManagerExecuteCount_AutoModelFailoverSwitchesModelWithinRequest(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "openai",
		countErrors: map[string]error{
			"auth-model-a-count": &Error{HTTPStatus: http.StatusTooManyRequests, Message: "model a exhausted"},
		},
	}
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("auth-model-a-count", "openai", []*registry.ModelInfo{{ID: "gpt-4.1"}})
	reg.RegisterClient("auth-model-b-count", "openai", []*registry.ModelInfo{{ID: "claude-opus-4-7"}})
	t.Cleanup(func() {
		reg.UnregisterClient("auth-model-a-count")
		reg.UnregisterClient("auth-model-b-count")
	})
	reg.SetHighestPriorityFunc(func(handlerType, modelID string) (int, bool) {
		switch modelID {
		case "gpt-4.1":
			return 30, true
		case "claude-opus-4-7":
			return 20, true
		default:
			return 0, false
		}
	})
	defer reg.SetHighestPriorityFunc(nil)

	if _, err := m.Register(context.Background(), &Auth{ID: "auth-model-a-count", Provider: "openai"}); err != nil {
		t.Fatalf("register model a auth: %v", err)
	}
	if _, err := m.Register(context.Background(), &Auth{ID: "auth-model-b-count", Provider: "openai"}); err != nil {
		t.Fatalf("register model b auth: %v", err)
	}

	resp, err := m.ExecuteCount(context.Background(), []string{"openai"}, cliproxyexecutor.Request{Model: "gpt-4.1"}, cliproxyexecutor.Options{IsAuto: true})
	if err != nil {
		t.Fatalf("execute count error = %v, want success", err)
	}
	if string(resp.Payload) != "auth-model-b-count" {
		t.Fatalf("payload = %q, want auth-model-b-count", string(resp.Payload))
	}

	if got := executor.CountCalls(); len(got) != 2 || got[0] != "auth-model-a-count" || got[1] != "auth-model-b-count" {
		t.Fatalf("count calls = %v, want [auth-model-a-count auth-model-b-count]", got)
	}
}

func TestManagerExecuteStream_AutoModelFailoverSwitchesModelWithinRequest(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "openai",
		streamFirstErrors: map[string]error{
			"auth-model-a-stream": &Error{HTTPStatus: http.StatusTooManyRequests, Message: "model a exhausted"},
		},
	}
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("auth-model-a-stream", "openai", []*registry.ModelInfo{{ID: "gpt-4o-mini"}})
	reg.RegisterClient("auth-model-b-stream", "openai", []*registry.ModelInfo{{ID: "claude-3-7-sonnet"}})
	t.Cleanup(func() {
		reg.UnregisterClient("auth-model-a-stream")
		reg.UnregisterClient("auth-model-b-stream")
	})
	reg.SetHighestPriorityFunc(func(handlerType, modelID string) (int, bool) {
		switch modelID {
		case "gpt-4o-mini":
			return 30, true
		case "claude-3-7-sonnet":
			return 20, true
		default:
			return 0, false
		}
	})
	defer reg.SetHighestPriorityFunc(nil)

	if _, err := m.Register(context.Background(), &Auth{ID: "auth-model-a-stream", Provider: "openai"}); err != nil {
		t.Fatalf("register model a auth: %v", err)
	}
	if _, err := m.Register(context.Background(), &Auth{ID: "auth-model-b-stream", Provider: "openai"}); err != nil {
		t.Fatalf("register model b auth: %v", err)
	}

	streamResult, err := m.ExecuteStream(context.Background(), []string{"openai"}, cliproxyexecutor.Request{Model: "gpt-4o-mini"}, cliproxyexecutor.Options{IsAuto: true})
	if err != nil {
		t.Fatalf("execute stream error = %v, want success", err)
	}
	var payload []byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk error = %v, want success", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if string(payload) != "auth-model-b-stream" {
		t.Fatalf("payload = %q, want auth-model-b-stream", string(payload))
	}

	if got := executor.StreamCalls(); len(got) != 2 || got[0] != "auth-model-a-stream" || got[1] != "auth-model-b-stream" {
		t.Fatalf("stream calls = %v, want [auth-model-a-stream auth-model-b-stream]", got)
	}
}

func TestManagerExecute_AutoModelFailoverStopsOnInvalidRequest(t *testing.T) {
	m := NewManager(nil, nil, nil)
	invalidErr := &Error{HTTPStatus: http.StatusBadRequest, Message: "invalid_request_error: malformed payload"}
	executor := &authFallbackExecutor{
		id: "openai",
		executeErrors: map[string]error{
			"auth-invalid-a": invalidErr,
		},
	}
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("auth-invalid-a", "openai", []*registry.ModelInfo{{ID: "gpt-4o"}})
	reg.RegisterClient("auth-invalid-b", "openai", []*registry.ModelInfo{{ID: "claude-sonnet-4-5"}})
	t.Cleanup(func() {
		reg.UnregisterClient("auth-invalid-a")
		reg.UnregisterClient("auth-invalid-b")
	})
	reg.SetHighestPriorityFunc(func(handlerType, modelID string) (int, bool) {
		switch modelID {
		case "gpt-4o":
			return 30, true
		case "claude-sonnet-4-5":
			return 20, true
		default:
			return 0, false
		}
	})
	defer reg.SetHighestPriorityFunc(nil)

	if _, err := m.Register(context.Background(), &Auth{ID: "auth-invalid-a", Provider: "openai"}); err != nil {
		t.Fatalf("register invalid a auth: %v", err)
	}
	if _, err := m.Register(context.Background(), &Auth{ID: "auth-invalid-b", Provider: "openai"}); err != nil {
		t.Fatalf("register invalid b auth: %v", err)
	}

	_, err := m.Execute(context.Background(), []string{"openai"}, cliproxyexecutor.Request{Model: "gpt-4o"}, cliproxyexecutor.Options{IsAuto: true})
	if err == nil {
		t.Fatal("expected invalid request error")
	}
	if err != invalidErr {
		t.Fatalf("error = %v, want %v", err, invalidErr)
	}
	if got := executor.ExecuteCalls(); len(got) != 1 || got[0] != "auth-invalid-a" {
		t.Fatalf("execute calls = %v, want [auth-invalid-a]", got)
	}
}
