// Package watcher watches config/auth files and triggers hot reloads.
// It supports cross-platform fsnotify event handling.
package watcher

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"gopkg.in/yaml.v3"

	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// storePersister captures persistence-capable token store methods used by the watcher.
type storePersister interface {
	PersistConfig(ctx context.Context) error
	PersistAuthFiles(ctx context.Context, message string, paths ...string) error
}

type authDirProvider interface {
	AuthDir() string
}

// Watcher manages file watching for configuration and authentication files
type Watcher struct {
	configPath        string
	authDir           string
	config            *config.Config
	clientsMutex      sync.RWMutex
	configReloadMu    sync.Mutex
	configReloadTimer *time.Timer
	serverUpdateMu    sync.Mutex
	serverUpdateTimer *time.Timer
	serverUpdateLast  time.Time
	serverUpdatePend  bool
	stopped           atomic.Bool
	reloadCallback    func(*config.Config)
	watcher           *fsnotify.Watcher
	lastAuthHashes    map[string]string
	lastAuthContents  map[string]*coreauth.Auth
	fileAuthsByPath   map[string]map[string]*coreauth.Auth
	lastRemoveTimes   map[string]time.Time
	lastConfigHash    string
	authQueue         chan<- AuthUpdate
	currentAuths      map[string]*coreauth.Auth
	runtimeAuths      map[string]*coreauth.Auth
	dispatchMu        sync.Mutex
	dispatchCond      *sync.Cond
	pendingUpdates    map[string]AuthUpdate
	pendingOrder      []string
	dispatchCancel    context.CancelFunc
	storePersister    storePersister
	mirroredAuthDir   string
	oldConfigYaml     []byte
}

// AuthUpdateAction represents the type of change detected in auth sources.
type AuthUpdateAction string

const (
	AuthUpdateActionAdd    AuthUpdateAction = "add"
	AuthUpdateActionModify AuthUpdateAction = "modify"
	AuthUpdateActionDelete AuthUpdateAction = "delete"
)

// AuthUpdate describes an incremental change to auth configuration.
type AuthUpdate struct {
	Action AuthUpdateAction
	ID     string
	Auth   *coreauth.Auth
}

const (
	// replaceCheckDelay is a short delay to allow atomic replace (rename) to settle
	// before deciding whether a Remove event indicates a real deletion.
	replaceCheckDelay        = 50 * time.Millisecond
	configReloadDebounce     = 150 * time.Millisecond
	authRemoveDebounceWindow = 1 * time.Second
	serverUpdateDebounce     = 1 * time.Second
)

// NewWatcher creates a new file watcher instance
func NewWatcher(configPath, authDir string, reloadCallback func(*config.Config)) (*Watcher, error) {
	watcher, errNewWatcher := fsnotify.NewWatcher()
	if errNewWatcher != nil {
		return nil, errNewWatcher
	}
	w := &Watcher{
		configPath:      configPath,
		authDir:         authDir,
		reloadCallback:  reloadCallback,
		watcher:         watcher,
		lastAuthHashes:  make(map[string]string),
		fileAuthsByPath: make(map[string]map[string]*coreauth.Auth),
	}
	w.dispatchCond = sync.NewCond(&w.dispatchMu)
	if store := sdkAuth.GetTokenStore(); store != nil {
		if persister, ok := store.(storePersister); ok {
			w.storePersister = persister
			log.Debug("persistence-capable token store detected; watcher will propagate persisted changes")
		}
		if provider, ok := store.(authDirProvider); ok {
			if fixed := strings.TrimSpace(provider.AuthDir()); fixed != "" {
				w.mirroredAuthDir = fixed
				log.Debugf("mirrored auth directory locked to %s", fixed)
			}
		}
	}
	return w, nil
}

// Start begins watching the configuration file and authentication directory
func (w *Watcher) Start(ctx context.Context) error {
	return w.start(ctx)
}

// Stop stops the file watcher
func (w *Watcher) Stop() error {
	w.stopped.Store(true)
	w.stopDispatch()
	w.stopConfigReloadTimer()
	w.stopServerUpdateTimer()
	return w.watcher.Close()
}

// SetConfig updates the current configuration
func (w *Watcher) SetConfig(cfg *config.Config) {
	w.clientsMutex.Lock()
	defer w.clientsMutex.Unlock()
	w.config = cfg
	w.oldConfigYaml, _ = yaml.Marshal(cfg)
}

// SetAuthUpdateQueue sets the queue used to emit auth updates.
func (w *Watcher) SetAuthUpdateQueue(queue chan<- AuthUpdate) {
	w.setAuthUpdateQueue(queue)
}

// DispatchRuntimeAuthUpdate allows external runtime providers (e.g., websocket-driven auths)
// to push auth updates through the same queue used by file/config watchers.
// Returns true if the update was enqueued; false if no queue is configured.
func (w *Watcher) DispatchRuntimeAuthUpdate(update AuthUpdate) bool {
	return w.dispatchRuntimeAuthUpdate(update)
}

// SnapshotCoreAuths converts current clients snapshot into core auth entries.
func (w *Watcher) SnapshotCoreAuths() []*coreauth.Auth {
	w.clientsMutex.RLock()
	cfg := w.config
	w.clientsMutex.RUnlock()
	return snapshotCoreAuths(cfg, w.authDir)
}

// NotifyTokenRefreshed handles token update notifications from the background refresher
// When the background refresher successfully refreshes a token, this method updates the in-memory Auth object
// tokenID: token file name (e.g. kiro-xxx.json)
// accessToken: new access token
// refreshToken: new refresh token
// expiresAt: new expiration time
func (w *Watcher) NotifyTokenRefreshed(tokenID, accessToken, refreshToken, expiresAt string) {
	if w == nil {
		return
	}

	w.clientsMutex.Lock()
	defer w.clientsMutex.Unlock()

	// Iterate currentAuths to find and update the matching Auth
	updated := false
	for id, auth := range w.currentAuths {
		if auth == nil || auth.Metadata == nil {
			continue
		}

		// Check if this is a kiro-type auth
		authType, _ := auth.Metadata["type"].(string)
		if authType != "kiro" {
			continue
		}

		// Multiple matching strategies to handle field differences across auth sources
		matched := false

		// 1. Match by auth.ID (ID may contain the file name)
		if !matched && auth.ID != "" {
			if auth.ID == tokenID || strings.HasSuffix(auth.ID, "/"+tokenID) || strings.HasSuffix(auth.ID, "\\"+tokenID) {
				matched = true
			}
			// ID may be "kiro-xxx" format (no extension), tokenID is "kiro-xxx.json"
			if !matched && strings.TrimSuffix(tokenID, ".json") == auth.ID {
				matched = true
			}
		}

		// 2. Match by auth.Attributes["path"]
		if !matched && auth.Attributes != nil {
			if authPath := auth.Attributes["path"]; authPath != "" {
				// Extract the file name portion for comparison
				pathBase := authPath
				if idx := strings.LastIndexAny(authPath, "/\\"); idx >= 0 {
					pathBase = authPath[idx+1:]
				}
				if pathBase == tokenID || strings.TrimSuffix(pathBase, ".json") == strings.TrimSuffix(tokenID, ".json") {
					matched = true
				}
			}
		}

		// 3. Match by auth.FileName (original logic)
		if !matched && auth.FileName != "" {
			if auth.FileName == tokenID || strings.HasSuffix(auth.FileName, "/"+tokenID) || strings.HasSuffix(auth.FileName, "\\"+tokenID) {
				matched = true
			}
		}

		if matched {
			// Update the in-memory token
			auth.Metadata["access_token"] = accessToken
			auth.Metadata["refresh_token"] = refreshToken
			auth.Metadata["expires_at"] = expiresAt
			auth.Metadata["last_refresh"] = time.Now().Format(time.RFC3339)
			auth.UpdatedAt = time.Now()
			auth.LastRefreshedAt = time.Now()

			log.Infof("watcher: updated in-memory auth for token %s (auth ID: %s)", tokenID, id)
			updated = true

			// Also update the copy in runtimeAuths if present
			if w.runtimeAuths != nil {
				if runtimeAuth, ok := w.runtimeAuths[id]; ok && runtimeAuth != nil {
					if runtimeAuth.Metadata == nil {
						runtimeAuth.Metadata = make(map[string]any)
					}
					runtimeAuth.Metadata["access_token"] = accessToken
					runtimeAuth.Metadata["refresh_token"] = refreshToken
					runtimeAuth.Metadata["expires_at"] = expiresAt
					runtimeAuth.Metadata["last_refresh"] = time.Now().Format(time.RFC3339)
					runtimeAuth.UpdatedAt = time.Now()
					runtimeAuth.LastRefreshedAt = time.Now()
				}
			}

			// Send update notification to authQueue
			if w.authQueue != nil {
				go func(authClone *coreauth.Auth) {
					update := AuthUpdate{
						Action: AuthUpdateActionModify,
						ID:     authClone.ID,
						Auth:   authClone,
					}
					w.dispatchAuthUpdates([]AuthUpdate{update})
				}(auth.Clone())
			}
		}
	}

	if !updated {
		log.Debugf("watcher: no matching auth found for token %s, will be picked up on next file scan", tokenID)
	}
}
