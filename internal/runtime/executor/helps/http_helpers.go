package helps

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// JoinBaseURL joins a provider base URL with an endpoint path, trimming a
// single trailing slash from base and ensuring path starts with '/'.
func JoinBaseURL(baseURL, path string) string {
	baseURL = strings.TrimSpace(baseURL)
	path = strings.TrimSpace(path)
	if baseURL == "" {
		return path
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if path == "" {
		return baseURL
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return baseURL + path
}

// AuthLogFields extracts the standard auth identity fields used by upstream
// request logging across executors.
func AuthLogFields(auth *cliproxyauth.Auth) (authID, authLabel, authType, authValue string) {
	if auth == nil {
		return "", "", "", ""
	}
	authID = auth.ID
	authLabel = auth.Label
	authType, authValue = auth.AccountInfo()
	return authID, authLabel, authType, authValue
}

// RecordUpstreamRequest logs an outbound upstream request with auth fields filled
// from auth when present.
func RecordUpstreamRequest(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, provider, method, url string, headers http.Header, body []byte) {
	authID, authLabel, authType, authValue := AuthLogFields(auth)
	RecordAPIRequest(ctx, cfg, UpstreamRequestLog{
		URL:       url,
		Method:    method,
		Headers:   headers,
		Body:      body,
		Provider:  provider,
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
}

// ResponseBodyDecoder optionally transforms an upstream response body before it
// is fully read (for example Content-Encoding decompression).
type ResponseBodyDecoder func(body io.ReadCloser, contentEncoding string) (io.ReadCloser, error)

// ReadHTTPErrorBody records response metadata/chunks for a non-2xx upstream
// response and returns the raw body for status error construction. The response
// body is fully consumed; callers should still close httpResp.Body when they own
// it, unless a decoder already closed the original body on failure.
func ReadHTTPErrorBody(ctx context.Context, cfg *config.Config, httpResp *http.Response) []byte {
	return ReadHTTPErrorBodyWithDecode(ctx, cfg, httpResp, nil)
}

// ReadHTTPErrorBodyWithDecode is like ReadHTTPErrorBody but allows a custom body
// decoder (gzip/br/zstd/magic-byte detection) before reading the payload.
func ReadHTTPErrorBodyWithDecode(ctx context.Context, cfg *config.Config, httpResp *http.Response, decode ResponseBodyDecoder) []byte {
	if httpResp == nil {
		return nil
	}
	RecordAPIResponseMetadata(ctx, cfg, httpResp.StatusCode, httpResp.Header.Clone())

	reader := io.ReadCloser(httpResp.Body)
	if decode != nil {
		decoded, errDecode := decode(httpResp.Body, httpResp.Header.Get("Content-Encoding"))
		if errDecode != nil {
			RecordAPIResponseError(ctx, cfg, errDecode)
			msg := fmt.Sprintf("failed to decode error response body: %v", errDecode)
			LogWithRequestID(ctx).Warn(msg)
			return []byte(msg)
		}
		reader = decoded
		defer CloseResponseBody("", reader)
	}

	body, errRead := io.ReadAll(reader)
	if errRead != nil {
		RecordAPIResponseError(ctx, cfg, errRead)
		msg := fmt.Sprintf("failed to read error response body: %v", errRead)
		LogWithRequestID(ctx).Warn(msg)
		return []byte(msg)
	}
	AppendAPIResponseChunk(ctx, cfg, body)
	LogWithRequestID(ctx).Debugf(
		"request error, error status: %d, error message: %s",
		httpResp.StatusCode,
		SummarizeErrorBody(httpResp.Header.Get("Content-Type"), body),
	)
	return body
}

// CloseResponseBody closes resp.Body with structured error logging.
func CloseResponseBody(provider string, body io.Closer) {
	if body == nil {
		return
	}
	if errClose := body.Close(); errClose != nil {
		if provider == "" {
			log.Errorf("close response body error: %v", errClose)
			return
		}
		log.Errorf("%s executor: close response body error: %v", provider, errClose)
	}
}
