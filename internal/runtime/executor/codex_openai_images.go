package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	codexOpenAIImageSourceFormat = "openai-image"
	codexImagesGenerationsPath   = "/v1/images/generations"
	codexImagesEditsPath         = "/v1/images/edits"
	codexDirectImagesGenerations = "/images/generations"
	codexDirectImagesEdit        = "/images/edits"
	codexGPTImage15Model         = "gpt-image-1.5"
	codexGPTImage25FlareModel    = "gpt-image-2.5-flare"
	codexGPTImage25SunburstModel = "gpt-image-2.5-sunburst"
	codexGPTImage25Model         = "gpt-image-2.5"
	codexOpenAIImagesMainModel   = "gpt-5.4-mini"
)

type codexOpenAIImagePreparedRequest struct {
	Body           []byte
	ResponseFormat string
	StreamPrefix   string
}

type codexImageCallResult struct {
	Result        string
	RevisedPrompt string
	OutputFormat  string
	Size          string
	Background    string
	Quality       string
}

func isCodexOpenAIImageRequest(opts cliproxyexecutor.Options) bool {
	if !strings.EqualFold(strings.TrimSpace(opts.SourceFormat.String()), codexOpenAIImageSourceFormat) {
		return false
	}
	return codexIsImagesEndpointPath(helps.PayloadRequestPath(opts))
}

func codexIsImagesEndpointPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == codexImagesGenerationsPath || path == codexImagesEditsPath {
		return true
	}
	return strings.HasSuffix(path, codexImagesGenerationsPath) || strings.HasSuffix(path, codexImagesEditsPath)
}

// resolveGPTImage2BaseModel returns the configured base model for the legacy
// hosted image_generation tool path. The value must start with "gpt-"
// (case-insensitive); anything else falls back to the built-in default.
func (e *CodexExecutor) resolveGPTImage2BaseModel() string {
	if e == nil || e.cfg == nil {
		return codexOpenAIImagesMainModel
	}
	model := strings.TrimSpace(e.cfg.GPTImage2BaseModel)
	if model == "" {
		return codexOpenAIImagesMainModel
	}
	if strings.HasPrefix(strings.ToLower(model), "gpt-") {
		return model
	}
	return codexOpenAIImagesMainModel
}

func (e *CodexExecutor) executeOpenAIImage(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	apiKey, baseURL := codexCreds(auth)
	if directEndpoint := codexDirectOpenAIImageEndpoint(req, opts); directEndpoint != "" {
		return e.executeDirectOpenAIImage(ctx, auth, req, opts, directEndpoint)
	}
	prepared, errPrepare := codexPrepareOpenAIImageRequest(req, opts)
	if errPrepare != nil {
		return resp, errPrepare
	}

	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	mainModel := e.resolveGPTImage2BaseModel()
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), mainModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	body, errBuild := e.prepareCodexOpenAIImageBody(prepared.Body, req, opts, mainModel)
	if errBuild != nil {
		return resp, errBuild
	}

	url := helps.JoinBaseURL(baseURL, "/responses")
	httpReq, body, errCache := e.cacheHelper(ctx, sdktranslator.FromString(codexOpenAIImageSourceFormat), url, req, body)
	if errCache != nil {
		return resp, errCache
	}
	applyCodexHeaders(httpReq, auth, apiKey, true, e.cfg)
	applyModelHeaderOverrides(httpReq.Header, mainModel)
	recordCodexOpenAIImageRequest(ctx, e.cfg, e.Identifier(), auth, url, httpReq.Header.Clone(), body)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return resp, errDo
	}
	defer helps.CloseResponseBody("codex", httpResp.Body)

	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	data, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		return resp, errRead
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = newCodexStatusErr(httpResp.StatusCode, data)
		return resp, err
	}

	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		if !bytes.HasPrefix(line, dataTag) {
			continue
		}
		eventData := bytes.TrimSpace(line[len(dataTag):])
		reporter.ObserveCodexResponseModel(eventData)
		switch gjson.GetBytes(eventData, "type").String() {
		case "response.output_item.done":
			collectCodexOutputItemDone(eventData, outputItemsByIndex, &outputItemsFallback)
		case "response.completed":
			if detail, ok := helps.ParseCodexUsage(eventData); ok {
				reporter.Publish(ctx, detail)
			}
			publishCodexImageToolUsage(ctx, reporter, body, eventData)
			completedData := patchCodexCompletedOutput(eventData, outputItemsByIndex, outputItemsFallback)
			results, createdAt, usageRaw, firstMeta, errExtract := codexExtractImagesFromResponsesCompleted(completedData)
			if errExtract != nil {
				return resp, errExtract
			}
			if len(results) == 0 {
				return resp, statusErr{code: http.StatusBadGateway, msg: "upstream did not return image output"}
			}
			out, errOutput := codexBuildImagesAPIResponse(results, createdAt, usageRaw, firstMeta, prepared.ResponseFormat)
			if errOutput != nil {
				return resp, errOutput
			}
			return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
		}
	}

	err = statusErr{code: http.StatusGatewayTimeout, msg: "stream error: stream disconnected before completion"}
	return resp, err
}

func (e *CodexExecutor) executeOpenAIImageStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	apiKey, baseURL := codexCreds(auth)
	if directEndpoint := codexDirectOpenAIImageEndpoint(req, opts); directEndpoint != "" {
		return e.executeDirectOpenAIImageStream(ctx, auth, req, opts, directEndpoint)
	}
	prepared, errPrepare := codexPrepareOpenAIImageRequest(req, opts)
	if errPrepare != nil {
		return nil, errPrepare
	}

	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	mainModel := e.resolveGPTImage2BaseModel()
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), mainModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	body, errBuild := e.prepareCodexOpenAIImageBody(prepared.Body, req, opts, mainModel)
	if errBuild != nil {
		return nil, errBuild
	}

	url := helps.JoinBaseURL(baseURL, "/responses")
	httpReq, body, errCache := e.cacheHelper(ctx, sdktranslator.FromString(codexOpenAIImageSourceFormat), url, req, body)
	if errCache != nil {
		return nil, errCache
	}
	applyCodexHeaders(httpReq, auth, apiKey, true, e.cfg)
	applyModelHeaderOverrides(httpReq.Header, mainModel)
	recordCodexOpenAIImageRequest(ctx, e.cfg, e.Identifier(), auth, url, httpReq.Header.Clone(), body)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return nil, errDo
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, errRead := io.ReadAll(httpResp.Body)
		helps.CloseResponseBody("codex", httpResp.Body)
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			return nil, errRead
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = newCodexStatusErr(httpResp.StatusCode, data)
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk, 16)
	go func() {
		defer close(out)
		defer helps.CloseResponseBody("codex", httpResp.Body)

		sendPayload := func(payload []byte) bool {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: payload}:
				return true
			case <-ctx.Done():
				return false
			}
		}
		sendError := func(errSend error) bool {
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errSend}:
				return true
			case <-ctx.Done():
				return false
			}
		}

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), streamScannerBuffer)
		outputItemsByIndex := make(map[int64][]byte)
		var outputItemsFallback [][]byte
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			if !bytes.HasPrefix(line, dataTag) {
				continue
			}
			eventData := bytes.TrimSpace(line[len(dataTag):])
			reporter.ObserveCodexResponseModel(eventData)
			switch gjson.GetBytes(eventData, "type").String() {
			case "response.output_item.done":
				collectCodexOutputItemDone(eventData, outputItemsByIndex, &outputItemsFallback)
			case "response.image_generation_call.partial_image":
				frame := codexBuildImagePartialFrame(eventData, prepared.ResponseFormat, prepared.StreamPrefix)
				if len(frame) > 0 && !sendPayload(frame) {
					return
				}
			case "response.completed":
				if detail, ok := helps.ParseCodexUsage(eventData); ok {
					reporter.Publish(ctx, detail)
				}
				publishCodexImageToolUsage(ctx, reporter, body, eventData)
				completedData := patchCodexCompletedOutput(eventData, outputItemsByIndex, outputItemsFallback)
				results, _, usageRaw, _, errExtract := codexExtractImagesFromResponsesCompleted(completedData)
				if errExtract != nil {
					sendError(errExtract)
					return
				}
				if len(results) == 0 {
					sendError(statusErr{code: http.StatusBadGateway, msg: "upstream did not return image output"})
					return
				}
				for _, img := range results {
					frame := codexBuildImageCompletedFrame(img, usageRaw, prepared.ResponseFormat, prepared.StreamPrefix)
					if len(frame) > 0 && !sendPayload(frame) {
						return
					}
				}
				return
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx, errScan)
			sendError(errScan)
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *CodexExecutor) prepareCodexOpenAIImageBody(body []byte, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, mainModel string) ([]byte, error) {
	out := body
	mainModel = strings.TrimSpace(mainModel)
	if mainModel == "" {
		mainModel = codexOpenAIImagesMainModel
	}
	var errThinking error
	out, errThinking = thinking.ApplyThinking(out, mainModel, codexOpenAIImageSourceFormat, "codex", e.Identifier())
	if errThinking != nil {
		return nil, errThinking
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	out = helps.ApplyPayloadConfigWithRequestForExecutor(e.cfg, e.Identifier(), mainModel, "codex", codexOpenAIImageSourceFormat, "", out, body, requestedModel, requestPath, opts.Headers)
	out, _ = sjson.SetBytes(out, "model", mainModel)
	out, _ = sjson.SetBytes(out, "stream", true)
	out, _ = sjson.DeleteBytes(out, "previous_response_id")
	out, _ = sjson.DeleteBytes(out, "prompt_cache_retention")
	out, _ = sjson.DeleteBytes(out, "safety_identifier")
	out, _ = sjson.DeleteBytes(out, "stream_options")
	return normalizeCodexInstructions(out), nil
}

func recordCodexOpenAIImageRequest(ctx context.Context, cfg *config.Config, provider string, auth *cliproxyauth.Auth, url string, headers http.Header, body []byte) {
	helps.RecordUpstreamRequest(ctx, cfg, auth, provider, http.MethodPost, url, headers, body)
}

func codexPrepareOpenAIImageRequest(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (codexOpenAIImagePreparedRequest, error) {
	path := helps.PayloadRequestPath(opts)
	if strings.HasSuffix(path, codexImagesGenerationsPath) {
		return codexPrepareOpenAIImageGenerationJSON(req.Payload, req.Model)
	}
	if !strings.HasSuffix(path, codexImagesEditsPath) {
		return codexOpenAIImagePreparedRequest{}, fmt.Errorf("unsupported OpenAI image endpoint path %q", path)
	}

	contentType := codexImageContentType(opts.Headers)
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
		return codexPrepareOpenAIImageEditMultipart(req.Payload, req.Model, contentType)
	}
	return codexPrepareOpenAIImageEditJSON(req.Payload, req.Model)
}

func codexPrepareOpenAIImageGenerationJSON(rawJSON []byte, routeModel string) (codexOpenAIImagePreparedRequest, error) {
	if !json.Valid(rawJSON) {
		return codexOpenAIImagePreparedRequest{}, fmt.Errorf("invalid OpenAI image generation request JSON")
	}
	prompt := strings.TrimSpace(gjson.GetBytes(rawJSON, "prompt").String())
	tool := codexBuildOpenAIImageTool(rawJSON, routeModel, "generate", []string{"size", "quality", "background", "output_format", "moderation"}, []string{"output_compression", "partial_images"})
	body := codexBuildImagesResponsesRequest(prompt, nil, tool)
	return codexOpenAIImagePreparedRequest{
		Body:           body,
		ResponseFormat: codexOpenAIImageResponseFormatFromJSON(rawJSON),
		StreamPrefix:   "image_generation",
	}, nil
}

func codexPrepareOpenAIImageEditJSON(rawJSON []byte, routeModel string) (codexOpenAIImagePreparedRequest, error) {
	if !json.Valid(rawJSON) {
		return codexOpenAIImagePreparedRequest{}, fmt.Errorf("invalid OpenAI image edit request JSON")
	}
	prompt := strings.TrimSpace(gjson.GetBytes(rawJSON, "prompt").String())
	images := make([]string, 0)
	if imagesResult := gjson.GetBytes(rawJSON, "images"); imagesResult.IsArray() {
		for _, img := range imagesResult.Array() {
			url := strings.TrimSpace(img.Get("image_url").String())
			if url != "" {
				images = append(images, url)
			}
		}
	}
	tool := codexBuildOpenAIImageTool(rawJSON, routeModel, "edit", []string{"size", "quality", "background", "output_format", "input_fidelity", "moderation"}, []string{"output_compression", "partial_images"})
	if mask := strings.TrimSpace(gjson.GetBytes(rawJSON, "mask.image_url").String()); mask != "" {
		tool, _ = sjson.SetBytes(tool, "input_image_mask.image_url", mask)
	}
	body := codexBuildImagesResponsesRequest(prompt, images, tool)
	return codexOpenAIImagePreparedRequest{
		Body:           body,
		ResponseFormat: codexOpenAIImageResponseFormatFromJSON(rawJSON),
		StreamPrefix:   "image_edit",
	}, nil
}

func codexPrepareOpenAIImageEditMultipart(rawBody []byte, routeModel string, contentType string) (codexOpenAIImagePreparedRequest, error) {
	_, params, errMedia := mime.ParseMediaType(contentType)
	if errMedia != nil {
		return codexOpenAIImagePreparedRequest{}, fmt.Errorf("parse multipart content type failed: %w", errMedia)
	}
	boundary := strings.TrimSpace(params["boundary"])
	if boundary == "" {
		return codexOpenAIImagePreparedRequest{}, fmt.Errorf("multipart boundary is required")
	}
	reader := multipart.NewReader(bytes.NewReader(rawBody), boundary)
	form, errForm := reader.ReadForm(32 << 20)
	if errForm != nil {
		return codexOpenAIImagePreparedRequest{}, fmt.Errorf("parse multipart form failed: %w", errForm)
	}
	defer func() {
		if errRemove := form.RemoveAll(); errRemove != nil {
			log.Errorf("codex openai images: remove multipart temp files error: %v", errRemove)
		}
	}()

	prompt := strings.TrimSpace(codexFormValue(form, "prompt"))
	responseFormat := codexNormalizeImageResponseFormat(codexFormValue(form, "response_format"))
	tool := []byte(`{"type":"image_generation","action":"edit"}`)
	tool, _ = sjson.SetBytes(tool, "model", codexOpenAIImageToolModel(codexFormValue(form, "model"), routeModel))
	for _, field := range []string{"size", "quality", "background", "output_format", "input_fidelity", "moderation"} {
		if value := strings.TrimSpace(codexFormValue(form, field)); value != "" {
			tool, _ = sjson.SetBytes(tool, field, value)
		}
	}
	for _, field := range []string{"output_compression", "partial_images"} {
		if value := strings.TrimSpace(codexFormValue(form, field)); value != "" {
			if parsed, errParse := strconv.ParseInt(value, 10, 64); errParse == nil {
				tool, _ = sjson.SetBytes(tool, field, parsed)
			}
		}
	}

	images := make([]string, 0)
	for _, fh := range codexMultipartImageFiles(form) {
		dataURL, errData := codexMultipartFileToDataURL(fh)
		if errData != nil {
			return codexOpenAIImagePreparedRequest{}, errData
		}
		images = append(images, dataURL)
	}
	if maskFiles := form.File["mask"]; len(maskFiles) > 0 && maskFiles[0] != nil {
		dataURL, errData := codexMultipartFileToDataURL(maskFiles[0])
		if errData != nil {
			return codexOpenAIImagePreparedRequest{}, errData
		}
		tool, _ = sjson.SetBytes(tool, "input_image_mask.image_url", dataURL)
	}

	body := codexBuildImagesResponsesRequest(prompt, images, tool)
	return codexOpenAIImagePreparedRequest{
		Body:           body,
		ResponseFormat: responseFormat,
		StreamPrefix:   "image_edit",
	}, nil
}

func codexImageContentType(headers http.Header) string {
	if headers == nil {
		return ""
	}
	return strings.TrimSpace(headers.Get("Content-Type"))
}

func codexOpenAIImageResponseFormatFromJSON(rawJSON []byte) string {
	return codexNormalizeImageResponseFormat(gjson.GetBytes(rawJSON, "response_format").String())
}

func codexNormalizeImageResponseFormat(responseFormat string) string {
	if strings.EqualFold(strings.TrimSpace(responseFormat), "url") {
		return "url"
	}
	return "b64_json"
}

func codexOpenAIImageToolModel(requestModel string, routeModel string) string {
	model := strings.TrimSpace(requestModel)
	if model == "" {
		model = strings.TrimSpace(routeModel)
	}
	if model == "" {
		model = codexDefaultImageToolModel
	}
	return model
}

func codexBuildOpenAIImageTool(rawJSON []byte, routeModel string, action string, stringFields []string, numberFields []string) []byte {
	tool := []byte(`{"type":"image_generation","action":""}`)
	tool, _ = sjson.SetBytes(tool, "action", action)
	tool, _ = sjson.SetBytes(tool, "model", codexOpenAIImageToolModel(gjson.GetBytes(rawJSON, "model").String(), routeModel))
	for _, field := range stringFields {
		if value := strings.TrimSpace(gjson.GetBytes(rawJSON, field).String()); value != "" {
			tool, _ = sjson.SetBytes(tool, field, value)
		}
	}
	for _, field := range numberFields {
		if value := gjson.GetBytes(rawJSON, field); value.Exists() && value.Type == gjson.Number {
			tool, _ = sjson.SetBytes(tool, field, value.Int())
		}
	}
	return tool
}

func codexBuildImagesResponsesRequest(prompt string, images []string, toolJSON []byte) []byte {
	req := []byte(`{"instructions":"","stream":true,"reasoning":{"effort":"medium","summary":"auto"},"parallel_tool_calls":true,"include":["reasoning.encrypted_content"],"model":"","store":false,"tool_choice":{"type":"image_generation"}}`)
	req, _ = sjson.SetBytes(req, "model", codexOpenAIImagesMainModel)

	input := []byte(`[{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}]`)
	input, _ = sjson.SetBytes(input, "0.content.0.text", prompt)
	contentIndex := 1
	for _, img := range images {
		if strings.TrimSpace(img) == "" {
			continue
		}
		part := []byte(`{"type":"input_image","image_url":""}`)
		part, _ = sjson.SetBytes(part, "image_url", img)
		input, _ = sjson.SetRawBytes(input, fmt.Sprintf("0.content.%d", contentIndex), part)
		contentIndex++
	}
	req, _ = sjson.SetRawBytes(req, "input", input)

	req, _ = sjson.SetRawBytes(req, "tools", []byte(`[]`))
	if len(toolJSON) > 0 && json.Valid(toolJSON) {
		req, _ = sjson.SetRawBytes(req, "tools.-1", toolJSON)
	}
	return req
}

func codexFormValue(form *multipart.Form, key string) string {
	if form == nil || len(form.Value[key]) == 0 {
		return ""
	}
	return strings.TrimSpace(form.Value[key][0])
}

func codexMultipartImageFiles(form *multipart.Form) []*multipart.FileHeader {
	if form == nil {
		return nil
	}
	if files := form.File["image[]"]; len(files) > 0 {
		return files
	}
	return form.File["image"]
}

func codexMultipartFileToDataURL(fileHeader *multipart.FileHeader) (string, error) {
	if fileHeader == nil {
		return "", fmt.Errorf("upload file is nil")
	}
	f, errOpen := fileHeader.Open()
	if errOpen != nil {
		return "", fmt.Errorf("open upload file failed: %w", errOpen)
	}
	defer func() {
		if errClose := f.Close(); errClose != nil {
			log.Errorf("codex openai images: close upload file error: %v", errClose)
		}
	}()

	data, errRead := io.ReadAll(f)
	if errRead != nil {
		return "", fmt.Errorf("read upload file failed: %w", errRead)
	}
	mediaType := strings.TrimSpace(fileHeader.Header.Get("Content-Type"))
	if mediaType == "" {
		mediaType = http.DetectContentType(data)
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func codexExtractImagesFromResponsesCompleted(payload []byte) (results []codexImageCallResult, createdAt int64, usageRaw []byte, firstMeta codexImageCallResult, err error) {
	if gjson.GetBytes(payload, "type").String() != "response.completed" {
		return nil, 0, nil, codexImageCallResult{}, fmt.Errorf("unexpected event type")
	}
	createdAt = gjson.GetBytes(payload, "response.created_at").Int()
	if createdAt <= 0 {
		createdAt = time.Now().Unix()
	}
	output := gjson.GetBytes(payload, "response.output")
	if output.IsArray() {
		for _, item := range output.Array() {
			if item.Get("type").String() != "image_generation_call" {
				continue
			}
			res := strings.TrimSpace(item.Get("result").String())
			if res == "" {
				continue
			}
			entry := codexImageCallResult{
				Result:        res,
				RevisedPrompt: strings.TrimSpace(item.Get("revised_prompt").String()),
				OutputFormat:  strings.TrimSpace(item.Get("output_format").String()),
				Size:          strings.TrimSpace(item.Get("size").String()),
				Background:    strings.TrimSpace(item.Get("background").String()),
				Quality:       strings.TrimSpace(item.Get("quality").String()),
			}
			if len(results) == 0 {
				firstMeta = entry
			}
			results = append(results, entry)
		}
	}
	if usage := gjson.GetBytes(payload, "response.tool_usage.image_gen"); usage.Exists() && usage.IsObject() {
		usageRaw = []byte(usage.Raw)
	}
	return results, createdAt, usageRaw, firstMeta, nil
}

func codexBuildImagesAPIResponse(results []codexImageCallResult, createdAt int64, usageRaw []byte, firstMeta codexImageCallResult, responseFormat string) ([]byte, error) {
	out := []byte(`{"created":0,"data":[]}`)
	out, _ = sjson.SetBytes(out, "created", createdAt)
	responseFormat = codexNormalizeImageResponseFormat(responseFormat)
	for _, img := range results {
		item := []byte(`{}`)
		if responseFormat == "url" {
			item, _ = sjson.SetBytes(item, "url", "data:"+codexMimeTypeFromOutputFormat(img.OutputFormat)+";base64,"+img.Result)
		} else {
			item, _ = sjson.SetBytes(item, "b64_json", img.Result)
		}
		if img.RevisedPrompt != "" {
			item, _ = sjson.SetBytes(item, "revised_prompt", img.RevisedPrompt)
		}
		out, _ = sjson.SetRawBytes(out, "data.-1", item)
	}
	if firstMeta.Background != "" {
		out, _ = sjson.SetBytes(out, "background", firstMeta.Background)
	}
	if firstMeta.OutputFormat != "" {
		out, _ = sjson.SetBytes(out, "output_format", firstMeta.OutputFormat)
	}
	if firstMeta.Quality != "" {
		out, _ = sjson.SetBytes(out, "quality", firstMeta.Quality)
	}
	if firstMeta.Size != "" {
		out, _ = sjson.SetBytes(out, "size", firstMeta.Size)
	}
	if len(usageRaw) > 0 && json.Valid(usageRaw) {
		out, _ = sjson.SetRawBytes(out, "usage", usageRaw)
	}
	return out, nil
}

func codexBuildImagePartialFrame(payload []byte, responseFormat string, streamPrefix string) []byte {
	b64 := strings.TrimSpace(gjson.GetBytes(payload, "partial_image_b64").String())
	if b64 == "" {
		return nil
	}
	outputFormat := strings.TrimSpace(gjson.GetBytes(payload, "output_format").String())
	eventName := strings.TrimSpace(streamPrefix) + ".partial_image"
	data := []byte(`{"type":"","partial_image_index":0}`)
	data, _ = sjson.SetBytes(data, "type", eventName)
	data, _ = sjson.SetBytes(data, "partial_image_index", gjson.GetBytes(payload, "partial_image_index").Int())
	if codexNormalizeImageResponseFormat(responseFormat) == "url" {
		data, _ = sjson.SetBytes(data, "url", "data:"+codexMimeTypeFromOutputFormat(outputFormat)+";base64,"+b64)
	} else {
		data, _ = sjson.SetBytes(data, "b64_json", b64)
	}
	return codexBuildSSEFrame(eventName, data)
}

func codexBuildImageCompletedFrame(img codexImageCallResult, usageRaw []byte, responseFormat string, streamPrefix string) []byte {
	eventName := strings.TrimSpace(streamPrefix) + ".completed"
	data := []byte(`{"type":""}`)
	data, _ = sjson.SetBytes(data, "type", eventName)
	if codexNormalizeImageResponseFormat(responseFormat) == "url" {
		data, _ = sjson.SetBytes(data, "url", "data:"+codexMimeTypeFromOutputFormat(img.OutputFormat)+";base64,"+img.Result)
	} else {
		data, _ = sjson.SetBytes(data, "b64_json", img.Result)
	}
	if len(usageRaw) > 0 && json.Valid(usageRaw) {
		data, _ = sjson.SetRawBytes(data, "usage", usageRaw)
	}
	return codexBuildSSEFrame(eventName, data)
}

func codexBuildSSEFrame(eventName string, data []byte) []byte {
	var buf bytes.Buffer
	if strings.TrimSpace(eventName) != "" {
		buf.WriteString("event: ")
		buf.WriteString(eventName)
		buf.WriteString("\n")
	}
	buf.WriteString("data: ")
	buf.Write(data)
	buf.WriteString("\n\n")
	return buf.Bytes()
}

func codexMimeTypeFromOutputFormat(outputFormat string) string {
	switch strings.ToLower(strings.TrimSpace(outputFormat)) {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	default:
		return "image/png"
	}
}

// codexDirectOpenAIImageEndpoint returns the upstream /images/* endpoint when the
// request targets a direct-capable image model, so it bypasses the legacy hosted
// image_generation tool path (and its mainline host model) entirely.
func codexDirectOpenAIImageEndpoint(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	if codexDirectOpenAIImageRouteModel(req, opts) == "" {
		return ""
	}
	path := helps.PayloadRequestPath(opts)
	if strings.HasSuffix(strings.TrimSpace(path), codexImagesGenerationsPath) {
		return codexDirectImagesGenerations
	}
	if strings.HasSuffix(strings.TrimSpace(path), codexImagesEditsPath) {
		return codexDirectImagesEdit
	}
	return ""
}

func (e *CodexExecutor) prepareDirectOpenAIImageBody(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) ([]byte, string, string, error) {
	model := e.directOpenAIImageModel(auth, req, opts)
	if model == "" {
		return nil, "", "", fmt.Errorf("unsupported direct OpenAI image model %q", req.Model)
	}
	body, contentType, errPrepare := codexPrepareDirectOpenAIImagePayload(req, opts, model, stream)
	if errPrepare != nil {
		return nil, "", "", errPrepare
	}
	return body, contentType, model, nil
}

func codexPrepareDirectOpenAIImagePayload(req cliproxyexecutor.Request, opts cliproxyexecutor.Options, model string, stream bool) ([]byte, string, error) {
	contentType := codexImageContentType(opts.Headers)
	path := strings.TrimSpace(helps.PayloadRequestPath(opts))
	if strings.HasSuffix(path, codexImagesEditsPath) {
		return codexPrepareDirectOpenAIImageEditPayload(req.Payload, model, contentType, stream)
	}
	return prepareOpenAICompatImagesPayload(req.Payload, model, contentType, stream)
}

func codexPrepareDirectOpenAIImageEditPayload(payload []byte, model string, contentType string, stream bool) ([]byte, string, error) {
	if json.Valid(payload) {
		return prepareOpenAICompatImagesPayload(payload, model, contentType, stream)
	}

	mediaType, params, errParse := mime.ParseMediaType(strings.TrimSpace(contentType))
	if errParse != nil || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(mediaType)), "multipart/") {
		return nil, "", fmt.Errorf("unsupported OpenAI image edit Content-Type %q", contentType)
	}
	boundary := strings.TrimSpace(params["boundary"])
	if boundary == "" {
		return nil, "", fmt.Errorf("multipart boundary is missing")
	}
	return codexRewriteOpenAIImageEditMultipartToJSON(payload, model, boundary, stream)
}

func codexRewriteOpenAIImageEditMultipartToJSON(payload []byte, model string, boundary string, stream bool) ([]byte, string, error) {
	reader := multipart.NewReader(bytes.NewReader(payload), boundary)
	form, errRead := reader.ReadForm(openAICompatMultipartMemory)
	if errRead != nil {
		return nil, "", fmt.Errorf("read multipart form failed: %w", errRead)
	}
	defer func() {
		if errRemove := form.RemoveAll(); errRemove != nil {
			log.Errorf("codex openai images: remove multipart form files error: %v", errRemove)
		}
	}()

	out := []byte(`{}`)
	out, _ = sjson.SetBytes(out, "model", model)
	if stream {
		out, _ = sjson.SetBytes(out, "stream", true)
	}

	for key, values := range form.Value {
		key = strings.TrimSpace(key)
		if key == "" || key == "model" || key == "stream" {
			continue
		}
		out = codexSetOpenAIImageEditFormValues(out, key, values)
	}

	if maskFiles := form.File["mask"]; len(maskFiles) > 0 && maskFiles[0] != nil {
		dataURL, errData := codexMultipartFileToDataURL(maskFiles[0])
		if errData != nil {
			return nil, "", errData
		}
		out, _ = sjson.SetBytes(out, "mask.image_url", dataURL)
	}

	imageFiles := codexMultipartImageFiles(form)
	if existingImages := gjson.GetBytes(out, "images"); !existingImages.Exists() || existingImages.IsArray() {
		existingItems := existingImages.Array()
		imageItems := make([][]byte, 0, len(existingItems)+len(imageFiles))
		for _, image := range existingItems {
			imageItems = append(imageItems, []byte(image.Raw))
		}
		for _, fileHeader := range imageFiles {
			dataURL, errData := codexMultipartFileToDataURL(fileHeader)
			if errData != nil {
				return nil, "", errData
			}
			item := []byte(`{"image_url":""}`)
			item, _ = sjson.SetBytes(item, "image_url", dataURL)
			imageItems = append(imageItems, item)
		}
		if len(imageFiles) > 0 {
			out, _ = sjson.SetRawBytes(out, "images", helps.JoinRawJSONArray(imageItems))
		}
	} else {
		for _, fileHeader := range imageFiles {
			dataURL, errData := codexMultipartFileToDataURL(fileHeader)
			if errData != nil {
				return nil, "", errData
			}
			out, _ = sjson.SetBytes(out, "images.-1.image_url", dataURL)
		}
	}

	return out, "application/json", nil
}

func codexSetOpenAIImageEditFormValues(out []byte, key string, values []string) []byte {
	if len(values) == 0 {
		return out
	}
	path := codexOpenAIImageEditFormJSONPath(key)
	if path == "" {
		return out
	}
	if len(values) == 1 {
		return codexSetOpenAIImageEditFormValue(out, path, values[0])
	}
	items := make([][]byte, 0, len(values))
	for _, value := range values {
		items = append(items, codexOpenAIImageEditFormJSONValue(key, value))
	}
	out, _ = sjson.SetRawBytes(out, path, helps.JoinRawJSONArray(items))
	return out
}

func codexSetOpenAIImageEditFormValue(out []byte, path string, value string) []byte {
	item := codexOpenAIImageEditFormJSONValue(path, value)
	out, _ = sjson.SetRawBytes(out, path, item)
	return out
}

func codexOpenAIImageEditFormJSONValue(key string, value string) []byte {
	value = strings.TrimSpace(value)
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "n", "output_compression", "partial_images":
		if parsed, errParse := strconv.ParseInt(value, 10, 64); errParse == nil {
			raw, _ := json.Marshal(parsed)
			return raw
		}
	}
	raw, _ := json.Marshal(value)
	return raw
}

func codexOpenAIImageEditFormJSONPath(key string) string {
	key = strings.TrimSpace(key)
	switch key {
	case "mask[file_id]":
		return "mask.file_id"
	case "mask[image_url]":
		return "mask.image_url"
	default:
		return key
	}
}

func codexDirectOpenAIImageRouteModel(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	// Multipart handlers supply the requested model separately from the raw form.
	requestedModel := helps.PayloadRequestedModel(opts, "")
	payloadModel := ""
	if json.Valid(req.Payload) {
		payloadModel = gjson.GetBytes(req.Payload, "model").String()
	}
	for _, model := range []string{requestedModel, payloadModel, req.Model} {
		baseModel := codexOpenAIImageBaseModel(model)
		if codexIsDirectOpenAIImageModel(baseModel) {
			return baseModel
		}
	}
	return ""
}

func (e *CodexExecutor) directOpenAIImageModel(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	routeModel := codexDirectOpenAIImageRouteModel(req, opts)
	if routeModel == "" {
		return ""
	}
	model := strings.TrimSpace(thinking.ParseSuffix(req.Model).ModelName)
	if model == "" {
		return routeModel
	}

	// Explicit upstream names are not routing prefixes, even when their base is builtin.
	if entry := e.resolveCodexConfig(auth); entry != nil {
		for _, configured := range entry.Models {
			name := strings.TrimSpace(configured.Name)
			if name == "" {
				name = strings.TrimSpace(configured.Alias)
			}
			if strings.EqualFold(strings.TrimSpace(thinking.ParseSuffix(name).ModelName), model) {
				return model
			}
		}
	}
	// Preserve legacy builtin route normalization only without an explicit upstream.
	if baseModel := codexOpenAIImageBaseModel(model); codexIsDirectOpenAIImageModel(baseModel) {
		return baseModel
	}
	return model
}

func codexOpenAIImageBaseModel(model string) string {
	model = strings.TrimSpace(thinking.ParseSuffix(model).ModelName)
	if idx := strings.LastIndex(model, "/"); idx >= 0 && idx < len(model)-1 {
		model = strings.TrimSpace(model[idx+1:])
	}
	return strings.ToLower(strings.TrimSpace(model))
}

func codexIsDirectOpenAIImageModel(model string) bool {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case codexGPTImage15Model, codexDefaultImageToolModel, codexGPTImage25FlareModel, codexGPTImage25SunburstModel, codexGPTImage25Model:
		return true
	default:
		return false
	}
}

// executeDirectOpenAIImage proxies the image request straight to the upstream
// /images/* endpoint with the requested image model, without translating it
// into a Responses request driven by a mainline host model.
func (e *CodexExecutor) executeDirectOpenAIImage(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, endpointPath string) (resp cliproxyexecutor.Response, err error) {
	body, contentType, model, errPrepare := e.prepareDirectOpenAIImageBody(auth, req, opts, false)
	if errPrepare != nil {
		return resp, errPrepare
	}

	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), model, auth)
	defer reporter.TrackFailure(ctx, &err)

	url := helps.JoinBaseURL(baseURL, endpointPath)
	httpReq, body, errCache := e.cacheHelper(ctx, sdktranslator.FromString(codexOpenAIImageSourceFormat), url, req, body)
	if errCache != nil {
		return resp, errCache
	}
	if json.Valid(body) {
		requestedModel := helps.PayloadRequestedModel(opts, req.Model)
		requestPath := helps.PayloadRequestPath(opts)
		body = helps.ApplyPayloadConfigWithRequestForExecutor(e.cfg, e.Identifier(), model, "openai", codexOpenAIImageSourceFormat, "", body, opts.OriginalRequest, requestedModel, requestPath, opts.Headers)
		httpReq.Body = io.NopCloser(bytes.NewReader(body))
		httpReq.ContentLength = int64(len(body))
	}
	httpReq.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	httpReq.Header.Del("Content-Encoding")
	applyCodexHeaders(httpReq, auth, apiKey, false, e.cfg)
	applyModelHeaderOverrides(httpReq.Header, model)
	if contentType != "" {
		httpReq.Header.Set("Content-Type", contentType)
	}
	recordCodexOpenAIImageRequest(ctx, e.cfg, e.Identifier(), auth, url, httpReq.Header.Clone(), body)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return resp, errDo
	}
	defer helps.CloseResponseBody("codex", httpResp.Body)

	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	data, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		return resp, errRead
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = newCodexStatusErr(httpResp.StatusCode, data)
		return resp, err
	}

	// Extract usage from response if available, otherwise ensure at least an empty record is published
	detail := helps.ParseOpenAIUsage(data)
	if helps.HasPublishableUsage(detail) {
		reporter.Publish(ctx, detail)
	} else {
		reporter.EnsurePublished(ctx)
	}

	return cliproxyexecutor.Response{Payload: data, Headers: httpResp.Header.Clone()}, nil
}

// executeDirectOpenAIImageStream is the streaming variant of executeDirectOpenAIImage.
func (e *CodexExecutor) executeDirectOpenAIImageStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, endpointPath string) (_ *cliproxyexecutor.StreamResult, err error) {
	body, contentType, model, errPrepare := e.prepareDirectOpenAIImageBody(auth, req, opts, true)
	if errPrepare != nil {
		return nil, errPrepare
	}

	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), model, auth)
	defer reporter.TrackFailure(ctx, &err)

	url := helps.JoinBaseURL(baseURL, endpointPath)
	httpReq, body, errCache := e.cacheHelper(ctx, sdktranslator.FromString(codexOpenAIImageSourceFormat), url, req, body)
	if errCache != nil {
		return nil, errCache
	}
	if json.Valid(body) {
		requestedModel := helps.PayloadRequestedModel(opts, req.Model)
		requestPath := helps.PayloadRequestPath(opts)
		body = helps.ApplyPayloadConfigWithRequestForExecutor(e.cfg, e.Identifier(), model, "openai", codexOpenAIImageSourceFormat, "", body, opts.OriginalRequest, requestedModel, requestPath, opts.Headers)
		httpReq.Body = io.NopCloser(bytes.NewReader(body))
		httpReq.ContentLength = int64(len(body))
	}
	httpReq.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	httpReq.Header.Del("Content-Encoding")
	applyCodexHeaders(httpReq, auth, apiKey, true, e.cfg)
	applyModelHeaderOverrides(httpReq.Header, model)
	if contentType != "" {
		httpReq.Header.Set("Content-Type", contentType)
	}
	recordCodexOpenAIImageRequest(ctx, e.cfg, e.Identifier(), auth, url, httpReq.Header.Clone(), body)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return nil, errDo
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, errRead := io.ReadAll(httpResp.Body)
		helps.CloseResponseBody("codex", httpResp.Body)
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			return nil, errRead
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = newCodexStatusErr(httpResp.StatusCode, data)
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk, 16)
	go func() {
		defer close(out)
		defer helps.CloseResponseBody("codex", httpResp.Body)
		defer reporter.EnsurePublished(ctx)

		sendPayload := func(payload []byte) bool {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: payload}:
				return true
			case <-ctx.Done():
				return false
			}
		}
		sendError := func(errSend error) bool {
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errSend}:
				return true
			case <-ctx.Done():
				return false
			}
		}

		buffer := make([]byte, 32*1024)
		for {
			n, errRead := httpResp.Body.Read(buffer)
			if n > 0 {
				chunk := bytes.Clone(buffer[:n])
				helps.AppendAPIResponseChunk(ctx, e.cfg, chunk)
				if !sendPayload(chunk) {
					return
				}
			}
			if errRead != nil {
				if errRead != io.EOF {
					helps.RecordAPIResponseError(ctx, e.cfg, errRead)
					sendError(errRead)
				}
				return
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}
