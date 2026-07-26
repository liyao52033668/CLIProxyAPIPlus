# Performance Audit Report
Generated: 2026-07-26
Auditor: Performance Agent (Overnight Repo Auditor)

## Executive Summary
- **Total findings: 18** (CRITICAL: 1, HIGH: 6, MEDIUM: 8, LOW: 3)
- **Estimated overall performance health: FAIR**
- The proxy’s steady-state request path is mostly correct (shared proxy client cache, Kiro pooled transport, tokenizer cache, model-list cache, bounded codex/xai/antigravity replay caches). The largest risks sit on hot paths: Claude uTLS clients that never reuse TLS sessions, unbuffered stream channels, and request-log aggregation that rebuilds full stream bodies per chunk. Under multi-tenant load these will dominate latency and memory before DB or build concerns do.

## Critical Findings

### [CRITICAL] Claude uTLS HTTP clients recreate TLS connections on every request
- **File**: `internal/runtime/executor/helps/utls_client.go` (lines 29-44, 184-220); call site `internal/runtime/executor/claude_executor.go` (line 439)
- **Category**: Connection Pool / TLS Re-handshake
- **Description**: `NewUtlsHTTPClient` always constructs a brand-new `utlsRoundTripper` with empty `connections` / `pending` maps, plus a fresh `http.Transport` fallback. Unlike `helps.NewProxyAwareHTTPClient` (which caches by proxy URL) and Kiro’s pooled client, Claude’s path never reuses h2 ClientConns or TLS sessions across requests. Each Claude stream/non-stream call therefore pays full TCP + uTLS handshake + HTTP/2 setup cost against `api.anthropic.com`.
- **Evidence**:
```go
func newUtlsHTTPClient(...) *http.Client {
    utlsRT := newUtlsRoundTripper(proxyURL) // always new
    var standardTransport http.RoundTripper = &http.Transport{...}
    client := &http.Client{Transport: &fallbackRoundTripper{utls: utlsRT, fallback: standardTransport}}
    return client
}
// claude_executor.go
Client: helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0),
```
- **Impact**: Under concurrent Claude traffic, CPU and dial latency spike; connection storms against Anthropic and intermediate proxies; TTFT (time-to-first-token) inflated by tens–hundreds of ms per request; file descriptors and ephemeral ports churn.
- **Recommendation**: Cache `*http.Client` / `*utlsRoundTripper` by `(proxyURL, fingerprint profile)` with the same double-checked locking pattern as `cachedProxyClient`. Cap idle h2 conns and prune dead `ClientConn`s. Ensure timeout wrappers share the underlying transport (as proxy cache already does).
- **References**: Go `http.Transport` connection pooling; HTTP/2 ClientConn reuse; `internal/runtime/executor/kiro_http.go` as the in-repo reference implementation.

## High Findings

### [HIGH] Unbuffered StreamChunk channels couple producer and consumer on every provider stream
- **File**: Multiple executors, e.g. `internal/runtime/executor/claude_executor.go:446`, `codex_executor.go:530`, `codex_websockets_executor.go:729`, `xai_websockets_executor.go:543`, `gemini_executor.go:249`, `openai_compat_executor.go:424`; also `sdk/cliproxy/auth/conductor_execute.go:430`, `internal/wsrelay/http.go:121`
- **Category**: Streaming Backpressure / Concurrency
- **Description**: Almost all stream producers use `out := make(chan cliproxyexecutor.StreamChunk)` (buffer 0). Producer goroutines block on every chunk until the handler drains and flushes to the client. Slow clients, flush stalls, or translation work hold upstream read loops, increasing memory pressure in the OS socket buffer and reducing concurrency.
- **Evidence**:
```go
out := make(chan cliproxyexecutor.StreamChunk)
// ...
select {
case out <- cliproxyexecutor.StreamChunk{Payload: cloned}:
case <-ctx.Done():
    return
}
```
- **Impact**: Head-of-line blocking between upstream SSE/websocket pumps and downstream Gin writers; reduced throughput under many concurrent streams; amplified by per-chunk translation work.
- **Recommendation**: Use a small bounded buffer (e.g. 16–64) consistently, or a single adaptive buffer policy in a shared stream helper. Keep hard backpressure (block when full) but absorb short consumer stalls. Joycode’s `make(..., 64)` is a good local precedent.
- **References**: Go channel buffering guidance for streaming pipelines.

### [HIGH] RequestLog path rebuilds the entire aggregated response on every SSE chunk (O(n²))
- **File**: `internal/runtime/executor/helps/logging_helpers.go` (lines 207-246, 438-459)
- **Category**: Allocation / Algorithmic Complexity
- **Description**: When `cfg.RequestLog` is enabled, `AppendAPIResponseChunk` appends to a per-attempt `strings.Builder` and then calls `updateAggregatedResponse`, which re-walks all attempts, copies full response text, and `ginCtx.Set(apiResponseKey, []byte(...))` on **every** chunk. A 2 MB stream with ~2k chunks reallocates multi-MB aggregates repeatedly.
- **Evidence**:
```go
func AppendAPIResponseChunk(...) {
    // ...
    attempt.response.WriteString(string(data))
    updateAggregatedResponse(ginCtx, attempts)
}
func updateAggregatedResponse(...) {
    var builder strings.Builder
    for _, attempt := range attempts {
        builder.WriteString(attempt.response.String())
        // ...
    }
    ginCtx.Set(apiResponseKey, []byte(builder.String()))
}
```
- **Impact**: With request logging on (common in debugging/production diagnostics), CPU and GC explode on long streams; risk of multi-GB temporary allocations under concurrent logged streams.
- **Recommendation**: Aggregate lazily at request end only; or maintain one growing `[]byte`/Builder without rebuilding. Cap logged body size (mirror request-logging middleware limits). Prefer sampling/truncated capture for streams.
- **References**: Same package’s deferred request path already avoids work when logging is off — extend that discipline to response aggregation.

### [HIGH] Client-facing API_RESPONSE capture also concatenates with full copies
- **File**: `sdk/api/handlers/handlers.go` (lines 506-530)
- **Category**: Allocation / Memory
- **Description**: `appendAPIResponse` stores the full translated stream in Gin context by allocating a new combined buffer for every append (`make` + double copy). Used when handlers capture responses for logging/error reconstruction.
- **Evidence**:
```go
combined := make([]byte, 0, len(existingBytes)+len(data)+1)
combined = append(combined, existingBytes...)
combined = append(combined, data...)
c.Set("API_RESPONSE", combined)
```
- **Impact**: O(n²) memory traffic for large streams when capture is active; compounds with executor-side logging.
- **Recommendation**: Use `bytes.Buffer` / `strings.Builder` stored once in context; finalize to `[]byte` at end. Enforce a max capture size.
- **References**: Gin context value lifetime = request scope only, but allocation cost is still paid live on the hot path.

### [HIGH] Stream translators allocate via repeated sjson.SetBytes on every delta
- **File**: e.g. `internal/translator/claude/openai/responses/claude_openai-responses_response.go` (lines 115-292+); `internal/translator/openai/openai/responses/openai_openai-responses_response.go` (lines 85+); many siblings under `internal/translator/**` (highest sjson usage: openai/gemini/claude responses converters)
- **Category**: Allocation / Hot-path JSON
- **Description**: Streaming response converters build each SSE event by starting from a JSON template and applying many `sjson.SetBytes` calls (each returns a new `[]byte`). Text text delta may perform 3–10 full JSON rewrites plus SSE framing.
- **Evidence**:
```go
msg, _ = sjson.SetBytes(msg, "sequence_number", nextSeq())
msg, _ = sjson.SetBytes(msg, "item_id", st.CurrentMsgID)
msg, _ = sjson.SetBytes(msg, "delta", text)
```
- **Impact**: Dominant allocator on cross-protocol streams (Claude↔OpenAI Responses, Gemini↔OpenAI, etc.). Increases GC CPU share and per-token latency.
- **Recommendation**: For high-frequency delta events, use `fmt.Appendf` / pre-sized `bytes.Buffer` templates with known field order; reserve sjson for sparse/complex terminal events. Consider `sync.Pool` for scratch buffers. Profile with `pprof` allocs on a synthetic stream.
- **References**: tidwall/sjson allocates on each Set; hot-path JSON should prefer fixed templates.

### [HIGH] SSE scanners allow 50 MiB per-line buffers
- **File**: `internal/runtime/executor/claude_executor.go` (lines 453-454, 486-487, 529-530); pattern repeated across many executors (`scanner.Buffer(nil, 52_428_800)`)
- **Category**: Memory / Resource Management
- **Description**: `bufio.Scanner` max token size is raised to 50 MB with a nil initial buffer. A single oversized SSE line forces a large allocation; concurrent streams can multiply this.
- **Evidence**:
```go
scanner := bufio.NewScanner(decodedBody)
scanner.Buffer(nil, 52_428_800) // 50MB
```
- **Impact**: Memory spikes / OOM risk when upstream emits huge lines (tool results, base64, malformed streams). Scanner also copies lines into its buffer.
- **Recommendation**: Use a modest initial buffer (e.g. 64–256 KiB) and a lower hard max aligned with realistic event sizes (1–4 MiB). For rare large payloads, fall back to `bufio.Reader` + delimiter scan without retaining 50 MiB capacity. Reject/log when max exceeded.
- **References**: `bufio.Scanner` grows to max token size; not a streaming window.

### [HIGH] Signature cache is unbounded and takes write locks on every Get
- **File**: `internal/cache/signature_cache.go` (lines 37-58, 91-114, 165-230)
- **Category**: Unbounded Cache / Lock Contention
- **Description**: Process-local signature cache has TTL cleanup (10 min) but **no max entry / max byte cap** (unlike codex/xai/antigravity reasoning replay caches). `GetCachedSignatureRequired` uses `sc.mu.Lock()` (exclusive) even on hit to implement sliding TTL. High multi-turn Claude/Gemini traffic serializes on the group mutex.
- **Evidence**:
```go
sc.mu.Lock()
entry, exists := sc.entries[textHash]
// ...
entry.Timestamp = now
sc.entries[textHash] = entry
sc.mu.Unlock()
```
- **Impact**: Memory growth with unique thinking texts; lock contention on popular model groups (`claude`, `gemini`).
- **Recommendation**: Add max entries / max bytes with batch eviction (copy codex replay limits). Use `RLock` + occasional refresh, or store expiry in atomic/time wheel; refresh TTL asynchronously. Consider sharded maps.
- **References**: In-repo `CodexReasoningReplayCacheMaxEntries` / `MaxTotalBytes` pattern.

## Medium Findings

### [MEDIUM] Session affinity cache grows without an entry cap
- **File**: `sdk/cliproxy/auth/session_cache.go` (lines 15-36, 59-95, 131-153)
- **Category**: Unbounded Cache
- **Description**: `SessionCache` only TTL-evicts. Distinct session IDs accumulate until TTL (default 30m). `GetAndRefresh` takes a full write lock per hit.
- **Evidence**:
```go
type SessionCache struct {
    mu      sync.RWMutex
    entries map[string]sessionEntry
    ttl     time.Duration
}
// no maxEntries field; cleanup only deletes expired
```
- **Impact**: Memory growth under many unique session keys; write-lock on every affinity hit.
- **Recommendation**: Cap entries (LRU/FIFO), use `RLock` for pure gets when sliding TTL not required, or store expiry without rewriting on every access.
- **References**: None.

### [MEDIUM] Websocket tool-output cache has per-session limits but no global session cap
- **File**: `sdk/api/handlers/openai/openai_responses_websocket_toolcall_repair.go` (lines 14-47, 50-124)
- **Category**: Unbounded Cache
- **Description**: `maxPerSession=256` and TTL cleanup exist, but `sessions` map itself is unbounded. Cleanup runs only on record/get while holding the global mutex.
- **Evidence**:
```go
type websocketToolOutputCache struct {
    sessions map[string]*websocketToolOutputSession
}
// cleanupLocked deletes expired sessions only when record/get is called
```
- **Impact**: Long-lived process with many short-lived websocket sessions retains memory until next access; mutex becomes a single point of contention.
- **Recommendation**: Global max sessions; background cleanup ticker; shard by session key.
- **References**: None.

### [MEDIUM] Kiro stream path re-tokenizes full accumulated text periodically (O(n²))
- **File**: `internal/runtime/executor/kiro_eventstream.go` (lines 1146-1173, 1741-1752)
- **Category**: Algorithmic Complexity / Tokenizer
- **Description**: During long streams, usage updates call `enc.Count(accumulatedContent.String())` on the **entire** accumulated output whenever char/time thresholds fire. Tokenizer instances are cached (good), but counting is not incremental.
- **Evidence**:
```go
if tokenCount, countErr := enc.Count(accumulatedContent.String()); countErr == nil {
    currentOutputTokens = int64(tokenCount)
}
```
- **Impact**: CPU grows with output length²-ish for long Kiro thinking/tool sessions; stalls stream pumping while counting.
- **Recommendation**: Count only the delta since last update and add to a running total; or estimate with chars during stream and exact-count once at end.
- **References**: `helps.TokenizerForModel` already caches codecs — keep that; fix call pattern only.

### [MEDIUM] Amp reverse proxy fully buffers gzip responses
- **File**: `internal/api/modules/amp/proxy.go` (lines 136-192)
- **Category**: Memory / Streaming
- **Description**: Non-streaming upstream responses that look gzip-compressed are fully `io.ReadAll`’d, decompressed into another full buffer, then served from memory. No size cap on this path.
- **Evidence**:
```go
rest, err := io.ReadAll(originalBody)
gzippedData := append(header[:n], rest...)
decompressed, err := io.ReadAll(gzipReader)
resp.Body = io.NopCloser(bytes.NewReader(decompressed))
```
- **Impact**: Large amp responses pin 2× body size in RAM; concurrent amp users amplify risk.
- **Recommendation**: Stream via `gzip.Reader` wrapping the body; set `Content-Encoding` correctly when possible; apply `io.LimitReader` if full buffer is mandatory.
- **References**: None.

### [MEDIUM] Postgres / usage DB pools hard-capped at MaxOpenConns=5
- **File**: `internal/store/postgresstore.go` (lines 104-106); `internal/usage/keeper/repository/postgres.go` (lines 32-34); sqlite usage DB uses MaxOpenConns=1 (`db.go:35-36`)
- **Category**: Database Connection Pool
- **Description**: Fixed tiny pools are safe for single-instance light metadata use but become a queueing bottleneck if management/usage/list endpoints share the same DB under load. List endpoints generally paginate (good).
- **Evidence**:
```go
sqlDB.SetMaxOpenConns(5)
sqlDB.SetMaxIdleConns(2)
sqlDB.SetConnMaxLifetime(30 * time.Minute)
```
- **Impact**: Request latency tails when DB-backed features are enabled; not on pure proxy hot path unless usage writes are synchronous on the request.
- **Recommendation**: Make pool size configurable; separate pools for request-path vs background sync; verify usage recording is async (queue) on the proxy path.
- **References**: database/sql pool docs.

### [MEDIUM] Proxy HTTP client cache has no eviction
- **File**: `internal/runtime/executor/helps/proxy_helpers.go` (lines 16-42)
- **Category**: Unbounded Cache
- **Description**: `httpClientCache map[string]*http.Client` grows with distinct proxy URLs forever. Each entry holds a Transport and idle conns.
- **Evidence**:
```go
var httpClientCache = make(map[string]*http.Client)
// store only; no delete/evict path
```
- **Impact**: Low in normal configs (few proxies); HIGH if many per-auth proxy URLs rotate.
- **Recommendation**: Cap map size; close idle transports on eviction; key by normalized proxy URL.
- **References**: None.

### [MEDIUM] Per-request regexp compilation on error / cloak paths
- **File**: `internal/runtime/executor/gemini_cli_executor.go` (lines 911, 919); `internal/runtime/executor/helps/json_retry_helpers.go` (lines 62, 70); `internal/logging/request_logger.go` (lines 693, 697); `internal/runtime/executor/helps/cloak_obfuscate.go` (line 53)
- **Category**: Algorithmic / Allocation
- **Description**: `regexp.MustCompile` / `regexp.Compile` inside functions rather than package-level vars. Most other regexes in the repo are correctly package-scoped.
- **Evidence**:
```go
re := regexp.MustCompile(`after\s+(\d+)s\.?`)
reHuman := regexp.MustCompile(`after\s+((?:\d+h)?(?:\d+m)?(?:\d+s)?)\.?`)
```
- **Impact**: Extra alloc/CPU on error parsing and logging path; small per call but avoidable.
- **Recommendation**: Hoist to package-level `var` like other sanitizers in `internal/util`.
- **References**: Go wiki: don’t compile regex in hot loops.

### [MEDIUM] GetModelInfo deep-clones on every lookup
- **File**: `internal/registry/model_registry.go` (lines 599-637, 1269-1287)
- **Category**: Allocation / Lock
- **Description**: Safety-driven `cloneModelInfo` copies slices/maps for every `GetModelInfo` call under `RLock`. Correct for mutability safety; costly if called per request on large model metadata.
- **Evidence**:
```go
func (r *ModelRegistry) GetModelInfo(...) *ModelInfo {
    r.mutex.RLock()
    defer r.mutex.RUnlock()
    return cloneModelInfo(info)
}
```
- **Impact**: Extra allocations on model resolution; mitigated by `availableModelsCache` for list endpoints.
- **Recommendation**: Return immutable views or copy-on-write; cache clones per (model, provider) with invalidation on registry update (already have invalidation hooks).
- **References**: `TestGetModelInfoReturnsClone` documents intentional safety tradeoff.

## Low Findings

### [LOW] Dockerfile enables CGO for sqlite, enlarging runtime surface
- **File**: `Dockerfile` (lines 1-21)
- **Category**: Build & Deploy
- **Description**: Multi-stage build is good (`golang:1.26-alpine` → `alpine:3.23`, `-ldflags -s -w`). `CGO_ENABLED=1` + `gcc musl-dev` pull CGO/sqlite into the binary.
- **Evidence**:
```dockerfile
RUN CGO_ENABLED=1 GOOS=linux go build ... -o ./CLIProxyAPIPlus ./cmd/server/
```
- **Impact**: Larger image and attack/build surface; slower builds. Acceptable if sqlite is required.
- **Recommendation**: Offer a pure-Go build tag path when postgres-only; document image size tradeoffs.
- **References**: None.

### [LOW] wsrelay pending channel buffer of 8 may stall bursty streams
- **File**: `internal/wsrelay/session.go` (lines 25-48); `internal/wsrelay/http.go` (line 121)
- **Category**: Streaming Backpressure
- **Description**: Pending request channels buffer 8 messages; `deliver` blocks when full (until ctx/session close). Outbound `Stream` channel is unbuffered. Intentional backpressure, but small buffer under high chunk rate.
- **Evidence**:
```go
return &pendingRequest{ctx: ctx, ch: make(chan Message, 8)}
out := make(chan StreamEvent)
```
- **Impact**: Secondary to main executor channels; may add latency for large relayed streams.
- **Recommendation**: Raise buffer modestly or make configurable; keep blocking (do not drop).
- **References**: Repo timeout policy exceptions for wsrelay deadlines — not a bug.

### [LOW] Few sync.Pool uses for hot-path byte buffers
- **File**: Hot path packages under `internal/runtime/executor`, `internal/translator`, `sdk/api/handlers`
- **Category**: Allocation
- **Description**: Stream paths frequently `make([]byte, len(line)+1)`, `bytes.Clone`, and sjson rewrites without buffer pooling.
- **Evidence**: Claude stream path clones every line; translators allocate new JSON per event.
- **Impact**: Incremental GC pressure; secondary to uTLS and sjson issues.
- **Recommendation**: Introduce pools for fixed-shape SSE line buffers after fixing larger issues.
- **References**: `sync.Pool` docs; avoid pooling across request lifetimes with retained refs.

## Performance Quick Wins
1. **Cache `NewUtlsHTTPClient` transports** by proxy URL (mirror `cachedProxyClient`) — largest single latency win for Claude.
2. **Stop per-chunk `updateAggregatedResponse`** — finalize logs once at stream end; cap body size.
3. **Buffer StreamChunk channels (16–64)** across executors + wsrelay Stream helper.
4. **Cap signature / session / tool-output caches** (entries + bytes) using the codex replay limit pattern.
5. **Replace hot sjson delta builders** with `fmt.Appendf` templates for text-delta events only.

## Checklist Coverage
1. **Database & Query**: CHECKED — 1 finding (pool sizing). List/usage queries largely paginated; no classic N+1 found on proxy hot path. Usage keeper archive/list use Limit batches.
2. **Memory & Resource Management**: CHECKED — 6 findings (unbounded caches, 50MB scanners, response aggregation, amp gzip buffer, session/tool caches).
3. **Streaming & Websocket paths**: CHECKED — 4 findings (unbuffered channels, sjson deltas, wsrelay buffers, API_RESPONSE capture). No classic goroutine leak found in wsrelay heartbeat/cleanup (ticker stopped, pending closed).
4. **API & Network**: CHECKED — 2 findings (uTLS no reuse CRITICAL; proxy cache unbounded MEDIUM). Kiro pooling is good. Retry/backoff exists in auth manager (`BackoffLevel`, `Retry-After`).
5. **Algorithmic Complexity**: CHECKED — 3 findings (log aggregation O(n²), kiro retokenize, per-call regexp).
6. **Concurrency**: CHECKED — 2 findings (signature write lock on get; session cache write lock). Round-robin cursor mutex scope is tight (good). No `time.Tick` leaks found in production code.
7. **Build & Deploy**: CHECKED — 1 finding (CGO). Multi-stage Dockerfile present.
8. **Caching Strategy**: CHECKED — Mixed: tokenizer cache good; model list cache good; reasoning replay bounded good; signature/session/tool/proxy caches incomplete bounds. OAuth refresh is scheduled (`NextRefreshAfter`), not per-request blind refresh.

## Files Reviewed
- `internal/runtime/executor/helps/utls_client.go`
- `internal/runtime/executor/helps/proxy_helpers.go`
- `internal/runtime/executor/helps/token_helpers.go`
- `internal/runtime/executor/helps/logging_helpers.go`
- `internal/runtime/executor/claude_executor.go`
- `internal/runtime/executor/kiro_http.go`
- `internal/runtime/executor/kiro_eventstream.go`
- `internal/runtime/executor/codex_executor.go`
- `internal/runtime/executor/codex_websockets_executor.go`
- `internal/runtime/executor/xai_websockets_executor.go`
- `internal/runtime/executor/openai_compat_executor.go`
- `internal/cache/signature_cache.go`
- `internal/cache/codex_reasoning_replay_cache.go`
- `internal/cache/antigravity_reasoning_replay_cache.go`
- `internal/cache/xai_reasoning_replay_cache.go`
- `internal/wsrelay/session.go`
- `internal/wsrelay/http.go`
- `internal/wsrelay/manager.go`
- `internal/api/buffered_conn.go`
- `internal/api/modules/amp/proxy.go`
- `internal/api/server.go` (body limits)
- `internal/registry/model_registry.go`
- `internal/store/postgresstore.go`
- `internal/usage/keeper/repository/postgres.go`
- `internal/usage/keeper/repository/db.go`
- `internal/usage/keeper/repository/usage.go`
- `internal/usage/keeper/repository/usage_identities.go`
- `sdk/api/handlers/handlers.go`
- `sdk/api/handlers/stream_forwarder.go`
- `sdk/api/handlers/request_body.go`
- `sdk/api/handlers/openai/openai_responses_websocket_toolcall_repair.go`
- `sdk/cliproxy/auth/selector.go`
- `sdk/cliproxy/auth/session_cache.go`
- `sdk/cliproxy/auth/conductor_result.go`
- `internal/translator/claude/openai/responses/claude_openai-responses_response.go`
- `internal/translator/openai/openai/responses/openai_openai-responses_response.go`
- `Dockerfile`
- Pattern scans across `internal/runtime/executor/**`, `internal/translator/**`, `internal/auth/**` (time.After / http.Client / regexp / channels)

## Methodology Notes
- Static analysis only: CodeGraph context/explore + ripgrep pattern sweeps + targeted source reads. No builds, tests, or runtime profiling.
- Sampling: prioritized HTTP/SSE/websocket hot paths, then cache/registry/auth selection, then usage DB/build.
- Intentionally **not** flagged: codex websocket liveness deadlines, wsrelay session deadlines, management APICall timeout, `fetch_antigravity_models` timeouts (repo policy exceptions).
- Assumed request logging may be enabled in production diagnostics — treated as a realistic load mode.
- Auth file re-read per request was not evidenced; watcher-driven auth updates appear to keep in-memory state (`internal/watcher`).
- Ambiguity: absolute production impact of `MaxOpenConns=5` depends on whether usage writes share the request path; documented as MEDIUM pending deployment topology.
