# Security Audit Report — CLIProxyAPIPlus

**Repository**: `/mnt/d/AAWebProjects/15.go/cpa/CLIProxyAPIPlus`  
**Module**: `github.com/router-for-me/CLIProxyAPI/v7`  
**Audit date**: 2026-07-26  
**Scope**: Read-only source audit (no builds/exec of project code; no live credential files)  
**Method**: CodeGraph structural analysis + targeted source review of auth, management API, OAuth, store, Docker/CI, crypto, logging

---

## Executive Summary

This Go proxy concentrates high-value secrets (provider OAuth tokens, API keys, management credentials) and exposes a large management surface plus unauthenticated OAuth helper routes. Several issues are **immediately actionable**:

1. **Unauthenticated Kiro OAuth import / refresh / start** can write credentials into the auth directory and trigger outbound OIDC traffic without management authentication.
2. **Empty `api-keys` disables request authentication** (legacy open access).
3. **`MANAGEMENT_PASSWORD` forces remote management open** even when `allow-remote: false`.
4. **User-controlled Kiro OIDC `region` can break out of `amazonaws.com` host construction** (SSRF-class).
5. **Hardcoded third-party OAuth client secrets** are committed in source.

Management API auth itself is relatively solid when a secret is configured (bcrypt hash, RemoteAddr-based localhost check, failed-attempt ban, no XFF spoof). Auth-file path checks on management download/delete are present. Secret files (`.env`, `config.yaml`, `auths/*`) are gitignored and not tracked.

| Severity | Count |
|----------|------:|
| CRITICAL | 3 |
| HIGH     | 7 |
| MEDIUM   | 9 |
| LOW      | 5 |

---

## Checklist Status

| # | Category | Status |
|---|----------|--------|
| 1 | Secrets & Credentials | **Findings** (hardcoded OAuth secrets; CPA_TOKEN build-time; gitignore OK) |
| 2 | Injection (SQL/cmd/path/XSS/SSRF) | **Findings** (Kiro region SSRF; management APICall open URL; path checks mostly OK; SQL mostly parameterized) |
| 3 | AuthN/AuthZ | **Findings** (open access, unauth OAuth, remote override, WS origin) |
| 4 | Data Exposure | **Findings** (config/API key export via mgmt; query-string keys; request-log bodies) |
| 5 | Dependency security | **Findings** (go-git v6 alpha only flagged; full CVE pass deferred to Dependency agent) |
| 6 | Infra/Config | **Findings** (Docker root; broad port publish; permissive public CORS; missing security headers) |
| 7 | Cryptography | **Findings** (MD5 for protocol signing; weak OAuth state entropy in several providers) |
| 8 | Business Logic | **Findings** (ws-auth toggle; credential import race surface; postgres delete path absolute IDs) |

---

## Findings

### [CRITICAL] Unauthenticated Kiro OAuth routes can import tokens and mutate auth store

- **File**: `internal/auth/kiro/oauth_web.go` (lines 80–90, 752–817, 824+); registered from `internal/api/server.go` (approx. 382–385)
- **Category**: AuthZ / Secrets
- **Description**: `OAuthWebHandler.RegisterRoutes` mounts `/v0/oauth/kiro/*` on the **main Gin engine without** management middleware. `POST /v0/oauth/kiro/import` accepts a refresh token, exchanges it, and `saveTokenToFile` writes a credential JSON under `AuthDir`. `POST /v0/oauth/kiro/refresh` walks the auth directory and refreshes all `kiro-*.json` files. `GET /v0/oauth/kiro/start` starts device/OIDC flows for any remote caller.
- **Evidence**:
```go
oauth := router.Group("/v0/oauth/kiro")
{
    oauth.GET("/start", h.handleStart)
    oauth.POST("/import", h.handleImportToken)
    oauth.POST("/refresh", h.handleManualRefresh)
}
// handleImportToken → socialClient.RefreshSocialToken → h.saveTokenToFile(tokenData)
```
- **Impact**: Any network client that can reach the service port can inject attacker-controlled (or stolen) Kiro credentials into the proxy’s credential pool and/or force bulk refresh of existing Kiro tokens. Combined with empty `api-keys` (open proxy), this enables free use of provider capacity or credential theft via subsequent management/auth-file APIs if those are also reachable.
- **Recommendation**:
  1. Protect all credential-mutating OAuth helper routes with the same management key middleware (or localhost-only + management key).
  2. Remove or gate `POST /import` and `POST /refresh` behind management auth.
  3. Keep pure browser callback endpoints minimal and state-bound; do not expose import/refresh on the public API surface.
- **References**: OWASP A01 Broken Access Control; ASVS V4 Access Control

---

### [CRITICAL] Empty API key list disables authentication (open proxy)

- **File**: `sdk/access/manager.go` (45–52); `internal/api/server.go` (2161–2181); `internal/access/config_access/provider.go` (19–22)
- **Category**: AuthN
- **Description**: When no access providers are registered (including when `api-keys` is empty), `Manager.Authenticate` returns `(nil, nil)`. `AuthMiddleware` treats `err == nil` as success and calls `c.Next()`, documented as “legacy behaviour”.
- **Evidence**:
```go
// sdk/access/manager.go
if len(providers) == 0 {
    return nil, nil
}
// internal/api/server.go
// When no providers are available, it allows all requests (legacy behaviour).
result, err := manager.Authenticate(...)
if err == nil {
    c.Next()
    return
}
```
- **Impact**: Misconfiguration or deliberate empty `api-keys` exposes all provider-backed chat/completions/model routes to the world, consuming upstream OAuth/API credentials stored on the host.
- **Recommendation**: Default-deny: if no providers are configured, return 401/503. Require explicit `allow-unauthenticated: true` (or bind to localhost only) to restore legacy open mode. Add startup warning that refuses to listen on non-loopback without keys unless forced.
- **References**: OWASP A07 Identification and Authentication Failures

---

### [CRITICAL] Unauthenticated Kiro OIDC `region` parameter enables SSRF host breakout

- **File**: `internal/auth/kiro/sso_oidc.go` (98–104); `internal/auth/kiro/oauth_web.go` (254–287)
- **Category**: SSRF
- **Description**: `startIDCAuth` takes `region` from the query string and builds `https://oidc.%s.amazonaws.com` with no allowlist. If `region` contains `/` (or other URL-significant characters), `net/http` parses a different host. Example: `region=evil.com/` → request host `oidc.evil.com` with path `/.amazonaws.com`. The endpoint is reachable without management auth via `/v0/oauth/kiro/start?method=idc&...`.
- **Evidence**:
```go
func getOIDCEndpoint(region string) string {
    return fmt.Sprintf("https://oidc.%s.amazonaws.com", region)
}
// startIDCAuth: region := c.Query("region") → RegisterClientWithRegion / StartDeviceAuthorizationWithIDC
```
- **Impact**: Unauthenticated SSRF: attacker can force the server to POST client registration / device authorization JSON (including dynamically registered client secrets) to attacker-controlled hosts. Can probe internal networks depending on egress policy.
- **Recommendation**: Validate `region` against `^[a-z0-9-]+$` (AWS region pattern). Prefer a fixed allowlist of regions. Never interpolate untrusted input into URL hostnames; use `url.URL{Scheme, Host}` with validated host only.
- **References**: OWASP A10 SSRF; CWE-918

---

### [HIGH] `MANAGEMENT_PASSWORD` forces `allowRemoteOverride=true`

- **File**: `internal/api/handlers/management/handler.go` (75–88, 285–287, 305–307, 352–355)
- **Category**: AuthZ
- **Description**: Setting env `MANAGEMENT_PASSWORD` both supplies the management secret **and** sets `allowRemoteOverride: envSecret != ""`, which forces `allowRemote = true` regardless of `remote-management.allow-remote: false` in config.
- **Evidence**:
```go
allowRemoteOverride: envSecret != "",
// ...
if h.allowRemoteOverride {
    allowRemote = true
}
if !localClient && !allowRemote {
    return false, http.StatusForbidden, "remote management disabled"
}
```
- **Impact**: Operators who set a strong env password expecting localhost-only management (because YAML says `allow-remote: false`) unknowingly expose full management API (config dump, auth-file download, api-call proxy) to the network. Compromise of the password = full control.
- **Recommendation**: Decouple “secret present” from “allow remote”. Respect `allow-remote` always; only env secret should authenticate, not change network policy. Document clearly if remote-by-env is intentional.
- **References**: OWASP A01 Broken Access Control

---

### [HIGH] Management `APICall` is an authenticated open HTTP proxy with credential injection

- **File**: `internal/api/handlers/management/api_tools.go` (118–363, 828–859)
- **Category**: SSRF
- **Description**: `POST /v0/management/api-call` accepts arbitrary absolute `url`, method, headers, and body. It substitutes `$TOKEN$` from selected auth credentials and performs the request server-side. Only scheme/host non-empty is checked; no block of link-local, private, or metadata IPs. Transport may use credential or global proxy.
- **Evidence**:
```go
urlStr := strings.TrimSpace(body.URL)
parsedURL, errParseURL := url.Parse(urlStr)
// only requires Scheme and Host
req, _ := http.NewRequestWithContext(..., method, urlStr, requestBody)
resp, errDo := httpClient.Do(req)
```
- **Impact**: Anyone with a valid management key (or env password with remote override) can pivot into internal networks, cloud metadata (`169.254.169.254`), or exfiltrate provider tokens to attacker URLs via `$TOKEN$` substitution.
- **Recommendation**: URL allowlist / deny private+link-local+metadata ranges; restrict schemes to https; optional per-host allowlist; never send raw tokens to non-provider hosts; require explicit “dangerous” capability flag.
- **References**: OWASP A10 SSRF; CWE-918

---

### [HIGH] Hardcoded OAuth client secrets in repository source

- **File**:
  - `internal/auth/gemini/gemini_auth.go` (31–32) — `ClientSecret = "GOCSPX-…"`
  - `internal/runtime/executor/gemini_cli_executor.go` (~40)
  - `internal/api/handlers/management/api_tools.go` (85–96) — Gemini + Antigravity secrets
  - `internal/auth/antigravity/constants.go` (7)
  - `internal/runtime/executor/antigravity_executor.go` (~53)
  - `internal/auth/iflow/iflow_auth.go` (34) — `defaultIFlowClientSecret`
- **Category**: Secrets
- **Description**: Third-party OAuth client secrets are committed as string constants and duplicated across packages. (Values treated as public-client secrets for desktop-style flows, but still sensitive for abuse/quota and should not be scattered.)
- **Evidence** (redacted):
```go
ClientSecret = "GOCS…Fsxl"          // gemini
antigravityOAuthClientSecret = "GOCS…DAf"
defaultIFlowClientSecret = "4Z3Y…DtW"
```
- **Impact**: Secret rotation is hard; forks/binaries carry the same secrets; attackers can craft standalone OAuth clients impersonating the app; increases abuse surface on Google/iFlow/Antigravity app registrations.
- **Recommendation**: Load from env/config or use public PKCE-only clients where the provider allows; centralize one constant module; rotate provider-side secrets; document which are intentionally “public” desktop secrets.
- **References**: OWASP A02 Cryptographic Failures; CWE-798

---

### [HIGH] WebSocket `CheckOrigin` always true; auth optionally disabled

- **File**: `internal/wsrelay/manager.go` (77–82); `sdk/api/handlers/openai/openai_responses_websocket.go` (46–51); `internal/api/server.go` (806–840)
- **Category**: AuthN / CSRF-adjacent
- **Description**: Both websocket upgraders set `CheckOrigin: func(...) bool { return true }`. `/v1/ws` uses `conditionalAuth` that **skips** `AuthMiddleware` when `wsAuthEnabled` is false. Config field `WebsocketAuth` is a Go `bool` (zero value **false** if omitted from YAML), though `config.example.yaml` sets `ws-auth: true`.
- **Evidence**:
```go
CheckOrigin: func(r *http.Request) bool { return true },
conditionalAuth := func(c *gin.Context) {
    if !s.wsAuthEnabled.Load() { c.Next(); return }
    authMiddleware(c)
}
```
- **Impact**: Cross-origin browser pages can attempt WS upgrades. If `ws-auth` is false/omitted, unauthenticated clients join the relay and may proxy provider traffic depending on session design.
- **Recommendation**: Enforce origin allowlist (or same-origin); default `WebsocketAuth` to true in code, not only example YAML; never skip auth in production builds without explicit opt-in.
- **References**: OWASP ASVS V5; CWE-346 Origin Validation Error

---

### [HIGH] Management endpoints return full secrets after auth (config YAML, API keys, auth files)

- **File**: `internal/api/handlers/management/config_basic.go` (28–34, 200–214); `config_lists.go` (110); `auth_files_io.go` (40–62)
- **Category**: Data Exposure / AuthZ blast radius
- **Description**: Authenticated management callers can `GET /config` (struct dump of provider keys), `GET /config.yaml` (raw file including all secrets), `GET /api-keys` (plaintext list), `GET` auth-file download (full token JSON). `RemoteManagement` is `json:"-"` so JSON config omits management nested object, but **YAML download and API key endpoints do not redact**.
- **Evidence**:
```go
func (h *Handler) GetConfig(c *gin.Context) { c.JSON(200, h.cfg) }
func (h *Handler) GetConfigYAML(c *gin.Context) { _, _ = c.Writer.Write(data) }
func (h *Handler) GetAPIKeys(c *gin.Context) { c.JSON(200, gin.H{"api-keys": h.cfg.APIKeys}) }
```
- **Impact**: Single management-key compromise (or XSS in management UI storing key in `localStorage`) yields complete credential exfiltration.
- **Recommendation**: Redact secrets in `GetConfig`; require separate elevated scope for YAML download / auth-file download; mask API keys in list endpoints (show suffix only); short-lived management tokens; CSRF protection for cookie-based UIs.
- **References**: OWASP A01; ASVS V8 Data Protection

---

### [HIGH] Unauthenticated CodeArts / JoyCode OAuth start + root `/callback`

- **File**: `internal/auth/codearts/oauth_web.go` (72–82, 104–125); `internal/auth/joycode/oauth_web.go` (RegisterRoutes); `internal/api/server.go` (387–401)
- **Category**: AuthZ
- **Description**: Like Kiro, CodeArts and JoyCode register public `/v0/oauth/.../start` routes without management auth. CodeArts also registers **root** `GET /callback`. Successful flows write credentials via onToken hooks.
- **Evidence**:
```go
oauth.GET("/start", h.handleStart)
router.GET("/callback", h.handleCallback) // codearts root callback
```
- **Impact**: Remote attackers can initiate OAuth sessions, consume resources, and—if they complete a login in a phishing scenario—bind victim tokens into the server’s auth store. Root `/callback` increases accidental exposure and routing conflicts.
- **Recommendation**: Gate start endpoints behind management auth or localhost; bind callbacks to dedicated localhost ports only; avoid root-level `/callback` on the main API port when the service is network-exposed.
- **References**: OWASP A01

---

### [HIGH] API keys accepted via URL query (`key`, `auth_token`)

- **File**: `internal/access/config_access/provider.go` (65–85); `sdk/cliproxy/auth/conductor_select.go` (~486–489)
- **Category**: Data Exposure / AuthN
- **Description**: Client authentication accepts credentials from query string in addition to headers. Query strings commonly land in access logs, browser history, Referer headers, and crash dumps. Gin logger masks some query keys via `MaskSensitiveQuery`, but reverse proxies and upstreams may still log them. Amp proxy strips matching client keys from query before upstream—good—but the inbound log risk remains.
- **Evidence**:
```go
queryKey = r.URL.Query().Get("key")
queryAuthToken = r.URL.Query().Get("auth_token")
```
- **Impact**: Credential leakage through logs and intermediary systems; easier CSRF-style abuse in browsers for simple GET-capable endpoints.
- **Recommendation**: Deprecate query auth; prefer headers only; if required for Gemini-compat, auto-disable on non-TLS or log hard warnings; ensure all log pipelines mask `key`/`auth_token`.
- **References**: OWASP Auth Cheat Sheet — “Do not put secrets in URLs”

---

### [MEDIUM] Predictable OAuth `state` for several management OAuth starts

- **File**: `oauth_handlers_gemini.go` (50); `oauth_handlers_iflow.go` (27); `oauth_handlers_github.go` (24); `oauth_handlers_kiro.go` (36); `oauth_handlers_kilo.go` (23); `oauth_handlers_kimi.go` (24); `oauth_handlers_cursor.go` (33)
- **Category**: AuthN / CSRF
- **Description**: Multiple providers use `fmt.Sprintf("xxx-%d", time.Now().UnixNano())` instead of `misc.GenerateRandomState()` (crypto/rand). Nano timestamps are guessable within a window for login CSRF / session fixation style attacks against in-progress flows.
- **Evidence**:
```go
state := fmt.Sprintf("gem-%d", time.Now().UnixNano())
// vs misc.GenerateRandomState() used by claude/codex/gitlab/xai/antigravity
```
- **Impact**: Weak CSRF protection on OAuth binding for those providers when management OAuth is used.
- **Recommendation**: Always use `misc.GenerateRandomState()` (or equivalent CSPRNG) for all providers.
- **References**: RFC 6749 §10.12; OWASP OAuth security

---

### [MEDIUM] Docker image runs as root; compose publishes many OAuth ports

- **File**: `Dockerfile` (full); `docker-compose.yml` (17–24)
- **Category**: Infra
- **Description**: Final image has no `USER` directive (process runs as root). Compose publishes `8317` plus OAuth-related ports `8085`, `1455`, `54545`, `51121`, `51122`, `11451` to the host.
- **Evidence**:
```dockerfile
FROM alpine:3.23
...
CMD ["./CLIProxyAPIPlus"]
```
```yaml
ports:
  - "8317:8317"
  - "8085:8085"
  # ... additional callback ports
```
- **Impact**: Container breakout / misconfig impact amplified; OAuth callback listeners exposed beyond localhost increase attack surface.
- **Recommendation**: Add non-root `USER`; publish only API port by default; bind OAuth callbacks to `127.0.0.1` inside container network.
- **References**: CIS Docker Benchmark; OWASP Docker Security

---

### [MEDIUM] `CPA_TOKEN` baked into binary via Docker build ARG / ldflags

- **File**: `Dockerfile` (18–20); `cmd/server/main.go` (51, 110); used in `config_basic.go` `latestReleaseToken`
- **Category**: Secrets
- **Description**: Build-arg `CPA_TOKEN` is embedded into the binary with `-X 'main.CPAToken=...'`. Build-args can persist in image history; embedded tokens are extractable from the binary.
- **Impact**: Release token leakage from distributed images/binaries; unauthorized access to private release API if token is privileged.
- **Recommendation**: Prefer runtime env only; never ldflag secrets; use BuildKit secret mounts if needed at build time without persistence.
- **References**: CWE-798; Docker Build Secrets

---

### [MEDIUM] Optional TLS verification skip

- **File**: `internal/usage/keeper/cpa/client.go` (96–104); `internal/config/home.go` (`InsecureSkipVerify`); `internal/home/client.go` (~247)
- **Category**: Crypto / MITM
- **Description**: Clients can be constructed with `InsecureSkipVerify: true`, disabling server certificate validation for management/home connections.
- **Impact**: MITM can steal management keys or home JWT material when the option is enabled in production.
- **Recommendation**: Default false; loud warnings; refuse combination with remote management in production profiles.
- **References**: CWE-295

---

### [MEDIUM] Public API CORS is fully permissive

- **File**: `internal/api/server.go` (1700–1723)
- **Category**: Infra / Browser security
- **Description**: Non-management routes set `Access-Control-Allow-Origin: *` and `Access-Control-Allow-Headers: *`. Management CORS is tighter (allowlist / no CORS by default)—good.
- **Impact**: Any website can trigger credentialed-less cross-origin API calls; combined with query-string API keys or browser extensions, increases abuse risk. (Browser will not send custom Authorization from arbitrary sites without preflight success—but `*` still enables simple cross-origin reads of unauthenticated or query-key endpoints.)
- **Recommendation**: Make CORS configurable; default deny or reflect known frontends; never pair `*` with cookie credentials.
- **References**: OWASP HTML5 Security — CORS

---

### [MEDIUM] Request logging can persist full request bodies (including secrets)

- **File**: `internal/api/middleware/request_logging.go`; `internal/logging/request_logger.go`
- **Category**: Data Exposure
- **Description**: When request logging is enabled, bodies up to multi‑MiB are captured. Headers are masked via `MaskSensitiveHeaderValue`, but JSON bodies often contain API keys, tokens, or PII in message content and are written to disk under `logs/`.
- **Impact**: Log volume becomes a second secrets store; backup/leak of logs exposes credentials and user prompts.
- **Recommendation**: Redact known JSON fields (`api_key`, `authorization`, `token`); default request-log off in commercial mode (already partially considered); encrypt log dir; short retention.
- **References**: OWASP A02; GDPR minimization if EU users

---

### [MEDIUM] go-git v6.0.0-alpha.4 dependency

- **File**: `go.mod` (require `github.com/go-git/go-git/v6 v6.0.0-alpha.4`)
- **Category**: Dependency
- **Description**: Git token store uses an **alpha** go-git major line. Alpha releases may lack stability and security hardening; supply-chain risk higher than stable tags.
- **Impact**: Potential unfixed bugs in git protocol/filesystem handling affecting `GitTokenStore` integrity or RCE-class issues if present upstream.
- **Recommendation**: Pin to stable go-git v5 or wait for stable v6; track upstream advisories; sandbox git operations.
- **References**: Dependency agent for full CVE scan

---

### [MEDIUM] Postgres store `resolveDeletePath` accepts absolute / separator-containing IDs

- **File**: `internal/store/postgresstore.go` (661–665)
- **Category**: Path Traversal (defense-in-depth)
- **Description**: `resolveDeletePath` returns absolute paths or paths with separators **as-is**, unlike `absoluteAuthPath` which rejects `..`. Management HTTP delete path uses `isUnsafeAuthFileName` (good), but lower-level store API is weaker if ever called with untrusted IDs.
- **Evidence**:
```go
func (s *PostgresStore) resolveDeletePath(id string) (string, error) {
    if strings.ContainsRune(id, os.PathSeparator) || filepath.IsAbs(id) {
        return id, nil
    }
    return filepath.Join(s.authDir, filepath.FromSlash(id)), nil
}
```
- **Impact**: Future callers or bugs could delete arbitrary files; TOCTOU/IDOR risk in multi-tenant misuses.
- **Recommendation**: Always resolve under `authDir` with `Rel` jail checks; never honor absolute IDs from external input.
- **References**: CWE-22

---

### [MEDIUM] Management panel stores management key in browser storage (static UI)

- **File**: `static/management.html` (bundled SPA; gitignored generated asset; ~2.7MB)
- **Category**: Data Exposure / XSS blast radius
- **Description**: Bundled UI references `localStorage` / `sessionStorage` dozens of times and `managementKey` ~25 times. XSS in the panel or a malicious extension can exfiltrate the management key. `dangerouslySetInnerHTML`-style patterns appear in the bundle (React).
- **Impact**: XSS → full management compromise (see secret export findings).
- **Recommendation**: Prefer memory-only key or HttpOnly session cookie established via login endpoint; CSP; strict management CORS (already partially done).
- **References**: OWASP A03 Injection / XSS

---

### [LOW] Missing global security headers

- **File**: `internal/api/server.go` (middleware stack)
- **Category**: Infra
- **Description**: No global `Content-Security-Policy`, `X-Frame-Options`/`frame-ancestors`, `Referrer-Policy`, or HSTS. `GetConfigYAML` sets `X-Content-Type-Options: nosniff` only for that response.
- **Impact**: Clickjacking of management UI if hosted same-origin; weaker browser hardening.
- **Recommendation**: Add secure default headers middleware; CSP for `/management` assets.
- **References**: OWASP Secure Headers

---

### [LOW] MD5 used in Qoder request signing

- **File**: `internal/runtime/executor/qoder_executor.go` (~1168–1169, ~1443)
- **Category**: Cryptography
- **Description**: MD5 hashes used for protocol signatures toward Qoder/COSY APIs. Likely mandated by upstream protocol rather than local password storage.
- **Impact**: Weak primitive if reused for security decisions beyond vendor protocol compatibility.
- **Recommendation**: Isolate to protocol adapters; never use MD5 for local password/token generation; document as vendor constraint.
- **References**: CWE-328

---

### [LOW] Default bind host empty (all interfaces)

- **File**: `config.example.yaml` (host: `''`); server listen path
- **Category**: Infra
- **Description**: Empty host binds all interfaces. Combined with open API keys or unauth OAuth routes, exposure is immediate on LAN/WAN.
- **Recommendation**: Safer example default `127.0.0.1` for dev; require explicit `0.0.0.0` for production.
- **References**: Secure-by-default

---

### [LOW] pprof optional but powerful if enabled on non-localhost

- **File**: `sdk/cliproxy/pprof_server.go`; `config.example.yaml` pprof section
- **Category**: Data Exposure
- **Description**: pprof exposes heap/goroutine/profiles. Example defaults to localhost—good—but config can set arbitrary `addr` with no forced localhost check in code.
- **Impact**: Memory disclosure including secrets if bound publicly.
- **Recommendation**: Enforce loopback-only unless `pprof.allow-remote: true`.
- **References**: Go pprof security notes

---

### [LOW] No application-level rate limit on public API / OAuth start

- **File**: Management has IP ban on failed management keys (`handler.go`); public `/v1` and `/v0/oauth/*` lack global rate limits
- **Category**: Business Logic / Availability
- **Description**: Failed management auth is throttled (5 fails → 30m ban). Public completion APIs and unauthenticated OAuth starts are not globally rate-limited in-process.
- **Impact**: Credential stuffing against API keys; OAuth start spam; cost amplification on upstream providers.
- **Recommendation**: Per-IP/per-key rate limits on auth and OAuth start; WAF/reverse-proxy limits.
- **References**: OWASP A04 Insecure Design

---

## Positive Controls Observed

1. **Secret files not tracked**: `.gitignore` covers `.env`, `config.yaml`, `auths/*`, `CLIProxyAPI/*`, `static/*`, store spools; `git ls-files` only shows `auths/.gitkeep` under auth paths.
2. **Management auth design**: Requires key even on localhost; uses `RemoteAddr` (not `ClientIP`/XFF) for loopback detection; bcrypt for stored secret-key; constant-time compare for env secret; failed attempt lockout.
3. **Management CORS default deny** with explicit origin allowlist support.
4. **Auth file name safety** on management download/delete: rejects `/`, `\`, volume names (`isUnsafeAuthFileName`).
5. **Error log download** validates prefix/suffix and rejects path separators; cleans path under log dir.
6. **Amp reverse proxy** strips client `Authorization` / query keys before injecting upstream key; scrubs fingerprint headers.
7. **Logging helpers** mask Authorization and api-key/token/secret header values and sensitive query params in gin access logs.
8. **OAuth crypto/rand state** used correctly in several providers (`misc.GenerateRandomState`, Kiro web `generateStateID`).
9. **Config secret-key hashing on load** (plaintext → bcrypt) reduces disk plaintext for management key in YAML after first run.
10. **Usage-keeper session cookie** sets HttpOnly (+ Secure when appropriate), SameSite=Lax.

---

## Secrets & Credentials — Git / CI detail

| Check | Result |
|-------|--------|
| `git check-ignore .env config.yaml` | Ignored |
| `git ls-files` for those paths | Not tracked |
| `auths/*` | Ignored; only `auths/.gitkeep` tracked |
| CI workflows | Use `${{ secrets.GITHUB_TOKEN }}` only in reviewed workflow text; no hardcoded PATs found in `.github/workflows` |
| Hardcoded provider OAuth secrets | **Present** (see HIGH finding) |
| Example config placeholders | `config.example.yaml` uses fake `your-api-key-*` and commented samples — OK |

---

## Injection notes (detail)

| Vector | Assessment |
|--------|------------|
| SQL | Usage keeper migrations/queries largely static or `?` placeholders; `fullTableName` uses `pgx.Identifier.Sanitize`. No classic string-concat SQLi found in hot paths. |
| Command injection | `os/exec` in `internal/browser`, TUI, Kiro/Qoder protocol handlers — commands are fixed binaries; URL passed as **argv** (not shell). Residual risk if URL schemes are exotic on some OS handlers — treat as LOW if browser open is only local admin UX. |
| Path traversal (mgmt auth files) | Mitigated by `isUnsafeAuthFileName`. |
| Path traversal (logs) | Mitigated for error log download. |
| XSS | Management UI is large React bundle with `dangerouslySetInnerHTML` usage count >0 — treat as residual XSS risk elevating management key theft (MEDIUM). Server-rendered OAuth HTML pages should avoid reflecting unsanitized query errors (spot-check recommended in remediation). |
| SSRF | **Confirmed** Kiro region breakout (CRITICAL); management APICall (HIGH); registry/model updaters use fixed/configured URLs (lower risk). |

---

## AuthN/AuthZ notes (detail)

| Area | Assessment |
|------|------------|
| Management middleware | Strong when secret configured; routes unregistered if no secret/env/local password |
| Env password remote override | **HIGH** issue |
| Public API | Open if no api-keys |
| OAuth web helpers | **Unauthenticated** credential write paths (Kiro import/refresh) |
| CSRF | Management uses bearer/header key (not cookie) for API — browser CSRF harder unless key in JS storage; panel stores key client-side |
| IDOR auth files | Names validated; still full read of any auth file with mgmt key (expected admin capability) |
| WS | Origin always true; optional auth |

---

## Cryptography notes

| Item | Notes |
|------|-------|
| Management secret | bcrypt — good |
| OAuth state | Mixed CSPRNG vs UnixNano — fix UnixNano sites |
| PKCE | Present for several providers (claude/codex/xai/kiro) — good |
| MD5 | Qoder protocol only |
| TLS skip verify | Optional flags exist |

---

## Business logic / race notes

- File-based auth store + multi-process/cluster: watcher hot-reload and concurrent OAuth saves may race (TOCTOU on filename conflict resolution) — primarily integrity/availability, residual **LOW/MEDIUM** without confirmed exploit in this pass.
- Round-robin credential selection after forced unauth import could prefer attacker-planted tokens — tied to CRITICAL import finding.
- `ws-auth` hot toggle terminates sessions when enabling — good; disabling leaves sessions — expected.

---

## Priority Remediation Plan

### P0 (immediate)
1. Require management auth (or localhost-only) on `/v0/oauth/kiro/import`, `/refresh`, and ideally all OAuth **start** routes that write credentials; same for CodeArts/JoyCode start if network-exposed.
2. Validate Kiro `region` strictly; fix `getOIDCEndpoint`.
3. Default-deny empty API key configuration (break legacy open mode or require explicit flag).

### P1 (this sprint)
4. Stop `MANAGEMENT_PASSWORD` from forcing remote allow.
5. Restrict management `APICall` destinations; redact management config/API key responses by default.
6. Fix websocket origin checks; code-default `ws-auth` true.
7. Replace UnixNano OAuth states with `GenerateRandomState`.
8. Rotate/centralize hardcoded OAuth client secrets where possible.

### P2 (this quarter)
9. Non-root Docker user; reduce published ports.
10. Remove query-string API key auth or strongly discourage.
11. Security headers + CSP for management UI; avoid long-lived management key in `localStorage`.
12. Replace go-git alpha; review InsecureSkipVerify call sites.
13. Request-log body redaction.

---

## Out of Scope / Deferred

- Full dependency CVE database correlation (Dependency agent).
- Dynamic/runtime exploitation and authenticated pentest against a live instance.
- Reading live `.env` / `config.yaml` / `auths/` (forbidden by audit rules).
- Binary firmware or third-party panel GitHub repo deep review beyond local `static/management.html` heuristics.

---

## Appendix A — High-risk file index

| Path | Why |
|------|-----|
| `internal/api/server.go` | Route registration, AuthMiddleware, CORS, WS attach, OAuth route wiring |
| `internal/api/handlers/management/handler.go` | Management auth, remote override |
| `internal/api/handlers/management/api_tools.go` | APICall SSRF surface, OAuth client secrets |
| `internal/api/handlers/management/config_basic.go` | Config/YAML secret export |
| `internal/api/handlers/management/auth_files_io.go` | Auth file download/upload |
| `internal/auth/kiro/oauth_web.go` | Unauth import/refresh/start |
| `internal/auth/kiro/sso_oidc.go` | Region URL construction |
| `internal/access/config_access/provider.go` | Query-string API keys |
| `sdk/access/manager.go` | Empty provider open auth |
| `internal/wsrelay/manager.go` | CheckOrigin true |
| `Dockerfile` / `docker-compose.yml` | Root + ports |
| `go.mod` | go-git alpha |

---

*End of security audit report.*
