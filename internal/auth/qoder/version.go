package qoder

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
)

// CLIManifestURL is the remote channel manifest published for qodercli releases.
const CLIManifestURL = "https://qoder-ide.oss-accelerate.aliyuncs.com/qodercli/channels/manifest.json"

const (
	cosyVersionCacheTTL  = 6 * time.Hour
	cosyVersionRetryTTL  = 5 * time.Minute
	manifestFetchTimeout = 10 * time.Second
)

var (
	cosyVersionMu       sync.Mutex
	cachedCosyVersion   string
	cosyVersionExpires  time.Time
	cosyVersionFetching atomic.Bool
)

// GetCosyVersion returns the latest qodercli version resolved from the remote
// channel manifest, falling back to the hardcoded CosyVersion constant. The
// remote fetch runs asynchronously so callers never block on network access.
func GetCosyVersion() string {
	cosyVersionMu.Lock()
	version := cachedCosyVersion
	stale := time.Now().After(cosyVersionExpires)
	cosyVersionMu.Unlock()
	if stale && cosyVersionFetching.CompareAndSwap(false, true) {
		go refreshCosyVersion()
	}
	if version == "" {
		return CosyVersion
	}
	return version
}

func refreshCosyVersion() {
	defer cosyVersionFetching.Store(false)
	latest, err := fetchLatestCLIVersion()
	cosyVersionMu.Lock()
	defer cosyVersionMu.Unlock()
	if err != nil {
		cosyVersionExpires = time.Now().Add(cosyVersionRetryTTL)
		log.Debugf("qoder: fetch cli manifest failed, keep current cosy version: %v", err)
		return
	}
	if latest != cachedCosyVersion && latest != CosyVersion {
		log.Infof("qoder: cosy version updated to %q", latest)
	}
	cachedCosyVersion = latest
	cosyVersionExpires = time.Now().Add(cosyVersionCacheTTL)
}

func fetchLatestCLIVersion() (string, error) {
	client := &http.Client{Timeout: manifestFetchTimeout}
	resp, err := client.Get(CLIManifestURL)
	if err != nil {
		return "", err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("qoder: close manifest body error: %v", errClose)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("manifest request failed: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var manifest struct {
		Latest string `json:"latest"`
	}
	if err = json.Unmarshal(data, &manifest); err != nil {
		return "", err
	}
	latest := strings.TrimSpace(manifest.Latest)
	if latest == "" {
		return "", fmt.Errorf("manifest missing latest version")
	}
	return latest, nil
}
