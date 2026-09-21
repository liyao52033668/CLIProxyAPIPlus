package executor

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func (e *XAIExecutor) executeImages(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, endpointPath string) (resp cliproxyexecutor.Response, err error) {
	model := strings.TrimSpace(gjson.GetBytes(req.Payload, "model").String())
	if model == "" {
		model = strings.TrimSpace(req.Model)
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, model, auth)
	defer reporter.TrackFailure(ctx, &err)

	token, _ := xaiCreds(auth)
	baseURL := xaiOfficialOrExplicitBaseURL(auth)
	logXAIResolvedBaseURL(ctx, baseURL)
	if endpointPath == "" {
		endpointPath = xaiDefaultImageEndpointPath
	}

	payload := normalizeXAIImageRefs(req.Payload)
	url := helps.JoinBaseURL(baseURL, endpointPath)
	headers := make(http.Header)
	tmpReq := &http.Request{Header: headers}
	applyXAIHeaders(tmpReq, auth, token, false, "")
	headers = tmpReq.Header
	e.recordXAIRequest(ctx, auth, url, headers.Clone(), payload)

	_, data, respHeaders, errDo := helps.DoJSON(ctx, e.cfg, helps.UpstreamRequest{
		Provider:       e.Identifier(),
		Auth:           auth,
		Method:         http.MethodPost,
		URL:            url,
		Headers:        headers,
		Body:           payload,
		SkipRequestLog: true,
	})
	if errDo != nil {
		if ue, ok := errDo.(helps.UpstreamStatusError); ok {
			helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", ue.Code, helps.SummarizeErrorBody("application/json", []byte(ue.Msg)))
			return resp, xaiStatusErr(ue.Code, []byte(ue.Msg))
		}
		return resp, errDo
	}

	reporter.EnsurePublished(ctx)
	return cliproxyexecutor.Response{Payload: data, Headers: respHeaders}, nil
}

func (e *XAIExecutor) executeVideos(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	token, _ := xaiCreds(auth)
	baseURL := xaiOfficialOrExplicitBaseURL(auth)
	logXAIResolvedBaseURL(ctx, baseURL)

	payload := normalizeXAIImageRefs(req.Payload)
	method := http.MethodPost
	endpointPath := xaiVideosGenerationsPath

	switch path := xaiVideoEndpointPath(opts); path {
	case xaiVideosGenerationsPath, xaiVideosEditsPath, xaiVideosExtensionsPath:
		endpointPath = path
	default:
		if requestID := strings.TrimSpace(gjson.GetBytes(payload, "request_id").String()); requestID != "" {
			method = http.MethodGet
			endpointPath = xaiVideosPath + "/" + url.PathEscape(requestID)
		}
	}
	requestURL := helps.JoinBaseURL(baseURL, endpointPath)
	headers := make(http.Header)
	tmpReq := &http.Request{Header: headers}
	applyXAIHeaders(tmpReq, auth, token, false, "")
	if method == http.MethodPost {
		key := xaiMetadataString(opts.Metadata, xaiIdempotencyKeyMetaKey)
		if key == "" && opts.Headers != nil {
			key = strings.TrimSpace(opts.Headers.Get("x-idempotency-key"))
		}
		if key != "" {
			tmpReq.Header.Set("x-idempotency-key", key)
		}
	}
	headers = tmpReq.Header
	e.recordXAIRequest(ctx, auth, requestURL, headers.Clone(), payload)

	var reqBody []byte
	if method == http.MethodPost {
		reqBody = payload
	}
	_, data, respHeaders, errDo := helps.DoJSON(ctx, e.cfg, helps.UpstreamRequest{
		Provider:       e.Identifier(),
		Auth:           auth,
		Method:         method,
		URL:            requestURL,
		Headers:        headers,
		Body:           reqBody,
		SkipRequestLog: true,
	})
	if errDo != nil {
		if ue, ok := errDo.(helps.UpstreamStatusError); ok {
			helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", ue.Code, helps.SummarizeErrorBody("application/json", []byte(ue.Msg)))
			return resp, xaiStatusErr(ue.Code, []byte(ue.Msg))
		}
		return resp, errDo
	}

	return cliproxyexecutor.Response{Payload: data, Headers: respHeaders}, nil
}
