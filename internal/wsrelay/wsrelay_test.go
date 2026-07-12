package wsrelay

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func newRelayTestServer(t *testing.T, manager *Manager) (*httptest.Server, string) {
	t.Helper()
	server := httptest.NewServer(manager.Handler())
	t.Cleanup(server.Close)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + manager.Path()
	return server, wsURL
}

func waitForRelaySession(t *testing.T, manager *Manager, provider string) *session {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if sess := manager.session(provider); sess != nil {
			return sess
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("provider %q did not connect", provider)
	return nil
}

func TestSessionDispatchDeliversBurstWithoutDropping(t *testing.T) {
	manager := NewManager(Options{})
	sess := &session{
		manager:  manager,
		provider: "burst",
		closed:   make(chan struct{}),
	}
	request := newPendingRequest(context.Background())
	sess.pending.Store("request-1", request)

	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		for i := 0; i < 32; i++ {
			sess.dispatch(Message{
				ID:      "request-1",
				Type:    MessageTypeStreamChunk,
				Payload: map[string]any{"data": fmt.Sprintf("chunk-%02d", i)},
			})
		}
		sess.dispatch(Message{ID: "request-1", Type: MessageTypeStreamEnd})
	}()

	var chunks []string
	for msg := range request.ch {
		if msg.Type == MessageTypeStreamChunk {
			chunks = append(chunks, string(decodeChunk(msg.Payload)))
		}
	}
	select {
	case <-producerDone:
	case <-time.After(time.Second):
		t.Fatal("dispatch producer did not complete")
	}

	if len(chunks) != 32 {
		t.Fatalf("received %d chunks, want 32", len(chunks))
	}
	for i, chunk := range chunks {
		want := fmt.Sprintf("chunk-%02d", i)
		if chunk != want {
			t.Fatalf("chunk %d = %q, want %q", i, chunk, want)
		}
	}
}

func TestConsumeNonStreamResponseReturnsErrorWhenChannelClosesBeforeTerminalResponse(t *testing.T) {
	responseCh := make(chan Message, 2)
	responseCh <- Message{
		ID:      "request-1",
		Type:    MessageTypeStreamStart,
		Payload: map[string]any{"status": float64(http.StatusOK)},
	}
	responseCh <- Message{
		ID:      "request-1",
		Type:    MessageTypeStreamChunk,
		Payload: map[string]any{"data": "partial"},
	}
	close(responseCh)

	response, errResponse := consumeNonStreamResponse(context.Background(), responseCh)
	if errResponse == nil {
		t.Fatalf("consumeNonStreamResponse() returned partial success: %#v", response)
	}
}

func TestManagerStopRejectsNewWebsocket(t *testing.T) {
	manager := NewManager(Options{Path: "/relay"})
	_, wsURL := newRelayTestServer(t, manager)

	if errStop := manager.Stop(context.Background()); errStop != nil {
		t.Fatalf("Stop() error = %v", errStop)
	}

	conn, response, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if conn != nil {
		_ = conn.Close()
	}
	if errDial == nil {
		t.Fatal("Dial() succeeded after Manager.Stop()")
	}
	if response == nil || response.StatusCode != http.StatusServiceUnavailable {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("upgrade status = %d, want %d", status, http.StatusServiceUnavailable)
	}
}
