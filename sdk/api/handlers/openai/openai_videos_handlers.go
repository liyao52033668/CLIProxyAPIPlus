package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	videosPath                 = "/v1/videos"
	xaiVideosGenerationsAPI    = "/v1/videos/generations"
	xaiVideosEditsAPI          = "/v1/videos/edits"
	xaiVideosExtensionsAPI     = "/v1/videos/extensions"
	defaultXAIVideosModel      = "grok-imagine-video"
	xaiVideos15Model           = "grok-imagine-video-1.5"
	xaiVideos15PreviewAlias    = "grok-imagine-video-1.5-preview"
	xaiVideosHandlerType       = "openai-video"
	defaultVideosSeconds       = "4"
	defaultVideosSize          = "720x1280"
	defaultVideosResolution    = "720p"
	maxXAIVideoReferences      = 7
	maxVideoAuthBindingEntries = 10000
)

const defaultVideoAuthBindingTTL = 3 * time.Hour

var videoAuthBindings = newVideoAuthBindingStore()

type xaiVideoCreateMetadata struct {
	Model        string
	RoutingModel string
	Prompt       string
	Seconds      string
	Size         string
	CreatedAt    int64
}

type videoAuthBinding struct {
	authID    string
	model     string
	expiresAt time.Time
}

// videoAuthBindingStore pins video IDs to the credential that created them so
// later retrieve/content calls reuse the same upstream credential, with a TTL
// to keep the mapping from growing unbounded.
type videoAuthBindingStore struct {
	mu      sync.RWMutex
	entries map[string]videoAuthBinding
}

func newVideoAuthBindingStore() *videoAuthBindingStore {
	return &videoAuthBindingStore{
		entries: make(map[string]videoAuthBinding),
	}
}

func (s *videoAuthBindingStore) set(videoID string, authID string, ttl time.Duration) {
	s.setWithModel(videoID, authID, "", ttl)
}

func (s *videoAuthBindingStore) setWithModel(videoID string, authID string, model string, ttl time.Duration) {
	if s == nil {
		return
	}
	videoID = strings.TrimSpace(videoID)
	authID = strings.TrimSpace(authID)
	if videoID == "" || authID == "" {
		return
	}
	if ttl <= 0 {
		ttl = defaultVideoAuthBindingTTL
	}
	now := time.Now()
	s.mu.Lock()
	s.cleanupExpiredLocked(now)
	// Enforce max capacity by evicting the oldest entry if needed
	if len(s.entries) >= maxVideoAuthBindingEntries {
		s.evictOldestLocked()
	}
	s.entries[videoID] = videoAuthBinding{
		authID:    authID,
		model:     strings.TrimSpace(model),
		expiresAt: now.Add(ttl),
	}
	s.mu.Unlock()
}

func (s *videoAuthBindingStore) get(videoID string) (string, bool) {
	binding, ok := s.getBinding(videoID)
	if !ok {
		return "", false
	}
	return binding.authID, true
}

func (s *videoAuthBindingStore) getBinding(videoID string) (videoAuthBinding, bool) {
	if s == nil {
		return videoAuthBinding{}, false
	}
	videoID = strings.TrimSpace(videoID)
	if videoID == "" {
		return videoAuthBinding{}, false
	}
	now := time.Now()
	s.mu.RLock()
	entry, ok := s.entries[videoID]
	s.mu.RUnlock()
	if !ok {
		return videoAuthBinding{}, false
	}
	if now.After(entry.expiresAt) {
		s.mu.Lock()
		if current, exists := s.entries[videoID]; exists && now.After(current.expiresAt) {
			delete(s.entries, videoID)
		}
		s.mu.Unlock()
		return videoAuthBinding{}, false
	}
	return entry, true
}

func (s *videoAuthBindingStore) cleanupExpiredLocked(now time.Time) {
	for videoID, entry := range s.entries {
		if now.After(entry.expiresAt) {
			delete(s.entries, videoID)
		}
	}
}

// evictOldestLocked removes the entry with the earliest expiration time.
// Must be called with s.mu held.
func (s *videoAuthBindingStore) evictOldestLocked() {
	var oldestID string
	var oldestTime time.Time
	first := true
	for videoID, entry := range s.entries {
		if first || entry.expiresAt.Before(oldestTime) {
			oldestID = videoID
			oldestTime = entry.expiresAt
			first = false
		}
	}
	if !first {
		delete(s.entries, oldestID)
	}
}

func videosModelBase(model string) string {
	_, baseModel := imagesModelParts(model)
	return strings.ToLower(strings.TrimSpace(baseModel))
}

// routingXAIVideosModel normalizes any xAI video alias to the registered canonical model.
func routingXAIVideosModel(model string) string {
	switch videosModelBase(model) {
	case xaiVideos15Model:
		return xaiVideos15Model
	case xaiVideos15PreviewAlias:
		return xaiVideos15PreviewAlias
	default:
		return defaultXAIVideosModel
	}
}

func (h *OpenAIAPIHandler) videoAuthBindingTTL() time.Duration {
	if h != nil {
		if cfg := h.CurrentConfig(); cfg != nil {
			raw := strings.TrimSpace(cfg.VideoResultAuthCacheTTL)
			if raw != "" {
				if ttl, err := time.ParseDuration(raw); err == nil && ttl > 0 {
					return ttl
				}
			}
		}
	}
	return defaultVideoAuthBindingTTL
}

func videoIDFromPayload(payload []byte) string {
	videoID := strings.TrimSpace(gjson.GetBytes(payload, "request_id").String())
	if videoID == "" {
		videoID = strings.TrimSpace(gjson.GetBytes(payload, "id").String())
	}
	return videoID
}

func (h *OpenAIAPIHandler) bindVideoAuthIDFromPayload(payload []byte, authID string) {
	h.bindVideoAuthIDAndModelFromPayload(payload, authID, strings.TrimSpace(gjson.GetBytes(payload, "model").String()))
}

func (h *OpenAIAPIHandler) bindVideoAuthIDAndModelFromPayload(payload []byte, authID string, model string) {
	videoID := videoIDFromPayload(payload)
	if videoID == "" {
		return
	}
	videoAuthBindings.setWithModel(videoID, authID, routingXAIVideosModel(model), h.videoAuthBindingTTL())
}

func (h *OpenAIAPIHandler) bindVideoAuthID(videoID string, authID string, model string) {
	videoAuthBindings.setWithModel(videoID, authID, routingXAIVideosModel(model), h.videoAuthBindingTTL())
}

func (h *OpenAIAPIHandler) contextWithVideoAuthBinding(ctx context.Context, videoID string) context.Context {
	if authID, ok := videoAuthBindings.get(videoID); ok {
		return handlers.WithPinnedAuthID(ctx, authID)
	}
	return ctx
}

func (h *OpenAIAPIHandler) modelWithVideoAuthBinding(videoID string, fallbackModel string) string {
	if binding, ok := videoAuthBindings.getBinding(videoID); ok {
		if model := strings.TrimSpace(binding.model); model != "" {
			return model
		}
	}
	return fallbackModel
}

func isSupportedVideosModel(model string) bool {
	prefix, baseModel := imagesModelParts(model)
	base := strings.ToLower(strings.TrimSpace(baseModel))
	if base != defaultXAIVideosModel && base != xaiVideos15Model && base != xaiVideos15PreviewAlias {
		return false
	}
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	return prefix == "" || prefix == "xai" || prefix == "x-ai" || prefix == "grok"
}

func rejectUnsupportedVideosModel(c *gin.Context, model string) bool {
	if isSupportedVideosModel(model) {
		return false
	}

	c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
		Error: handlers.ErrorDetail{
			Message: fmt.Sprintf("Model %s is not supported on %s. Use %s.", model, videosPath, defaultXAIVideosModel),
			Type:    "invalid_request_error",
		},
	})
	return true
}

func rejectUnsupportedNativeVideosModel(c *gin.Context, model string) bool {
	if isSupportedVideosModel(model) {
		return false
	}

	c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
		Error: handlers.ErrorDetail{
			Message: fmt.Sprintf("Model %s is not supported on %s, %s, or %s. Use %s.", model, xaiVideosGenerationsAPI, xaiVideosEditsAPI, xaiVideosExtensionsAPI, defaultXAIVideosModel),
			Type:    "invalid_request_error",
		},
	})
	return true
}

func canonicalXAIVideosModel(model string) string {
	base := videosModelBase(model)
	if base == xaiVideos15Model || base == xaiVideos15PreviewAlias {
		return xaiVideos15Model
	}
	return defaultXAIVideosModel
}

func readVideosCreateRequest(c *gin.Context) ([]byte, error) {
	contentType := strings.ToLower(strings.TrimSpace(c.ContentType()))
	switch contentType {
	case "multipart/form-data", "application/x-www-form-urlencoded":
		return videosCreateRequestFromForm(c)
	default:
		rawJSON, err := handlers.ReadRequestBody(c)
		if err != nil {
			return nil, err
		}
		if !json.Valid(rawJSON) {
			return nil, fmt.Errorf("body must be valid JSON")
		}
		return rawJSON, nil
	}
}

func readXAIVideosNativeRequest(c *gin.Context) ([]byte, error) {
	rawJSON, err := handlers.ReadRequestBody(c)
	if err != nil {
		return nil, err
	}
	if !json.Valid(rawJSON) {
		return nil, fmt.Errorf("body must be valid JSON")
	}
	return rawJSON, nil
}

func videosCreateRequestFromForm(c *gin.Context) ([]byte, error) {
	rawJSON := []byte(`{}`)
	for _, field := range []string{"model", "prompt", "seconds", "size", "aspect_ratio", "resolution"} {
		if value := strings.TrimSpace(c.PostForm(field)); value != "" {
			rawJSON, _ = sjson.SetBytes(rawJSON, field, value)
		}
	}
	if value := strings.TrimSpace(firstPostForm(c, "input_reference[image_url]", "input_reference.image_url", "image_url")); value != "" {
		rawJSON, _ = sjson.SetBytes(rawJSON, "input_reference.image_url", value)
	}
	if value := strings.TrimSpace(firstPostForm(c, "input_reference[file_id]", "input_reference.file_id", "file_id")); value != "" {
		rawJSON, _ = sjson.SetBytes(rawJSON, "input_reference.file_id", value)
	}
	if refs := strings.TrimSpace(c.PostForm("reference_image_urls")); refs != "" {
		for _, ref := range strings.Split(refs, ",") {
			if ref = strings.TrimSpace(ref); ref != "" {
				rawJSON, _ = sjson.SetBytes(rawJSON, "reference_image_urls.-1", ref)
			}
		}
	}
	return rawJSON, nil
}

func firstPostForm(c *gin.Context, keys ...string) string {
	for _, key := range keys {
		if value := c.PostForm(key); strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func buildXAIVideosCreateRequest(rawJSON []byte, model string) ([]byte, xaiVideoCreateMetadata, error) {
	prompt := strings.TrimSpace(gjson.GetBytes(rawJSON, "prompt").String())
	if prompt == "" {
		return nil, xaiVideoCreateMetadata{}, fmt.Errorf("prompt is required")
	}

	seconds, duration, err := normalizeXAIVideosSeconds(gjson.GetBytes(rawJSON, "seconds").String())
	if err != nil {
		return nil, xaiVideoCreateMetadata{}, err
	}

	size, aspectRatio, resolution, err := xaiVideosSizeOptions(gjson.GetBytes(rawJSON, "size").String())
	if err != nil {
		return nil, xaiVideoCreateMetadata{}, err
	}
	if value := xaiVideosAspectRatio(gjson.GetBytes(rawJSON, "aspect_ratio").String(), ""); value != "" {
		aspectRatio = value
	}
	if value := xaiVideosResolution(gjson.GetBytes(rawJSON, "resolution").String(), ""); value != "" {
		resolution = value
	}

	imageURL, err := xaiVideosInputImageURL(rawJSON)
	if err != nil {
		return nil, xaiVideoCreateMetadata{}, err
	}
	referenceImages := collectXAIVideoReferenceImages(rawJSON)
	if len(referenceImages) > maxXAIVideoReferences {
		return nil, xaiVideoCreateMetadata{}, fmt.Errorf("reference_images supports at most %d images on xAI", maxXAIVideoReferences)
	}
	if imageURL != "" && len(referenceImages) > 0 {
		return nil, xaiVideoCreateMetadata{}, fmt.Errorf("image and reference_images cannot be combined on xAI")
	}
	if len(referenceImages) > 0 && duration > 10 {
		duration = 10
		seconds = "10"
	}

	canonicalModel := canonicalXAIVideosModel(model)
	req := []byte(`{}`)
	req, _ = sjson.SetBytes(req, "model", canonicalModel)
	req, _ = sjson.SetBytes(req, "prompt", prompt)
	req, _ = sjson.SetRawBytes(req, "duration", []byte(strconv.FormatInt(duration, 10)))
	req, _ = sjson.SetBytes(req, "aspect_ratio", aspectRatio)
	req, _ = sjson.SetBytes(req, "resolution", resolution)
	if imageURL != "" {
		req, _ = sjson.SetBytes(req, "image.url", imageURL)
	}
	for _, image := range referenceImages {
		req, _ = sjson.SetBytes(req, "reference_images.-1.url", image)
	}

	meta := xaiVideoCreateMetadata{
		Model:        canonicalModel,
		RoutingModel: model,
		Prompt:       prompt,
		Seconds:      seconds,
		Size:         size,
		CreatedAt:    time.Now().Unix(),
	}
	return req, meta, nil
}

func normalizeXAIVideosSeconds(raw string) (string, int64, error) {
	seconds := strings.TrimSpace(raw)
	if seconds == "" {
		seconds = defaultVideosSeconds
	}
	duration, err := strconv.ParseInt(seconds, 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("seconds must be an integer")
	}
	if duration < 1 {
		duration = 1
	}
	if duration > 15 {
		duration = 15
	}
	return strconv.FormatInt(duration, 10), duration, nil
}

func xaiVideosSizeOptions(raw string) (size string, aspectRatio string, resolution string, err error) {
	size = strings.TrimSpace(raw)
	if size == "" {
		size = defaultVideosSize
	}
	switch size {
	case "720x1280", "1024x1792":
		return size, "9:16", defaultVideosResolution, nil
	case "1280x720", "1792x1024":
		return size, "16:9", defaultVideosResolution, nil
	default:
		return "", "", "", fmt.Errorf("size must be one of 720x1280, 1280x720, 1024x1792, or 1792x1024")
	}
}

func xaiVideosAspectRatio(raw string, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1:1", "square":
		return "1:1"
	case "16:9", "landscape":
		return "16:9"
	case "9:16", "portrait":
		return "9:16"
	case "4:3":
		return "4:3"
	case "3:4":
		return "3:4"
	case "3:2":
		return "3:2"
	case "2:3":
		return "2:3"
	default:
		return fallback
	}
}

func xaiVideosResolution(raw string, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "480p":
		return "480p"
	case "720p":
		return "720p"
	default:
		return fallback
	}
}

func xaiVideosInputImageURL(rawJSON []byte) (string, error) {
	inputRef := gjson.GetBytes(rawJSON, "input_reference")
	if inputRef.Exists() {
		imageURL := strings.TrimSpace(inputRef.Get("image_url").String())
		fileID := strings.TrimSpace(inputRef.Get("file_id").String())
		if imageURL != "" && fileID != "" {
			return "", fmt.Errorf("input_reference must provide exactly one of image_url or file_id")
		}
		if fileID != "" {
			return "", fmt.Errorf("input_reference.file_id is not supported for xAI video generation; use input_reference.image_url")
		}
		if imageURL != "" {
			return imageURL, nil
		}
	}

	image := gjson.GetBytes(rawJSON, "image")
	if image.Exists() {
		if image.Type == gjson.String {
			return strings.TrimSpace(image.String()), nil
		}
		if value := strings.TrimSpace(image.Get("url").String()); value != "" {
			return value, nil
		}
		if value := strings.TrimSpace(image.Get("image_url.url").String()); value != "" {
			return value, nil
		}
	}

	return strings.TrimSpace(gjson.GetBytes(rawJSON, "image_url").String()), nil
}

func collectXAIVideoReferenceImages(rawJSON []byte) []string {
	out := make([]string, 0)
	appendRef := func(value string) {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	collectArray := func(result gjson.Result) {
		if !result.IsArray() {
			return
		}
		result.ForEach(func(_, item gjson.Result) bool {
			if item.Type == gjson.String {
				appendRef(item.String())
				return true
			}
			if value := item.Get("url").String(); value != "" {
				appendRef(value)
				return true
			}
			if value := item.Get("image_url.url").String(); value != "" {
				appendRef(value)
			}
			return true
		})
	}
	collectArray(gjson.GetBytes(rawJSON, "reference_images"))
	collectArray(gjson.GetBytes(rawJSON, "reference_image_urls"))
	return out
}

func buildVideosCreateAPIResponseFromXAI(payload []byte, meta xaiVideoCreateMetadata) ([]byte, error) {
	requestID := strings.TrimSpace(gjson.GetBytes(payload, "request_id").String())
	if requestID == "" {
		requestID = strings.TrimSpace(gjson.GetBytes(payload, "id").String())
	}
	if requestID == "" {
		return nil, fmt.Errorf("xAI video response did not include request_id")
	}

	out := []byte(`{"object":"video","progress":0,"status":"queued"}`)
	out, _ = sjson.SetBytes(out, "id", requestID)
	out, _ = sjson.SetBytes(out, "model", meta.Model)
	out, _ = sjson.SetBytes(out, "prompt", meta.Prompt)
	out, _ = sjson.SetBytes(out, "seconds", meta.Seconds)
	out, _ = sjson.SetBytes(out, "size", meta.Size)
	out, _ = sjson.SetBytes(out, "created_at", meta.CreatedAt)
	if status := openAIVideoStatus(gjson.GetBytes(payload, "status").String()); status != "" {
		out, _ = sjson.SetBytes(out, "status", status)
	}
	if progress := gjson.GetBytes(payload, "progress"); progress.Exists() {
		out, _ = sjson.SetRawBytes(out, "progress", []byte(progress.Raw))
	}
	return out, nil
}

func buildVideosRetrieveAPIResponseFromXAI(videoID string, payload []byte, fallbackModel string) ([]byte, error) {
	out := []byte(`{"object":"video"}`)
	out, _ = sjson.SetBytes(out, "id", videoID)

	model := strings.TrimSpace(gjson.GetBytes(payload, "model").String())
	if model == "" {
		model = fallbackModel
	}
	out, _ = sjson.SetBytes(out, "model", model)

	if status := openAIVideoStatus(gjson.GetBytes(payload, "status").String()); status != "" {
		out, _ = sjson.SetBytes(out, "status", status)
	}
	if progress := gjson.GetBytes(payload, "progress"); progress.Exists() {
		out, _ = sjson.SetRawBytes(out, "progress", []byte(progress.Raw))
	}
	if duration := gjson.GetBytes(payload, "video.duration"); duration.Exists() {
		out, _ = sjson.SetBytes(out, "seconds", duration.String())
	}
	if video := gjson.GetBytes(payload, "video"); video.Exists() && json.Valid([]byte(video.Raw)) {
		out, _ = sjson.SetRawBytes(out, "video", []byte(video.Raw))
	}
	if usage := gjson.GetBytes(payload, "usage"); usage.Exists() && json.Valid([]byte(usage.Raw)) {
		out, _ = sjson.SetRawBytes(out, "usage", []byte(usage.Raw))
	}
	if errPayload := gjson.GetBytes(payload, "error"); errPayload.Exists() && json.Valid([]byte(errPayload.Raw)) {
		out, _ = sjson.SetRawBytes(out, "error", []byte(errPayload.Raw))
	}
	return out, nil
}

func openAIVideoStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "queued", "pending":
		return "queued"
	case "in_progress", "processing", "running":
		return "in_progress"
	case "completed", "done", "succeeded", "success":
		return "completed"
	case "failed", "error", "expired", "cancelled", "canceled":
		return "failed"
	default:
		return ""
	}
}

func (h *OpenAIAPIHandler) VideosCreate(c *gin.Context) {
	rawJSON, err := readVideosCreateRequest(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	videoModel := strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String())
	if videoModel == "" {
		videoModel = defaultXAIVideosModel
	}
	if rejectUnsupportedVideosModel(c, videoModel) {
		return
	}

	xaiReq, meta, err := buildXAIVideosCreateRequest(rawJSON, videoModel)
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	h.collectXAIVideosCreate(c, xaiReq, meta)
}

func (h *OpenAIAPIHandler) XAIVideosGenerations(c *gin.Context) {
	h.handleXAIVideosNativePost(c)
}

func (h *OpenAIAPIHandler) XAIVideosEdits(c *gin.Context) {
	h.handleXAIVideosNativePost(c)
}

func (h *OpenAIAPIHandler) XAIVideosExtensions(c *gin.Context) {
	h.handleXAIVideosNativePost(c)
}

func (h *OpenAIAPIHandler) handleXAIVideosNativePost(c *gin.Context) {
	rawJSON, err := readXAIVideosNativeRequest(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	videoModel := strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String())
	if videoModel == "" {
		videoModel = defaultXAIVideosModel
	}
	if rejectUnsupportedNativeVideosModel(c, videoModel) {
		return
	}

	h.collectXAIVideosNative(c, rawJSON, videoModel, true)
}

func (h *OpenAIAPIHandler) XAIVideosRetrieve(c *gin.Context) {
	requestID := strings.TrimSpace(c.Param("request_id"))
	if requestID == "" {
		requestID = strings.TrimSpace(c.Param("video_id"))
	}
	if requestID == "" {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Invalid request: request_id is required",
				Type:    "invalid_request_error",
			},
		})
		return
	}

	payload := []byte(`{}`)
	payload, _ = sjson.SetBytes(payload, "request_id", requestID)
	h.collectXAIVideosNative(c, payload, defaultXAIVideosModel, false)
}

func (h *OpenAIAPIHandler) VideosRetrieve(c *gin.Context) {
	videoID := strings.TrimSpace(c.Param("video_id"))
	if videoID == "" {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Invalid request: video_id is required",
				Type:    "invalid_request_error",
			},
		})
		return
	}

	payload := []byte(`{}`)
	payload, _ = sjson.SetBytes(payload, "request_id", videoID)

	c.Header("Content-Type", "application/json")
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	selectedAuthID := ""
	cliCtx = h.contextWithVideoAuthBinding(cliCtx, videoID)
	executionModel := h.modelWithVideoAuthBinding(videoID, defaultXAIVideosModel)
	cliCtx = handlers.WithSelectedAuthIDCallback(cliCtx, func(authID string) {
		selectedAuthID = authID
	})
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, xaiVideosHandlerType, executionModel, payload, "")
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		if errMsg.Error != nil {
			cliCancel(errMsg.Error)
		} else {
			cliCancel(nil)
		}
		return
	}

	out, err := buildVideosRetrieveAPIResponseFromXAI(videoID, resp, defaultXAIVideosModel)
	if err != nil {
		errMsg := &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err}
		h.WriteErrorResponse(c, errMsg)
		cliCancel(err)
		return
	}

	h.bindVideoAuthID(videoID, selectedAuthID, executionModel)
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(out)
	cliCancel(nil)
}

func (h *OpenAIAPIHandler) collectXAIVideosNative(c *gin.Context, rawJSON []byte, model string, bindCreatedVideoAuth bool) {
	c.Header("Content-Type", "application/json")

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	selectedAuthID := ""
	videoID := videoIDFromPayload(rawJSON)
	executionModel := model
	if !bindCreatedVideoAuth {
		cliCtx = h.contextWithVideoAuthBinding(cliCtx, videoID)
		executionModel = h.modelWithVideoAuthBinding(videoID, model)
	}
	cliCtx = handlers.WithSelectedAuthIDCallback(cliCtx, func(authID string) {
		selectedAuthID = authID
	})
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, xaiVideosHandlerType, executionModel, rawJSON, "")
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		if errMsg.Error != nil {
			cliCancel(errMsg.Error)
		} else {
			cliCancel(nil)
		}
		return
	}

	if bindCreatedVideoAuth {
		h.bindVideoAuthIDAndModelFromPayload(resp, selectedAuthID, executionModel)
	} else {
		h.bindVideoAuthID(videoID, selectedAuthID, executionModel)
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(resp)
	cliCancel(nil)
}

func (h *OpenAIAPIHandler) collectXAIVideosCreate(c *gin.Context, xaiReq []byte, meta xaiVideoCreateMetadata) {
	c.Header("Content-Type", "application/json")

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	selectedAuthID := ""
	cliCtx = handlers.WithSelectedAuthIDCallback(cliCtx, func(authID string) {
		selectedAuthID = authID
	})
	routingModel := strings.TrimSpace(meta.RoutingModel)
	if routingModel == "" {
		routingModel = routingXAIVideosModel(meta.Model)
	}
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, xaiVideosHandlerType, routingModel, xaiReq, "")
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		if errMsg.Error != nil {
			cliCancel(errMsg.Error)
		} else {
			cliCancel(nil)
		}
		return
	}

	out, err := buildVideosCreateAPIResponseFromXAI(resp, meta)
	if err != nil {
		errMsg := &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err}
		h.WriteErrorResponse(c, errMsg)
		cliCancel(err)
		return
	}

	h.bindVideoAuthIDAndModelFromPayload(out, selectedAuthID, routingModel)
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(out)
	cliCancel(nil)
}
