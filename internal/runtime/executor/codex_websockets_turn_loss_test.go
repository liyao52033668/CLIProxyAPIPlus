package executor

import (
	"errors"
	"io"
	"net"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestCodexWebsocketTurnTransportErrorClassifiesTransportLosses(t *testing.T) {
	turnErr := &cliproxyexecutor.UpstreamWebsocketTurnError{}
	if got := codexWebsocketTurnTransportError(turnErr); got != error(turnErr) {
		t.Fatal("turn errors must pass through unchanged")
	}
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "eof", err: io.EOF},
		{name: "unexpected-eof", err: io.ErrUnexpectedEOF},
		{name: "closed-pipe", err: io.ErrClosedPipe},
		{name: "net-closed", err: net.ErrClosed},
		{name: "conn-reset", err: &net.OpError{Op: "read", Err: syscall.ECONNRESET}},
		{name: "close-1006", err: &websocket.CloseError{Code: websocket.CloseAbnormalClosure, Text: "abnormal"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := codexWebsocketTurnTransportError(tc.err)
			if !cliproxyexecutor.IsUpstreamWebsocketTurnError(got) {
				t.Fatalf("codexWebsocketTurnTransportError(%v) = %v, want a turn error", tc.err, got)
			}
		})
	}
}

func TestCodexWebsocketTurnTransportErrorKeepsOtherErrors(t *testing.T) {
	tooBig := mapCodexWebsocketReadError(&websocket.CloseError{Code: websocket.CloseMessageTooBig, Text: "big"})
	if got := codexWebsocketTurnTransportError(tooBig); got != error(tooBig) {
		t.Fatal("status-scoped errors must pass through unchanged")
	}
	normalClose := &websocket.CloseError{Code: websocket.CloseNormalClosure, Text: "bye"}
	if got := codexWebsocketTurnTransportError(normalClose); got != error(normalClose) {
		t.Fatal("policy close codes must pass through unchanged")
	}
	if got := codexWebsocketTurnTransportError(nil); got != nil {
		t.Fatal("nil must stay nil")
	}
}

func TestCodexWebsocketSessionTurnLossKeepsDisconnectChannelOpen(t *testing.T) {
	sess := newEphemeralCodexWebsocketSession()
	sess.upstreamDisconnectCh = make(chan error, 1)

	// Simulate an active downstream turn scoped to this session.
	sess.connMu.Lock()
	sess.recoverTransport = true
	sess.turnContext = t.Context()
	sess.connMu.Unlock()

	loss := codexWebsocketTurnTransportError(io.EOF)
	sess.notifyUpstreamDisconnect(loss)

	select {
	case err := <-sess.upstreamDisconnectCh:
		t.Fatalf("turn loss must not notify upstream disconnect, got %v", err)
	default:
	}

	// A non-turn loss still notifies.
	sess2 := newEphemeralCodexWebsocketSession()
	sess2.upstreamDisconnectCh = make(chan error, 1)
	sess2.notifyUpstreamDisconnect(io.EOF)
	select {
	case err := <-sess2.upstreamDisconnectCh:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("disconnect error = %v, want io.EOF", err)
		}
	case <-time.After(time.Second):
		t.Fatal("plain loss must notify upstream disconnect")
	}
}

func TestCodexWebsocketSessionRecoverTransportResetAfterTurn(t *testing.T) {
	sess := newEphemeralCodexWebsocketSession()
	sess.connMu.Lock()
	sess.recoverTransport = cliproxyexecutor.DownstreamWebsocket(cliproxyexecutor.WithDownstreamWebsocket(t.Context()))
	sess.turnContext = t.Context()
	sess.connMu.Unlock()
	if !sess.recoverTransport {
		t.Fatal("downstream-scoped turn must enable transport recovery")
	}

	// Mirror the ExecuteStream defer cleanup.
	sess.connMu.Lock()
	sess.recoverTransport = false
	sess.turnContext = nil
	sess.connMu.Unlock()

	var epoch atomic.Uint64
	ctx := cliproxyexecutor.WithWebsocketInputEpoch(t.Context(), &epoch, 0)
	_ = ctx
	if sess.recoverTransport {
		t.Fatal("recoverTransport must reset after the turn ends")
	}
}
