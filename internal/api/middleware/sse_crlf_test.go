package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func newSSETestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/stream", nil)
	return c, recorder
}

func runThroughSSEMiddleware(c *gin.Context, handler gin.HandlerFunc) {
	// The middleware wraps c.Writer, then c.Next() is a no-op on an empty
	// handler chain; run the handler afterwards so it writes through the
	// wrapped writer, mirroring engine execution order.
	SSELineTerminatorMiddleware()(c)
	handler(c)
}

func TestSSELineTerminatorMiddlewareNormalizesLF(t *testing.T) {
	c, recorder := newSSETestContext()

	runThroughSSEMiddleware(c, func(c *gin.Context) {
		c.Header("Content-Type", sseContentTypeMarker)
		fmt.Fprintf(c.Writer, "event: message_start\ndata: {\"type\":\"message_start\"}\n\n")
	})

	want := "event: message_start\r\ndata: {\"type\":\"message_start\"}\r\n\r\n"
	if got := recorder.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	// SSE responses must be marked unbuffered so reverse proxies (nginx,
	// Railway edge gateways, ...) flush every event instead of coalescing them.
	if got := recorder.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want %q", got, "no")
	}
}

func TestSSELineTerminatorMiddlewareKeepsExistingCRLF(t *testing.T) {
	c, recorder := newSSETestContext()

	runThroughSSEMiddleware(c, func(c *gin.Context) {
		c.Header("Content-Type", sseContentTypeMarker)
		fmt.Fprintf(c.Writer, "data: {\"ok\":true}\r\n\r\ndata: [DONE]\n\n")
	})

	want := "data: {\"ok\":true}\r\n\r\ndata: [DONE]\r\n\r\n"
	if got := recorder.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestSSELineTerminatorMiddlewareLeavesNonSSEResponses(t *testing.T) {
	c, recorder := newSSETestContext()

	runThroughSSEMiddleware(c, func(c *gin.Context) {
		c.Header("Content-Type", "application/json")
		fmt.Fprintf(c.Writer, "{\"line\":\"a\nb\"}\n")
	})

	want := "{\"line\":\"a\nb\"}\n"
	if got := recorder.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	if got := recorder.Header().Get("X-Accel-Buffering"); got != "" {
		t.Fatalf("X-Accel-Buffering = %q on non-SSE response, want unset", got)
	}
}

func TestSSELineTerminatorMiddlewareNormalizesAcrossChunkBoundary(t *testing.T) {
	c, recorder := newSSETestContext()

	runThroughSSEMiddleware(c, func(c *gin.Context) {
		c.Header("Content-Type", sseContentTypeMarker)
		// Event separator split across two writes.
		_, _ = c.Writer.Write([]byte("data: {\"n\":1}\n"))
		_, _ = c.Writer.Write([]byte("\n"))
		// CR/LF pair split across two writes.
		_, _ = c.Writer.Write([]byte("data: {\"n\":2}\r"))
		_, _ = c.Writer.Write([]byte("\n\n"))
	})

	want := "data: {\"n\":1}\r\n\r\ndata: {\"n\":2}\r\n\r\n"
	if got := recorder.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestSSEWriterFlushEmitsHeldCR(t *testing.T) {
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", sseContentTypeMarker)
	c, _ := gin.CreateTestContext(recorder)

	writer := &sseWriter{ResponseWriter: c.Writer}
	if _, err := writer.Write([]byte("data: keep-alive\r")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := recorder.Body.String(); got != "data: keep-alive" {
		t.Fatalf("body before flush = %q, want %q", got, "data: keep-alive")
	}
	writer.Flush()
	if got := recorder.Body.String(); got != "data: keep-alive\r" {
		t.Fatalf("body after flush = %q, want %q", got, "data: keep-alive\r")
	}
}

func TestSSEWriterHeldCRPairsWithNextLF(t *testing.T) {
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", sseContentTypeMarker)
	c, _ := gin.CreateTestContext(recorder)

	writer := &sseWriter{ResponseWriter: c.Writer}
	if _, err := writer.Write([]byte("abc\r")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := writer.Write([]byte("\ndef")); err != nil {
		t.Fatalf("write: %v", err)
	}
	writer.Flush()

	want := "abc\r\ndef"
	if got := recorder.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestSSEWriterWriteStringNormalizes(t *testing.T) {
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", sseContentTypeMarker)
	c, _ := gin.CreateTestContext(recorder)

	writer := &sseWriter{ResponseWriter: c.Writer}
	n, err := writer.WriteString("data: hello\n\n")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != len("data: hello\n\n") {
		t.Fatalf("n = %d, want %d", n, len("data: hello\n\n"))
	}
	want := "data: hello\r\n\r\n"
	if got := recorder.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestNormalizeSSELineTerminators(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"bare LF", "data: a\n\ndata: b\n\n", "data: a\r\n\r\ndata: b\r\n\r\n"},
		{"existing CRLF untouched", "data: a\r\n\r\n", "data: a\r\n\r\n"},
		{"mixed", "event: e\ndata: {}\r\n\r\n", "event: e\r\ndata: {}\r\n\r\n"},
		{"lone CR preserved", ": keep-alive\r", ": keep-alive\r"},
		{"CR CR LF sequence", "a\r\r\n", "a\r\r\n"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeSSELineTerminators([]byte(tt.input))
			if string(got) != tt.want {
				t.Fatalf("NormalizeSSELineTerminators(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}

	// No bare LF means the input slice is returned unchanged.
	input := []byte("data: a\r\n\r\n")
	if got := NormalizeSSELineTerminators(input); &got[0] != &input[0] {
		t.Fatal("expected the original slice when no normalization is needed")
	}
}
