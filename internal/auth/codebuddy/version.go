package codebuddy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	// VersionAPI is the official CodeBuddy version check endpoint
	VersionAPI = "https://acc-1258344699.cos.accelerate.myqcloud.com/@tencent-ai/codebuddy-code/releases/latest"

	// DefaultIDEVersion is the fallback version if API fetch fails
	DefaultIDEVersion = "2.137.1"

	// VersionCacheTTL is how long to cache the version
	VersionCacheTTL = 1 * time.Hour
)

var (
	versionCache     string
	versionCacheTime time.Time
	versionMu        sync.RWMutex
)

// GetIDEVersion returns the latest CodeBuddy CLI version.
// It caches the result for VersionCacheTTL to avoid excessive API calls.
func GetIDEVersion() string {
	versionMu.RLock()
	if versionCache != "" && time.Since(versionCacheTime) < VersionCacheTTL {
		v := versionCache
		versionMu.RUnlock()
		return v
	}
	versionMu.RUnlock()

	// Fetch new version
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, VersionAPI, nil)
	if err != nil {
		log.Debugf("codebuddy: failed to create version request: %v", err)
		return DefaultIDEVersion
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Debugf("codebuddy: failed to fetch version: %v", err)
		return DefaultIDEVersion
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Debugf("codebuddy: version API returned status %d", resp.StatusCode)
		return DefaultIDEVersion
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Debugf("codebuddy: failed to read version response: %v", err)
		return DefaultIDEVersion
	}

	version := strings.TrimSpace(string(body))
	if version == "" {
		log.Debugf("codebuddy: empty version response")
		return DefaultIDEVersion
	}

	// Update cache
	versionMu.Lock()
	versionCache = version
	versionCacheTime = time.Now()
	versionMu.Unlock()

	log.Debugf("codebuddy: fetched latest version %s", version)
	return version
}
