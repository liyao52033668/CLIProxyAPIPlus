package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

func (e *XAIExecutor) executeSpeech(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	model := strings.TrimSpace(gjson.GetBytes(req.Payload, "model").String())
	if model == "" {
		model = strings.TrimSpace(req.Model)
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, model, auth)
	defer reporter.TrackFailure(ctx, &err)

	token, _ := xaiCreds(auth)
	requestURL := xaiSpeechRequestURL(auth)
	logXAIResolvedBaseURL(ctx, strings.TrimSuffix(requestURL, xaiTTSPath))

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	payload := helps.ApplyPayloadConfigWithRequestForExecutor(e.cfg, e.Identifier(), model, xaiSpeechHandlerType, opts.SourceFormat.String(), "", req.Payload, req.Payload, requestedModel, requestPath, opts.Headers)
	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(payload))
	if errReq != nil {
		return resp, errReq
	}
	applyXAIHeaders(httpReq, auth, token, false, "")
	// Official TTS returns raw audio. The media header helper asks for JSON.
	httpReq.Header.Set("Accept", "*/*")
	e.recordXAIRequest(ctx, auth, requestURL, httpReq.Header.Clone(), payload)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return resp, errDo
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("xai executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	data, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		return resp, errRead
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = xaiStatusErr(httpResp.StatusCode, data)
		return resp, err
	}

	reporter.EnsurePublished(ctx)
	return cliproxyexecutor.Response{Payload: data, Headers: httpResp.Header.Clone()}, nil
}
