package helps

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// UpstreamStatusError is the standard non-2xx upstream error returned by DoJSON /
// DoStream helpers. It implements StatusCode() so auth conductor cooldown/retry
// logic can inspect it without knowing the executor package.
type UpstreamStatusError struct {
	Code int
	Msg  string
	// retryAfter is unexported so the RetryAfter() method can implement the
	// auth conductor retryAfterProvider interface without a field/method clash.
	retryAfter *time.Duration
}

func (e UpstreamStatusError) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	return fmt.Sprintf("status %d", e.Code)
}

func (e UpstreamStatusError) StatusCode() int { return e.Code }

// RetryAfter implements the auth conductor retryAfterProvider interface.
func (e UpstreamStatusError) RetryAfter() *time.Duration { return e.retryAfter }

// WithRetryAfter returns a copy of the error with RetryAfter set.
func (e UpstreamStatusError) WithRetryAfter(d *time.Duration) UpstreamStatusError {
	e.retryAfter = d
	return e
}

// UpstreamRequest describes a single outbound provider HTTP call.
type UpstreamRequest struct {
	Provider string
	Auth     *cliproxyauth.Auth
	Method   string
	URL      string
	Headers  http.Header
	Body     []byte
	// Client optional; when nil a proxy-aware client with no request Timeout is used.
	Client *http.Client
	// Decode optional body decoder for error/success bodies (gzip/br/zstd).
	Decode ResponseBodyDecoder
	// SkipRequestLog skips RecordUpstreamRequest when the caller already logged.
	SkipRequestLog bool
}

// DoJSON executes an upstream request, fully reads a successful response body,
// and records the standard request/response log hooks. On non-2xx it returns
// UpstreamStatusError after consuming the error body.
// When req.Decode is set, both error and success bodies are passed through it
// (used for Content-Encoding decompression such as Claude responses).
func DoJSON(ctx context.Context, cfg *config.Config, req UpstreamRequest) (status int, body []byte, headers http.Header, err error) {
	httpResp, errDo := doUpstream(ctx, cfg, req, true)
	if errDo != nil {
		return 0, nil, nil, errDo
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		errBody := ReadHTTPErrorBodyWithDecode(ctx, cfg, httpResp, req.Decode)
		CloseResponseBody(req.Provider, httpResp.Body)
		return httpResp.StatusCode, nil, httpResp.Header.Clone(), UpstreamStatusError{
			Code: httpResp.StatusCode,
			Msg:  string(errBody),
		}
	}

	reader := io.ReadCloser(httpResp.Body)
	if req.Decode != nil {
		decoded, errDecode := req.Decode(httpResp.Body, httpResp.Header.Get("Content-Encoding"))
		if errDecode != nil {
			RecordAPIResponseError(ctx, cfg, errDecode)
			CloseResponseBody(req.Provider, httpResp.Body)
			return httpResp.StatusCode, nil, httpResp.Header.Clone(), errDecode
		}
		reader = decoded
	}
	defer CloseResponseBody(req.Provider, reader)

	data, errRead := io.ReadAll(reader)
	if errRead != nil {
		RecordAPIResponseError(ctx, cfg, errRead)
		return httpResp.StatusCode, nil, httpResp.Header.Clone(), errRead
	}
	AppendAPIResponseChunk(ctx, cfg, data)
	return httpResp.StatusCode, data, httpResp.Header.Clone(), nil
}

// DoStream executes an upstream request intended for streaming. On success the
// caller owns httpResp.Body and must close it. On non-2xx the error body is
// consumed, the response body is closed, and UpstreamStatusError is returned.
// When req.Decode is set on success, httpResp.Body is replaced with the decoded
// reader (caller still closes it).
func DoStream(ctx context.Context, cfg *config.Config, req UpstreamRequest) (*http.Response, error) {
	httpResp, errDo := doUpstream(ctx, cfg, req, true)
	if errDo != nil {
		return nil, errDo
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		errBody := ReadHTTPErrorBodyWithDecode(ctx, cfg, httpResp, req.Decode)
		CloseResponseBody(req.Provider, httpResp.Body)
		return nil, UpstreamStatusError{Code: httpResp.StatusCode, Msg: string(errBody)}
	}
	if req.Decode != nil {
		decoded, errDecode := req.Decode(httpResp.Body, httpResp.Header.Get("Content-Encoding"))
		if errDecode != nil {
			RecordAPIResponseError(ctx, cfg, errDecode)
			CloseResponseBody(req.Provider, httpResp.Body)
			return nil, errDecode
		}
		httpResp.Body = decoded
	}
	return httpResp, nil
}

func doUpstream(ctx context.Context, cfg *config.Config, req UpstreamRequest, logRequest bool) (*http.Response, error) {
	method := req.Method
	if method == "" {
		method = http.MethodPost
	}
	var bodyReader io.Reader
	if len(req.Body) > 0 {
		bodyReader = bytes.NewReader(req.Body)
	}
	httpReq, errNew := http.NewRequestWithContext(ctx, method, req.URL, bodyReader)
	if errNew != nil {
		return nil, errNew
	}
	if req.Headers != nil {
		httpReq.Header = req.Headers.Clone()
	}

	if logRequest && !req.SkipRequestLog {
		RecordUpstreamRequest(ctx, cfg, req.Auth, req.Provider, method, req.URL, httpReq.Header.Clone(), req.Body)
	}

	client := req.Client
	if client == nil {
		client = NewProxyAwareHTTPClient(ctx, cfg, req.Auth, 0)
	}
	httpResp, errDo := client.Do(httpReq)
	if errDo != nil {
		RecordAPIResponseError(ctx, cfg, errDo)
		return nil, errDo
	}
	RecordAPIResponseMetadata(ctx, cfg, httpResp.StatusCode, httpResp.Header.Clone())
	return httpResp, nil
}
