package executor

import (
	"bytes"
	"testing"
)

func TestSplitRelayFrames(t *testing.T) {
	multi := "data: {\"text\":\"A\"}\n\ndata: {\"text\":\"B\"}\n\n"
	frames := splitRelayFrames([]byte(multi))
	if len(frames) != 2 || string(frames[0]) != "data: {\"text\":\"A\"}\n\n" || string(frames[1]) != "data: {\"text\":\"B\"}\n\n" {
		t.Fatalf("multi-frame split = %q", frames)
	}

	crlf := "event: message\r\ndata: {\"a\":1}\r\n\r\n"
	frames = splitRelayFrames([]byte(crlf))
	if len(frames) != 1 || string(frames[0]) != "data: {\"a\":1}\n\n" {
		t.Fatalf("crlf split = %q", frames)
	}

	keepAlive := ": keep-alive\n\ndata: {\"b\":2}\n\n"
	frames = splitRelayFrames([]byte(keepAlive))
	if len(frames) != 1 || string(frames[0]) != "data: {\"b\":2}\n\n" {
		t.Fatalf("keep-alive split = %q", frames)
	}

	for _, whole := range []string{"{\n  \"text\": \"v\"\n\n}", "[DONE]", "{\"a\":1}"} {
		frames := splitRelayFrames([]byte(whole))
		if len(frames) != 1 || !bytes.Equal(frames[0], []byte(whole)) {
			t.Fatalf("bare payload %q changed: %q", whole, frames)
		}
	}
}
