package executor

import (
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestUpstreamWebsocketTurnErrorSemantics(t *testing.T) {
	lost := &UpstreamWebsocketTurnError{}
	if got := lost.StatusCode(); got != http.StatusBadGateway {
		t.Fatalf("StatusCode() = %d, want %d", got, http.StatusBadGateway)
	}
	if !lost.IsRequestScoped() {
		t.Fatal("turn error must be request-scoped")
	}
	if !IsUpstreamWebsocketTurnError(lost) {
		t.Fatal("IsUpstreamWebsocketTurnError(lost) = false")
	}
	wrapped := &UpstreamWebsocketTurnError{Cause: io.EOF}
	if !IsUpstreamWebsocketTurnError(wrapped) {
		t.Fatal("wrapped turn error not detected")
	}
	if !errors.Is(wrapped, io.EOF) {
		t.Fatal("turn error must unwrap to its cause")
	}
	if IsUpstreamWebsocketTurnError(io.EOF) {
		t.Fatal("plain transport error must not be a turn error")
	}
}

func TestWebsocketInputLossEpoch(t *testing.T) {
	var epoch atomic.Uint64
	ctx := WithWebsocketInputEpoch(t.Context(), &epoch, 0)
	if WebsocketInputPredatesLoss(ctx) {
		t.Fatal("fresh input must not predate a loss")
	}
	if WebsocketInputLossError(ctx) != nil {
		t.Fatal("fresh input must not produce a loss error")
	}

	NotifyWebsocketInputLoss(ctx)
	if epoch.Load() != 1 {
		t.Fatalf("epoch = %d, want 1", epoch.Load())
	}
	if !WebsocketInputPredatesLoss(ctx) {
		t.Fatal("input read before the loss must predate it")
	}
	if !IsUpstreamWebsocketTurnError(WebsocketInputLossError(ctx)) {
		t.Fatal("loss error must be a turn error")
	}

	required := WithWebsocketInputEpoch(WithRequiredUpstreamWebsocket(t.Context()), &epoch, 0)
	NotifyWebsocketInputLoss(required)
	lossErr := WebsocketInputLossError(required)
	var turnErr *UpstreamWebsocketTurnError
	if !errors.As(lossErr, &turnErr) || !turnErr.FullContextRequired {
		t.Fatalf("required-upstream loss error = %v, want FullContextRequired", lossErr)
	}
}

func TestNotifyWebsocketInputLossWithoutState(t *testing.T) {
	NotifyWebsocketInputLoss(nil)
	NotifyWebsocketInputLoss(t.Context())
	if WebsocketInputPredatesLoss(t.Context()) {
		t.Fatal("context without epoch state must not predate a loss")
	}
}
