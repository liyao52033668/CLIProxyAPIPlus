package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	freebuffauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/freebuff"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	// freebuffAdPlacement is the ad placement identifier used by the official
	// desktop client. The upstream ad auction uses this to select which ad to
	// serve.
	freebuffAdPlacement = "Desktop-Below-Chat"

	// freebuffAdTimeout is the maximum time to wait for ad auction and
	// impression confirmation requests.
	freebuffAdTimeout = 10 * time.Second

	// freebuffAdDesktopUA is the User-Agent used for ad requests, matching the
	// official desktop client.
	freebuffAdDesktopUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// freebuffAdRequest is the payload sent to POST /api/v1/ads to trigger an ad
// auction. The upstream returns an ad (if available) which we then "confirm"
// via an impression request to extend free quota.
type freebuffAdRequest struct {
	Messages     []freebuffAdMessage `json:"messages"`
	SessionID    string              `json:"sessionId"`
	Device       freebuffAdDevice    `json:"device"`
	UserAgent    string              `json:"userAgent"`
	PlacementIDs []string            `json:"placementIds"`
	AdSequenceID string              `json:"adSequenceId"`
	Surface      string              `json:"surface"`
}

type freebuffAdMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type freebuffAdDevice struct {
	OS       string `json:"os"`
	Timezone string `json:"timezone"`
	Locale   string `json:"locale"`
}

// freebuffAdResponse is the response from POST /api/v1/ads.
type freebuffAdResponse struct {
	Ads []freebuffAdItem `json:"ads"`
}

type freebuffAdItem struct {
	Title         string   `json:"title"`
	ClickURL      string   `json:"clickUrl"`
	URL           string   `json:"url"`
	ImpressionURL string   `json:"impressionUrl"`
	ImpressionIDs []string `json:"impressionIds"`
}

// freebuffImpressionRequest is the payload sent to POST /api/v1/ads/impression
// to confirm an ad was "viewed".
type freebuffImpressionRequest struct {
	ImpressionURL string `json:"impressionUrl"`
	Mode          string `json:"mode"`
	UserAgent     string `json:"userAgent"`
	OS            string `json:"os"`
}

// freebuffRefreshAds triggers the ad refresh flow to extend free session quota.
// It requests an ad from the upstream auction, then confirms the impression.
// Returns true if the refresh succeeded (ad was served and impression confirmed).
//
// This is a best-effort operation: errors are logged but do not fail the request.
// The caller (heartbeat loop) uses this to extend session lifetime when expiry
// is approaching.
func (e *FreebuffExecutor) freebuffRefreshAds(ctx context.Context, auth *cliproxyauth.Auth) bool {
	token := freebuffAPIKey(auth)
	if token == "" {
		return false
	}

	// Build ad request payload with fake conversation messages.
	adReq := freebuffAdRequest{
		Messages: []freebuffAdMessage{
			{Role: "user", Content: "build me a web app from this prompt: hi"},
			{Role: "user", Content: "continue"},
		},
		SessionID:    "gateway-ad-" + newFreebuffID(),
		Device:       freebuffAdDevice{OS: "windows", Timezone: "Asia/Shanghai", Locale: "zh-CN"},
		UserAgent:    freebuffAdDesktopUA,
		PlacementIDs: []string{freebuffAdPlacement},
		AdSequenceID: "agent:" + strings.ReplaceAll(newFreebuffID(), "-", ""),
		Surface:      "cli_chat",
	}

	adPayload, err := json.Marshal(adReq)
	if err != nil {
		log.Debugf("freebuff ad refresh: marshal request: %v", err)
		return false
	}

	// POST /api/v1/ads
	adURL := e.baseURL(auth) + "/api/v1/ads"
	adCtx, adCancel := context.WithTimeout(ctx, freebuffAdTimeout)
	defer adCancel()

	adHTTPReq, err := http.NewRequestWithContext(adCtx, http.MethodPost, adURL, bytes.NewReader(adPayload))
	if err != nil {
		log.Debugf("freebuff ad refresh: build request: %v", err)
		return false
	}
	adHTTPReq.Header.Set("Authorization", "Bearer "+token)
	adHTTPReq.Header.Set("Content-Type", "application/json")
	adHTTPReq.Header.Set("User-Agent", freebuffAdDesktopUA)

	adResp, err := helps.NewProxyAwareHTTPClient(adCtx, e.cfg, auth, 0).Do(adHTTPReq)
	if err != nil {
		log.Debugf("freebuff ad refresh: POST /api/v1/ads: %v", err)
		return false
	}
	defer adResp.Body.Close()

	if adResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(adResp.Body, 1024))
		log.Debugf("freebuff ad refresh: POST /api/v1/ads status %d: %s", adResp.StatusCode, string(body))
		return false
	}

	var adResult freebuffAdResponse
	if err := json.NewDecoder(adResp.Body).Decode(&adResult); err != nil {
		log.Debugf("freebuff ad refresh: decode response: %v", err)
		return false
	}

	if len(adResult.Ads) == 0 {
		log.Debugf("freebuff ad refresh: no ads returned")
		return false
	}

	// Take the first ad and confirm impression.
	ad := adResult.Ads[0]
	if ad.ImpressionURL == "" {
		log.Debugf("freebuff ad refresh: ad has no impression URL")
		return false
	}

	// Validate impression URL to prevent SSRF.
	if err := freebuffValidateImpressionURL(ad.ImpressionURL); err != nil {
		log.Debugf("freebuff ad refresh: invalid impression URL %q: %v", ad.ImpressionURL, err)
		return false
	}

	// POST /api/v1/ads/impression
	impReq := freebuffImpressionRequest{
		ImpressionURL: ad.ImpressionURL,
		Mode:          "desktop",
		UserAgent:     freebuffAdDesktopUA,
		OS:            "windows",
	}
	impPayload, err := json.Marshal(impReq)
	if err != nil {
		log.Debugf("freebuff ad refresh: marshal impression: %v", err)
		return false
	}

	impURL := e.baseURL(auth) + "/api/v1/ads/impression"
	impCtx, impCancel := context.WithTimeout(ctx, freebuffAdTimeout)
	defer impCancel()

	impHTTPReq, err := http.NewRequestWithContext(impCtx, http.MethodPost, impURL, bytes.NewReader(impPayload))
	if err != nil {
		log.Debugf("freebuff ad refresh: build impression request: %v", err)
		return false
	}
	impHTTPReq.Header.Set("Authorization", "Bearer "+token)
	impHTTPReq.Header.Set("Content-Type", "application/json")
	impHTTPReq.Header.Set("User-Agent", freebuffAdDesktopUA)

	impResp, err := helps.NewProxyAwareHTTPClient(impCtx, e.cfg, auth, 0).Do(impHTTPReq)
	if err != nil {
		log.Debugf("freebuff ad refresh: POST /api/v1/ads/impression: %v", err)
		return false
	}
	defer impResp.Body.Close()

	if impResp.StatusCode != http.StatusOK && impResp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(impResp.Body, 1024))
		log.Debugf("freebuff ad refresh: impression status %d: %s", impResp.StatusCode, string(body))
		return false
	}

	log.Debugf("freebuff ad refresh: success (ad=%q)", ad.Title)
	return true
}

// freebuffValidateImpressionURL checks that the impression URL is safe to
// request. It rejects non-HTTP schemes and private/reserved hosts to prevent
// SSRF via upstream-controlled ad URLs.
func freebuffValidateImpressionURL(rawURL string) error {
	// Delegate to the auth package's SSRF-safe validator which rejects
	// loopback, private, reserved, CGNAT, and multicast addresses.
	_, err := freebuffauth.ValidateLoginBaseURL(rawURL)
	return err
}
