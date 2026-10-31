package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestRequestBodyTrackerIsComplete(t *testing.T) {
	tracker := &requestBodyTracker{
		ReadCloser:    io.NopCloser(strings.NewReader("hello")),
		contentLength: 5,
	}
	if tracker.isComplete() {
		t.Fatal("fresh tracker must not be complete")
	}
	buf := make([]byte, 8)
	n, errRead := tracker.Read(buf)
	if errRead != nil || n != 5 {
		t.Fatalf("Read() = %d, %v; want 5, nil", n, errRead)
	}
	if !tracker.isComplete() {
		t.Fatal("tracker must be complete after reading ContentLength bytes")
	}

	unknown := &requestBodyTracker{
		ReadCloser:    io.NopCloser(strings.NewReader("x")),
		contentLength: -1,
	}
	buf2 := make([]byte, 4)
	for {
		n, errRead := unknown.Read(buf2)
		if errRead != nil {
			break
		}
		if n == 0 {
			t.Fatal("stuck reading")
		}
	}
	if !unknown.sawEOF {
		t.Fatal("sawEOF must be set at EOF")
	}
	if !unknown.isComplete() {
		t.Fatal("unknown-length tracker must be complete at EOF")
	}
}

func TestWrapUnconsumedRequestBodyHandlerClosesOnUnconsumedBody(t *testing.T) {
	newRequest := func(body string, contentLength int64) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.ContentLength = contentLength
		return req
	}

	t.Run("unconsumed body closes connection", func(t *testing.T) {
		handler := wrapUnconsumedRequestBodyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Never read the body: the response must expire the read deadline.
			w.WriteHeader(http.StatusUnauthorized)
		}))
		req := newRequest("never-read", 10)
		rr := &readDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		handler.ServeHTTP(rr, req)
		if got := rr.Header().Get("Connection"); got != "close" {
			t.Fatalf("Connection header = %q, want close", got)
		}
		if !rr.deadlineSet {
			t.Fatal("SetReadDeadline must be called for the unconsumed body")
		}
	})

	t.Run("consumed body keeps connection alive", func(t *testing.T) {
		handler := wrapUnconsumedRequestBodyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
		}))
		req := newRequest("read-all", 8)
		rr := &readDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		handler.ServeHTTP(rr, req)
		if got := rr.Header().Get("Connection"); got != "" {
			t.Fatalf("Connection header = %q, want empty", got)
		}
		if rr.deadlineSet {
			t.Fatal("SetReadDeadline must not be called when the body was consumed")
		}
	})

	t.Run("bodyless request passes through untouched", func(t *testing.T) {
		var seenTracker bool
		handler := wrapUnconsumedRequestBodyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, seenTracker = r.Body.(*requestBodyTracker)
			w.WriteHeader(http.StatusOK)
		}))
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if seenTracker {
			t.Fatal("bodyless request must not be wrapped with a tracker")
		}
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rr.Code)
		}
	})
}

func TestCloseUnconsumedRequestOnAbort(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := &readDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Request.ContentLength = 42

	closeUnconsumedRequestOnAbort(c)

	if got := c.Writer.Header().Get("Connection"); got != "close" {
		t.Fatalf("Connection header = %q, want close", got)
	}
	if !recorder.deadlineSet {
		t.Fatal("SetReadDeadline must be called on abort")
	}
}

// readDeadlineRecorder records SetReadDeadline calls made through the
// http.NewResponseController Unwrap chain in tests.
type readDeadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlineSet bool
}

func (r *readDeadlineRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseRecorder
}

func (r *readDeadlineRecorder) SetReadDeadline(time.Time) error {
	r.deadlineSet = true
	return nil
}
