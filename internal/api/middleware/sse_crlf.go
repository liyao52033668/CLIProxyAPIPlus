// Package middleware provides Gin HTTP middleware components for the CLI Proxy API server.
// This file contains a middleware that normalizes SSE responses to the standard
// CRLF line termination required by strict client parsers.
package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// sseContentTypeMarker identifies Server-Sent Events responses.
const sseContentTypeMarker = "text/event-stream"

// sseNoBufferingHeader disables response buffering in reverse proxies (nginx,
// Railway and similar edge gateways) so each flushed SSE event reaches the
// client immediately instead of being coalesced with later frames.
const sseNoBufferingHeader = "X-Accel-Buffering"

// sseWriter is a gin.ResponseWriter wrapper that normalizes bare LF line
// terminators into CRLF for SSE responses. The WHATWG SSE specification
// accepts CR, LF, or CRLF as line endings, but strict client parsers (such
// as the stainless SDK used by the Claude CLI) only split lines on CRLF.
// Event producers in this codebase emit LF-only frames, so the bytes are
// normalized here at the point where the response leaves the server,
// leaving all internal chunk semantics unchanged.
type sseWriter struct {
	gin.ResponseWriter
	// isSSE caches whether this response is SSE; nil until first observation.
	isSSE *bool
	// pendingCR is true when the previous chunk ended with a lone CR that is
	// still undecided (it terminates a line only if the next byte is not LF).
	pendingCR bool
}

// shouldNormalize reports whether the current response is an SSE stream.
// The decision is made lazily on first write and cached, because handlers set
// the Content-Type header before writing any body bytes. On a positive first
// observation it also marks the response as unbuffered for reverse proxies;
// this happens before any body bytes are written, so the header is still
// mutable at that point.
func (w *sseWriter) shouldNormalize() bool {
	if w.isSSE == nil {
		ct := strings.Contains(w.ResponseWriter.Header().Get("Content-Type"), sseContentTypeMarker)
		w.isSSE = &ct
		if ct {
			w.ResponseWriter.Header().Set(sseNoBufferingHeader, "no")
		}
	}
	return *w.isSSE
}

// Write normalizes bare LF terminators in data to CRLF before forwarding it
// to the client when the response is SSE; other responses pass through as-is.
func (w *sseWriter) Write(data []byte) (int, error) {
	if !w.shouldNormalize() {
		return w.ResponseWriter.Write(data)
	}
	n, err := w.ResponseWriter.Write(w.normalize(w.consumePendingCR(), data))
	if n > len(data) {
		n = len(data)
	}
	return n, err
}

// WriteString normalizes bare LF terminators in data to CRLF before
// forwarding it to the client when the response is SSE.
func (w *sseWriter) WriteString(data string) (int, error) {
	if !w.shouldNormalize() {
		return w.ResponseWriter.WriteString(data)
	}
	n, err := w.ResponseWriter.Write(w.normalize(w.consumePendingCR(), []byte(data)))
	if n > len(data) {
		n = len(data)
	}
	return n, err
}

// Flush emits a buffered lone CR as a line terminator before flushing the
// underlying writer.
func (w *sseWriter) Flush() {
	if w.pendingCR {
		w.pendingCR = false
		_, _ = w.ResponseWriter.Write([]byte{'\r'})
	}
	if flusher, ok := w.ResponseWriter.(interface{ Flush() }); ok {
		flusher.Flush()
	}
}

// consumePendingCR resolves a CR left pending at the end of the previous
// chunk and returns it when it is a standalone line terminator (not part of
// a CRLF pair).
func (w *sseWriter) consumePendingCR() []byte {
	if !w.pendingCR {
		return nil
	}
	w.pendingCR = false
	return []byte{'\r'}
}

// normalize converts every bare LF (0x0A) in data to CRLF (0x0D 0x0A) and
// keeps every LF that already follows a CR. A leading CR in data pairs with
// prevCR (a lone CR held from the previous chunk) when present, so CRLF pairs
// are never duplicated across chunk boundaries. A trailing lone CR is held in
// w.pendingCR until the next write or flush resolves it. It returns the
// normalized bytes.
func (w *sseWriter) normalize(prevCR, data []byte) []byte {
	if len(prevCR) == 0 && len(data) == 0 {
		return data
	}
	out := make([]byte, 0, len(data)+len(prevCR)+1)
	out = append(out, prevCR...)
	prevWasCR := len(prevCR) > 0
	for _, b := range data {
		switch b {
		case '\r':
			out = append(out, b)
			prevWasCR = true
		case '\n':
			if !prevWasCR {
				out = append(out, '\r')
			}
			out = append(out, b)
			prevWasCR = false
		default:
			out = append(out, b)
			prevWasCR = false
		}
	}
	// Hold a trailing lone CR: it is only a line terminator if the stream
	// continues with a non-LF byte (or ends).
	if prevWasCR {
		out = out[:len(out)-1]
		w.pendingCR = true
	}
	return out
}

// SSELineTerminatorMiddleware normalizes SSE responses to use CRLF line
// termination. The gin writer is wrapped before downstream handlers run so
// that every SSE body write is normalized; the decision to normalize is made
// lazily per response based on the Content-Type header. Non-SSE responses
// pass through untouched. Register it before middleware that installs its own
// response writer (e.g. request logging) so this wrapper remains in the write
// chain closest to the client.
func SSELineTerminatorMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		writer, ok := c.Writer.(gin.ResponseWriter)
		if ok {
			c.Writer = &sseWriter{ResponseWriter: writer}
		}
		c.Next()
	}
}

// NormalizeSSELineTerminators converts every bare LF (0x0A) in data to CRLF
// (0x0D 0x0A) and keeps every LF that already follows a CR, so SSE frames
// delimited by "\n\n" become standard "\r\n\r\n" without doubling pairs.
// It returns data unchanged when no normalization is needed.
func NormalizeSSELineTerminators(data []byte) []byte {
	bareLF := 0
	prevWasCR := false
	for _, b := range data {
		switch b {
		case '\r':
			prevWasCR = true
		case '\n':
			if !prevWasCR {
				bareLF++
			}
			prevWasCR = false
		default:
			prevWasCR = false
		}
	}
	if bareLF == 0 {
		return data
	}
	out := make([]byte, 0, len(data)+bareLF)
	prevWasCR = false
	for _, b := range data {
		switch b {
		case '\r':
			out = append(out, b)
			prevWasCR = true
		case '\n':
			if !prevWasCR {
				out = append(out, '\r')
			}
			out = append(out, b)
			prevWasCR = false
		default:
			out = append(out, b)
			prevWasCR = false
		}
	}
	return out
}
