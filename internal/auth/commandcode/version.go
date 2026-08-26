package commandcode

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	// VersionAPI is the official npm registry endpoint for command-code CLI.
	VersionAPI = "https://registry.npmjs.org/command-code/latest"

	// VersionCacheTTL is how long to cache the version before re-fetching.
	VersionCacheTTL = 1 * time.Hour
)

var (
	versionCache     string
	versionCacheTime time.Time
	versionMu        sync.RWMutex
)

// GetCLIVersion returns the latest Command Code CLI version.
// It caches the result for VersionCacheTTL to avoid excessive API calls.
func GetCLIVersion() string {
	versionMu.RLock()
	if versionCache != "" && time.Since(versionCacheTime) < VersionCacheTTL {
		v := versionCache
		versionMu.RUnlock()
		return v
	}
	versionMu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, VersionAPI, nil)
	if err != nil {
		log.Debugf("commandcode: failed to create version request: %v", err)
		return DefaultCLIVersion
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Debugf("commandcode: failed to fetch version: %v", err)
		return DefaultCLIVersion
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		log.Debugf("commandcode: version API returned status %d", resp.StatusCode)
		return DefaultCLIVersion
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Debugf("commandcode: failed to read version response: %v", err)
		return DefaultCLIVersion
	}

	var payload struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		log.Debugf("commandcode: failed to parse version response: %v", err)
		return DefaultCLIVersion
	}

	version := strings.TrimSpace(payload.Version)
	if version == "" {
		log.Debugf("commandcode: empty version in response")
		return DefaultCLIVersion
	}

	versionMu.Lock()
	versionCache = version
	versionCacheTime = time.Now()
	versionMu.Unlock()

	log.Debugf("commandcode: fetched latest version %s", version)
	return version
}
