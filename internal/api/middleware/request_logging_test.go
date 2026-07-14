package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
)

func TestShouldSkipMethodForRequestLogging(t *testing.T) {
	tests := []struct {
		name string
		req  *http.Request
		skip bool
	}{
		{
			name: "nil request",
			req:  nil,
			skip: true,
		},
		{
			name: "post request should not skip",
			req: &http.Request{
				Method: http.MethodPost,
				URL:    &url.URL{Path: "/v1/responses"},
			},
			skip: false,
		},
		{
			name: "plain get should skip",
			req: &http.Request{
				Method: http.MethodGet,
				URL:    &url.URL{Path: "/v1/models"},
				Header: http.Header{},
			},
			skip: true,
		},
		{
			name: "responses websocket upgrade should not skip",
			req: &http.Request{
				Method: http.MethodGet,
				URL:    &url.URL{Path: "/v1/responses"},
				Header: http.Header{"Upgrade": []string{"websocket"}},
			},
			skip: false,
		},
		{
			name: "codex responses websocket upgrade should not skip",
			req: &http.Request{
				Method: http.MethodGet,
				URL:    &url.URL{Path: "/backend-api/codex/responses"},
				Header: http.Header{"Upgrade": []string{"websocket"}},
			},
			skip: false,
		},
		{
			name: "responses get without upgrade should skip",
			req: &http.Request{
				Method: http.MethodGet,
				URL:    &url.URL{Path: "/v1/responses"},
				Header: http.Header{},
			},
			skip: true,
		},
	}

	for i := range tests {
		got := shouldSkipMethodForRequestLogging(tests[i].req)
		if got != tests[i].skip {
			t.Fatalf("%s: got skip=%t, want %t", tests[i].name, got, tests[i].skip)
		}
	}
}

func TestShouldCaptureRequestBody(t *testing.T) {
	tests := []struct {
		name          string
		loggerEnabled bool
		req           *http.Request
		want          bool
	}{
		{
			name:          "logger enabled still requires known size",
			loggerEnabled: true,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("{}")),
				ContentLength: -1,
				Header:        http.Header{"Content-Type": []string{"application/json"}},
			},
			want: false,
		},
		{
			name:          "logger enabled captures within full cap",
			loggerEnabled: true,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("{}")),
				ContentLength: 2,
				Header:        http.Header{"Content-Type": []string{"application/json"}},
			},
			want: true,
		},
		{
			name:          "logger enabled skips over full cap",
			loggerEnabled: true,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("x")),
				ContentLength: maxFullCapturedRequestBodyBytes + 1,
				Header:        http.Header{"Content-Type": []string{"application/json"}},
			},
			want: false,
		},
		{
			name:          "nil request",
			loggerEnabled: false,
			req:           nil,
			want:          false,
		},
		{
			name:          "small known size json in error-only mode",
			loggerEnabled: false,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("{}")),
				ContentLength: 2,
				Header:        http.Header{"Content-Type": []string{"application/json"}},
			},
			want: true,
		},
		{
			name:          "large known size skipped in error-only mode",
			loggerEnabled: false,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("x")),
				ContentLength: maxErrorOnlyCapturedRequestBodyBytes + 1,
				Header:        http.Header{"Content-Type": []string{"application/json"}},
			},
			want: false,
		},
		{
			name:          "unknown size skipped in error-only mode",
			loggerEnabled: false,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("x")),
				ContentLength: -1,
				Header:        http.Header{"Content-Type": []string{"application/json"}},
			},
			want: false,
		},
		{
			name:          "multipart skipped in error-only mode",
			loggerEnabled: false,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("x")),
				ContentLength: 1,
				Header:        http.Header{"Content-Type": []string{"multipart/form-data; boundary=abc"}},
			},
			want: false,
		},
	}

	for i := range tests {
		got := shouldCaptureRequestBody(tests[i].loggerEnabled, tests[i].req)
		if got != tests[i].want {
			t.Fatalf("%s: got %t, want %t", tests[i].name, got, tests[i].want)
		}
	}
}

func TestCaptureRequestInfoDecodesZstdRequestBodyForLog(t *testing.T) {
	gin.SetMode(gin.TestMode)

	payload := []byte(`{"model":"test-model","stream":true}`)
	var compressed bytes.Buffer
	encoder, errNewWriter := zstd.NewWriter(&compressed)
	if errNewWriter != nil {
		t.Fatalf("zstd.NewWriter: %v", errNewWriter)
	}
	if _, errWrite := encoder.Write(payload); errWrite != nil {
		t.Fatalf("zstd write: %v", errWrite)
	}
	if errClose := encoder.Close(); errClose != nil {
		t.Fatalf("zstd close: %v", errClose)
	}
	compressedBytes := compressed.Bytes()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(compressedBytes))
	req.Header.Set("Content-Encoding", "zstd")
	c.Request = req

	info, errCapture := captureRequestInfo(c, true)
	if errCapture != nil {
		t.Fatalf("captureRequestInfo: %v", errCapture)
	}
	if !bytes.Equal(info.Body, payload) {
		t.Fatalf("logged request body = %q, want %q", string(info.Body), string(payload))
	}

	restoredBody, errRead := io.ReadAll(c.Request.Body)
	if errRead != nil {
		t.Fatalf("read restored request body: %v", errRead)
	}
	if !bytes.Equal(restoredBody, compressedBytes) {
		t.Fatal("request body was not restored with the original compressed bytes")
	}
}

func TestDeferredRequestBodyCaptureLogsLargeErrorBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logsDir := t.TempDir()
	requestLogger := logging.NewFileRequestLogger(false, logsDir, "", 10)
	payload := bytes.Repeat([]byte("x"), int(maxErrorOnlyCapturedRequestBodyBytes)+64)

	router := gin.New()
	router.Use(RequestLoggingMiddleware(requestLogger))
	router.POST("/v1/responses", func(c *gin.Context) {
		body, errRead := io.ReadAll(c.Request.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		if !bytes.Equal(body, payload) {
			t.Fatal("handler received a modified request body")
		}
		c.String(http.StatusBadRequest, "bad request")
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	logBody := readSingleRequestLog(t, logsDir)
	if !bytes.Contains(logBody, payload) {
		t.Fatal("error log does not contain the deferred request body")
	}
	if bytes.Contains(logBody, []byte("REQUEST BODY CAPTURE INCOMPLETE")) {
		t.Fatal("fully consumed request body was marked incomplete")
	}
}

func TestDeferredRequestBodyCaptureHandlesUnderreportedContentLength(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logsDir := t.TempDir()
	requestLogger := logging.NewFileRequestLogger(false, logsDir, "", 10)
	payload := []byte(`{"model":"underreported-content-length"}`)

	router := gin.New()
	router.Use(RequestLoggingMiddleware(requestLogger))
	router.POST("/v1/responses", func(c *gin.Context) {
		body, errRead := io.ReadAll(c.Request.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		if !bytes.Equal(body, payload) {
			t.Fatalf("handler body = %q, want %q", body, payload)
		}
		c.Status(http.StatusBadRequest)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(payload))
	request.ContentLength = 2
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	logBody := readSingleRequestLog(t, logsDir)
	if !bytes.Contains(logBody, payload) {
		t.Fatal("error log does not contain the underreported request body")
	}
}

func TestDeferredRequestBodyCaptureCleansUpSuccessfulRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logsDir := t.TempDir()
	requestLogger := logging.NewFileRequestLogger(false, logsDir, "", 10)
	payload := bytes.Repeat([]byte("x"), int(maxErrorOnlyCapturedRequestBodyBytes)+64)

	router := gin.New()
	router.Use(RequestLoggingMiddleware(requestLogger))
	router.POST("/v1/responses", func(c *gin.Context) {
		if _, errRead := io.Copy(io.Discard, c.Request.Body); errRead != nil {
			t.Fatalf("consume request body: %v", errRead)
		}
		c.Status(http.StatusOK)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	entries, errReadDir := os.ReadDir(logsDir)
	if errReadDir != nil {
		t.Fatalf("read logs directory: %v", errReadDir)
	}
	if len(entries) != 0 {
		t.Fatalf("successful error-only request left %d log artifacts", len(entries))
	}
}

func TestDeferredRequestBodyCaptureDoesNotDrainUnreadBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logsDir := t.TempDir()
	requestLogger := logging.NewFileRequestLogger(false, logsDir, "", 10)
	body := &countingReadCloser{Reader: strings.NewReader("request-body")}

	router := gin.New()
	router.Use(RequestLoggingMiddleware(requestLogger))
	router.POST("/v1/responses", func(c *gin.Context) {
		c.Status(http.StatusBadRequest)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	request.Body = body
	request.ContentLength = maxErrorOnlyCapturedRequestBodyBytes + 1
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	if body.readCalls != 0 {
		t.Fatalf("request body read calls = %d, want 0", body.readCalls)
	}
	logBody := readSingleRequestLog(t, logsDir)
	if !bytes.Contains(logBody, []byte("REQUEST BODY CAPTURE INCOMPLETE")) {
		t.Fatal("unread request body was not marked incomplete")
	}
}

func TestAttachDeferredRequestBodyCaptureSkipsMultipart(t *testing.T) {
	logsDir := t.TempDir()
	requestLogger := logging.NewFileRequestLogger(false, logsDir, "", 10)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("multipart-payload"))
	request.Header.Set("Content-Type", "multipart/form-data; boundary=test")
	requestInfo := &RequestInfo{}

	capture := attachDeferredRequestBodyCapture(request, requestLogger, requestInfo, false, false)
	if capture != nil || requestInfo.deferredBodyCapture != nil {
		t.Fatal("multipart request received deferred body capture")
	}
	entries, errReadDir := os.ReadDir(logsDir)
	if errReadDir != nil {
		t.Fatalf("read logs directory: %v", errReadDir)
	}
	if len(entries) != 0 {
		t.Fatalf("multipart request left %d log artifacts", len(entries))
	}
}

func TestDeferredRequestBodyCaptureCapsWrittenBytes(t *testing.T) {
	source, errSource := logging.NewFileBodySourceInDir(t.TempDir(), "request-body")
	if errSource != nil {
		t.Fatalf("create file body source: %v", errSource)
	}
	defer func() {
		if errCleanup := source.Cleanup(); errCleanup != nil {
			t.Fatalf("cleanup file body source: %v", errCleanup)
		}
	}()
	file, errPart := source.CreatePart("body")
	if errPart != nil {
		t.Fatalf("create body part: %v", errPart)
	}
	capture := &deferredRequestBodyCapture{
		body:          io.NopCloser(strings.NewReader("abcd")),
		file:          file,
		source:        source,
		contentLength: 4,
		bytesCaptured: maxDeferredErrorRequestBodyBytes - 2,
	}
	buffer := make([]byte, 4)
	if _, errRead := io.ReadFull(capture, buffer); errRead != nil {
		t.Fatalf("read capture: %v", errRead)
	}
	body, _, errBytes := capture.Bytes()
	if errBytes != nil {
		t.Fatalf("read captured bytes: %v", errBytes)
	}
	if string(body) != "ab" {
		t.Fatalf("captured body = %q, want %q", body, "ab")
	}
	if !capture.truncated {
		t.Fatal("capture did not mark the body truncated")
	}
}

func TestDecodeCapturedRequestBodyForLogWithLimitMarksTruncation(t *testing.T) {
	var compressed bytes.Buffer
	encoder, errNewWriter := zstd.NewWriter(&compressed)
	if errNewWriter != nil {
		t.Fatalf("zstd.NewWriter: %v", errNewWriter)
	}
	if _, errWrite := encoder.Write([]byte("abcdefgh")); errWrite != nil {
		t.Fatalf("zstd write: %v", errWrite)
	}
	if errClose := encoder.Close(); errClose != nil {
		t.Fatalf("zstd close: %v", errClose)
	}

	got := decodeCapturedRequestBodyForLogWithLimit(compressed.Bytes(), "zstd", 4)
	want := []byte("abcd\n[DECOMPRESSED REQUEST BODY TRUNCATED]")
	if !bytes.Equal(got, want) {
		t.Fatalf("decoded body = %q, want %q", got, want)
	}
}

type countingReadCloser struct {
	io.Reader
	readCalls int
}

func (r *countingReadCloser) Read(payload []byte) (int, error) {
	r.readCalls++
	return r.Reader.Read(payload)
}

func (r *countingReadCloser) Close() error {
	return nil
}

func readSingleRequestLog(t *testing.T, logsDir string) []byte {
	t.Helper()
	entries, errReadDir := os.ReadDir(logsDir)
	if errReadDir != nil {
		t.Fatalf("read logs directory: %v", errReadDir)
	}
	var logPath string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".log") {
			if logPath != "" {
				t.Fatal("found multiple request logs")
			}
			logPath = logsDir + string(os.PathSeparator) + entry.Name()
		}
	}
	if logPath == "" {
		t.Fatal("request log was not written")
	}
	body, errRead := os.ReadFile(logPath)
	if errRead != nil {
		t.Fatalf("read request log: %v", errRead)
	}
	return body
}
