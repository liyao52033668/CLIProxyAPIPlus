package api

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/httpwire"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
)

func (s *Server) maxRequestBodyBytes() int64 {
	if s == nil {
		return 0
	}
	cfg := s.getConfig()
	if cfg == nil || cfg.MaxRequestBodyBytes <= 0 {
		return 0
	}
	return cfg.MaxRequestBodyBytes
}

type requestBodyTracker struct {
	io.ReadCloser
	contentLength int64
	bytesRead     int64
	sawEOF        bool
}

func (t *requestBodyTracker) Read(p []byte) (int, error) {
	if t == nil || t.ReadCloser == nil {
		return 0, io.EOF
	}
	n, errRead := t.ReadCloser.Read(p)
	if n > 0 {
		t.bytesRead += int64(n)
	}
	if errRead == io.EOF {
		t.sawEOF = true
	}
	return n, errRead
}

func (t *requestBodyTracker) isComplete() bool {
	if t == nil {
		return true
	}
	return t.sawEOF || (t.contentLength >= 0 && t.bytesRead >= t.contentLength)
}

type unconsumedBodyHTTPResponseWriter struct {
	http.ResponseWriter
	tracker     *requestBodyTracker
	status      int
	wroteHeader bool
	hijacked    bool
	closed      bool
}

func (w *unconsumedBodyHTTPResponseWriter) Unwrap() http.ResponseWriter {
	if w == nil {
		return nil
	}
	return w.ResponseWriter
}

func (w *unconsumedBodyHTTPResponseWriter) WriteHeader(code int) {
	w.status = code
	w.closeIfBodyUnconsumed(code)
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *unconsumedBodyHTTPResponseWriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		if w.status == 0 {
			w.status = http.StatusOK
		}
		w.closeIfBodyUnconsumed(w.status)
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(data)
}

func (w *unconsumedBodyHTTPResponseWriter) WriteString(s string) (int, error) {
	if !w.wroteHeader {
		if w.status == 0 {
			w.status = http.StatusOK
		}
		w.closeIfBodyUnconsumed(w.status)
		w.wroteHeader = true
	}
	return io.WriteString(w.ResponseWriter, s)
}

func (w *unconsumedBodyHTTPResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	conn, rw, errHijack := hijacker.Hijack()
	if errHijack == nil {
		w.hijacked = true
	}
	return conn, rw, errHijack
}

func (w *unconsumedBodyHTTPResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *unconsumedBodyHTTPResponseWriter) Push(target string, opts *http.PushOptions) error {
	if pusher, ok := w.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, opts)
	}
	return http.ErrNotSupported
}

func (w *unconsumedBodyHTTPResponseWriter) closeIfBodyUnconsumed(status int) {
	if w == nil || w.closed || w.hijacked || status < http.StatusOK {
		return
	}
	if w.tracker != nil && w.tracker.isComplete() {
		return
	}
	w.closed = true
	if !w.wroteHeader {
		w.ResponseWriter.Header().Set("Connection", "close")
	}
	_ = http.NewResponseController(w.ResponseWriter).SetReadDeadline(time.Unix(1, 0))
}

// wrapUnconsumedRequestBodyHandler tracks request bodies at the raw HTTP layer so
// responses that finish with an unconsumed body expire the connection read deadline
// and close it, preventing stalled unauthenticated bodies from exhausting descriptors.
func wrapUnconsumedRequestBodyHandler(next http.Handler) http.Handler {
	if next == nil {
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r == nil || r.Body == nil || r.Body == http.NoBody || (r.ContentLength == 0 && len(r.TransferEncoding) == 0) {
			next.ServeHTTP(w, r)
			return
		}
		tracker, ok := r.Body.(*requestBodyTracker)
		if !ok {
			tracker = &requestBodyTracker{
				ReadCloser:    r.Body,
				contentLength: r.ContentLength,
			}
			r.Body = tracker
		}
		wrappedWriter := &unconsumedBodyHTTPResponseWriter{
			ResponseWriter: w,
			tracker:        tracker,
		}
		defer func() {
			status := wrappedWriter.status
			if status == 0 {
				status = http.StatusOK
			}
			wrappedWriter.closeIfBodyUnconsumed(status)
		}()
		next.ServeHTTP(wrappedWriter, r)
	})
}

type unconsumedBodyCloseWriter struct {
	gin.ResponseWriter
	tracker  *requestBodyTracker
	hijacked bool
	closed   bool
}

func (w *unconsumedBodyCloseWriter) Unwrap() http.ResponseWriter {
	if w == nil {
		return nil
	}
	return w.ResponseWriter
}

func (w *unconsumedBodyCloseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, errHijack := w.ResponseWriter.Hijack()
	if errHijack == nil {
		w.hijacked = true
	}
	return conn, rw, errHijack
}

func (w *unconsumedBodyCloseWriter) WriteHeader(code int) {
	w.closeIfBodyUnconsumed(code)
	w.ResponseWriter.WriteHeader(code)
}

func (w *unconsumedBodyCloseWriter) WriteHeaderNow() {
	w.closeIfBodyUnconsumed(w.Status())
	w.ResponseWriter.WriteHeaderNow()
}

func (w *unconsumedBodyCloseWriter) Write(data []byte) (int, error) {
	status := w.Status()
	if status == 0 {
		status = http.StatusOK
	}
	w.closeIfBodyUnconsumed(status)
	return w.ResponseWriter.Write(data)
}

func (w *unconsumedBodyCloseWriter) WriteString(data string) (int, error) {
	status := w.Status()
	if status == 0 {
		status = http.StatusOK
	}
	w.closeIfBodyUnconsumed(status)
	return w.ResponseWriter.WriteString(data)
}

func (w *unconsumedBodyCloseWriter) Flush() {
	_ = w.FlushError()
}

func (w *unconsumedBodyCloseWriter) FlushError() error {
	status := w.Status()
	if status == 0 {
		status = http.StatusOK
	}
	w.closeIfBodyUnconsumed(status)
	return httpwire.FlushResponse(w.ResponseWriter)
}

func (w *unconsumedBodyCloseWriter) closeIfBodyUnconsumed(status int) {
	if w == nil || w.closed || w.hijacked || status < http.StatusOK {
		return
	}
	if w.tracker != nil && w.tracker.isComplete() {
		return
	}
	w.closed = true
	if !w.ResponseWriter.Written() {
		w.ResponseWriter.Header().Set("Connection", "close")
	}
	_ = http.NewResponseController(w.ResponseWriter).SetReadDeadline(time.Unix(1, 0))
}

func (s *Server) maxRequestBodyBytesMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c == nil || c.Request == nil {
			c.Next()
			return
		}
		if c.Request.Body != nil && c.Request.Body != http.NoBody && (c.Request.ContentLength != 0 || len(c.Request.TransferEncoding) > 0) {
			tracker, ok := c.Request.Body.(*requestBodyTracker)
			if !ok {
				tracker = &requestBodyTracker{
					ReadCloser:    c.Request.Body,
					contentLength: c.Request.ContentLength,
				}
				c.Request.Body = tracker
			}
			closeWriter := &unconsumedBodyCloseWriter{
				ResponseWriter: c.Writer,
				tracker:        tracker,
			}
			c.Writer = closeWriter
			defer func() {
				closeWriter.closeIfBodyUnconsumed(c.Writer.Status())
			}()
		}
		maxBytes := s.maxRequestBodyBytes()
		if maxBytes <= 0 {
			c.Next()
			return
		}
		c.Set(handlers.MaxRequestBodyBytesContextKey, maxBytes)
		if c.Request.ContentLength > maxBytes {
			closeUnconsumedRequestOnAbort(c)
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": "http: request body too large",
			})
			return
		}
		if c.Request.Body != nil && c.Request.Body != http.NoBody {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
	}
}

func closeUnconsumedRequestOnAbort(c *gin.Context) {
	if c == nil || c.Request == nil {
		return
	}
	if c.Request.ContentLength != 0 || len(c.Request.TransferEncoding) > 0 {
		c.Header("Connection", "close")
		_ = http.NewResponseController(c.Writer).SetReadDeadline(time.Unix(1, 0))
	}
}
