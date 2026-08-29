package executor

// Cursor transport knobs: environment-tunable protocol settings, upstream
// client-identity routing, proxy resolution, and the pre-warmed H2
// connection pool for the hand-rolled Connect stream.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	cursorproto "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/proto"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Settings are read lazily (first use) so .env values loaded at startup are
// visible. Each returns the effective value for one knob.
var (
	cursorAgentHostSetting   = envString("CURSOR_AGENT_HOST", "api2.cursor.sh")
	cursorClientVersionValue = envString("CURSOR_CLI_VERSION", "cli-2026.02.13-41ac335")
	cursorGhostModeSetting   = envString("CURSOR_GHOST_MODE", "true")
	cursorClientTypeSetting  = envString("CURSOR_CLIENT_TYPE", "cli")
	cursorH2PoolSizeSetting  = envInt("CURSOR_H2_POOL", 2)
	cursorIdleStopSetting    = envSeconds("CURSOR_IDLE_STOP", 180)
	cursorFirstTimeoutValue  = envSeconds("CURSOR_FIRST_TIMEOUT", 90)
	cursorFirstOutputValue   = envSeconds("CURSOR_FIRST_OUTPUT_TIMEOUT", 240)
)

func envString(key, def string) func() string {
	return sync.OnceValue(func() string {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
		return def
	})
}

func envInt(key string, def int) func() int {
	return sync.OnceValue(func() int {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				return n
			}
		}
		return def
	})
}

func envSeconds(key string, def int) func() time.Duration {
	seconds := envInt(key, def)
	return sync.OnceValue(func() time.Duration { return time.Duration(seconds()) * time.Second })
}

// cursorClientTypePrefixes maps model-name prefixes to the upstream
// x-cursor-client-type identity. "cli" draws on the plan's usage pools;
// "sand" is the Grok Bot identity whose weekly pool is metered separately.
// The version header must keep naming a CLI build for both identities.
var cursorClientTypePrefixes = map[string]string{
	"cli/":     "cli",
	"sand/":    "sand",
	"bot/":     "sand",
	"grokbot/": "sand",
}

// splitCursorClientType strips a client-type prefix from the model name:
// "sand/grok-4" -> ("sand", "grok-4"); plain names use the configured default.
func splitCursorClientType(model string) (clientType, modelName string) {
	text := strings.TrimSpace(model)
	lower := strings.ToLower(text)
	for prefix, clientType := range cursorClientTypePrefixes {
		if strings.HasPrefix(lower, prefix) {
			return clientType, strings.TrimSpace(text[len(prefix):])
		}
	}
	return cursorClientTypeSetting(), text
}

// resolveCursorProxy mirrors the executor-wide priority: an auth-level proxy
// overrides the global config proxy.
func resolveCursorProxy(cfg *config.Config, auth *cliproxyauth.Auth) string {
	if auth != nil {
		if p := strings.TrimSpace(auth.ProxyURL); p != "" {
			return p
		}
	}
	if cfg != nil {
		return strings.TrimSpace(cfg.ProxyURL)
	}
	return ""
}

// cursorH2PoolIdle bounds how long a pooled connection may sit unused;
// servers GOAWAY long-idle HTTP/2 connections.
const cursorH2PoolIdle = 35 * time.Second

type cursorH2ConnPool struct {
	mu    sync.Mutex
	conns map[string][]*cursorproto.H2Conn
}

var cursorH2Conns = &cursorH2ConnPool{conns: make(map[string][]*cursorproto.H2Conn)}

func cursorPoolKey(host, proxy string) string { return host + "|" + proxy }

// acquireCursorH2Conn hands out a pre-warmed connection for the host|proxy
// pair, dialing a fresh one when the pool is empty.
func acquireCursorH2Conn(host, proxy string) (*cursorproto.H2Conn, error) {
	key := cursorPoolKey(host, proxy)
	cursorH2Conns.mu.Lock()
	if n := len(cursorH2Conns.conns[key]); n > 0 {
		conn := cursorH2Conns.conns[key][n-1]
		cursorH2Conns.conns[key] = cursorH2Conns.conns[key][:n-1]
		cursorH2Conns.mu.Unlock()
		return conn, nil
	}
	cursorH2Conns.mu.Unlock()
	return cursorproto.DialH2Conn(host, proxy)
}

// prewarmCursorH2Pool prunes idle connections past the idle window and tops
// the default pool up in the background, so a request skips the TLS+HTTP/2
// handshake round trips before its first token.
func (e *CursorExecutor) prewarmCursorH2Pool() {
	size := cursorH2PoolSizeSetting()
	if size <= 0 {
		return
	}
	proxy := resolveCursorProxy(e.cfg, nil)
	key := cursorPoolKey(cursorAgentHostSetting(), proxy)
	for {
		time.Sleep(30 * time.Second)
		cursorH2Conns.mu.Lock()
		kept := cursorH2Conns.conns[key][:0]
		for _, conn := range cursorH2Conns.conns[key] {
			if time.Since(conn.CreatedAt()) < cursorH2PoolIdle {
				kept = append(kept, conn)
			} else {
				conn.Close()
			}
		}
		cursorH2Conns.conns[key] = kept
		idle := len(kept)
		cursorH2Conns.mu.Unlock()
		for idle < size {
			conn, err := cursorproto.DialH2Conn(cursorAgentHostSetting(), proxy)
			if err != nil {
				log.Debugf("cursor: pool prewarm dial failed: %v", err)
				break
			}
			cursorH2Conns.mu.Lock()
			cursorH2Conns.conns[key] = append(cursorH2Conns.conns[key], conn)
			cursorH2Conns.mu.Unlock()
			idle++
		}
	}
}

// cursorLiveness implements the upstream liveness guards for one Run stream:
// a no-response clock (nothing at all arrived), a first-output clock (control
// chatter and heartbeats must not keep a zero-output turn alive), and a
// silence clock (even heartbeats stopped). All are env-tunable; 0 disables.
// Checks run on the frame-processing goroutine only.
type cursorLiveness struct {
	started     time.Time
	lastFrame   time.Time // any upstream frame, heartbeats included
	firstOutput bool      // user-visible output: text/thinking/tool call
}

func newCursorLiveness() *cursorLiveness { return &cursorLiveness{started: time.Now()} }

func (l *cursorLiveness) markFrame() { l.lastFrame = time.Now() }

func (l *cursorLiveness) markOutput() { l.firstOutput = true }

// check validates the guards and returns how long the next wait may last
// (-1 arms no timer). A non-nil error means a guard already fired.
func (l *cursorLiveness) check() (time.Duration, error) {
	now := time.Now()
	idleStop := cursorIdleStopSetting()
	if idleStop > 0 && !l.lastFrame.IsZero() {
		if since := now.Sub(l.lastFrame); since > idleStop {
			return 0, fmt.Errorf("cursor: upstream stream went silent for %s", idleStop)
		}
	}
	if !l.firstOutput {
		if l.lastFrame.IsZero() {
			if ft := cursorFirstTimeoutValue(); ft > 0 {
				if since := now.Sub(l.started); since > ft {
					return 0, fmt.Errorf("cursor: upstream did not respond within %s", ft)
				}
			}
		} else if fot := cursorFirstOutputValue(); fot > 0 {
			if since := now.Sub(l.lastFrame); since > fot {
				return 0, fmt.Errorf("cursor: upstream produced no output for %s", fot)
			}
		}
	}
	next := time.Duration(-1)
	consider := func(d time.Duration) {
		if d <= 0 {
			d = time.Millisecond
		}
		if next < 0 || d < next {
			next = d
		}
	}
	if idleStop > 0 && !l.lastFrame.IsZero() {
		consider(idleStop - now.Sub(l.lastFrame))
	}
	if !l.firstOutput {
		if l.lastFrame.IsZero() {
			if ft := cursorFirstTimeoutValue(); ft > 0 {
				consider(ft - now.Sub(l.started))
			}
		} else if fot := cursorFirstOutputValue(); fot > 0 {
			consider(fot - now.Sub(l.lastFrame))
		}
	}
	return time.Duration(next), nil
}
