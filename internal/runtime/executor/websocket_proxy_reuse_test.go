package executor

import (
	"testing"

	"github.com/gorilla/websocket"
)

// TestWebsocketSessionIsolatesReusableConnectionByProxy verifies that websocket
// session reuse is isolated by the effective proxy endpoint. Adapted from the
// upstream split-file test to the local monolith session API: reuse matching is
// exercised through websocketSessionTargetChanged and
// detachMismatchedWebsocketSessionConn because the local monolith has no
// existingWebsocketSessionConn helper or connection closer type.
func TestWebsocketSessionIsolatesReusableConnectionByProxy(t *testing.T) {
	conn := &websocket.Conn{}
	sess := &codexWebsocketSession{
		authID:   "auth-1",
		wsURL:    "wss://upstream.example/v1",
		proxyURL: "http://proxy-a.example:8081",
		conn:     conn,
	}

	if !websocketSessionTargetChanged(sess, "auth-1", sess.wsURL, "http://proxy-b.example:8082") {
		t.Fatal("proxy change was not treated as a websocket target change")
	}
	if websocketSessionTargetChanged(sess, "auth-1", sess.wsURL, sess.proxyURL) {
		t.Fatal("same proxy was treated as a websocket target change")
	}
	if detached, _, _ := detachMismatchedWebsocketSessionConn(sess, "auth-1", sess.wsURL, sess.proxyURL); detached != nil {
		t.Fatal("matching proxy detached the websocket connection")
	}
	if sess.conn == nil {
		t.Fatal("matching proxy detached the websocket connection")
	}

	detached, _, _ := detachMismatchedWebsocketSessionConn(sess, "auth-1", sess.wsURL, "")
	if detached == nil {
		t.Fatal("removing the proxy override did not detach the websocket")
	}
	if sess.conn != nil {
		t.Fatal("detached websocket remained attached")
	}
}
