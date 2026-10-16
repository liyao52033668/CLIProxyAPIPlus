package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"strings"
)

// OpenCode's free tier (2026-10-14 gate) validates request identity beyond the
// header names: the session id must be minted in the official client's
// descending Identifier layout, the project header must be a bare 40-hex id,
// the User-Agent must read opencode/<channel>/<version>/cli, and the body must
// carry the genuine core agent tool definitions with streaming forced on.
// These helpers produce that shape from any stable per-conversation identity.

// openCodeClientVersion is the OpenCode release the requests claim to be.
// Upstream accepts a range of releases; bump when it starts rejecting this one.
const openCodeClientVersion = "2.0.29"

// OpenCodeUserAgent reports the shape upstream expects.
func OpenCodeUserAgent() string {
	return "opencode/latest/" + openCodeClientVersion + "/cli"
}

// OpenCodeCanonicalSessionID re-encodes a stable identity into the official
// session layout: "ses_" + 12 hex time chars + 14 base62 chars. The time part
// mirrors Identifier.descending(): a 36-bit timestamp shifted into the top of
// a 48-bit word, ORed with counter 1, then bitwise-NOT-ed so the low 12 hex
// digits read ffe — the encoding the free tier's shape check accepts.
func OpenCodeCanonicalSessionID(identity string) string {
	sum := sha256.Sum256([]byte("ses\x00" + strings.TrimSpace(identity)))
	current := (uint64(sum[0])<<28 | uint64(sum[1])<<20 | uint64(sum[2])<<12 | uint64(sum[3])<<4 | uint64(sum[4])>>4) << 12
	current |= 1
	inverted := ^current & 0xFFFFFFFFFFFF
	timePart := hex.EncodeToString([]byte{
		byte(inverted >> 40), byte(inverted >> 32), byte(inverted >> 24),
		byte(inverted >> 16), byte(inverted >> 8), byte(inverted),
	})
	const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	n := new(big.Int).SetBytes(sum[6:16])
	out := make([]byte, 14)
	remainder := new(big.Int)
	base := big.NewInt(62)
	for i := 13; i >= 0; i-- {
		n.DivMod(n, base, remainder)
		out[i] = base62[remainder.Int64()]
	}
	return "ses_" + timePart + string(out)
}

// OpenCodeProjectID renders the project header the way the client does: a bare
// 40-character lowercase hex id (not "prj_"-prefixed, not "global"). Only the
// shape is checked upstream; hashing keeps one conversation pinned to one
// project.
func OpenCodeProjectID(identity string) string {
	sum := sha256.Sum256([]byte("opencode-project\x00" + strings.TrimSpace(identity)))
	return hex.EncodeToString(sum[:20])
}

// openCodeCoreTools are the tool names the free tier recognises and validates;
// every missing entry is appended to the outbound body.
var openCodeCoreTools = []string{"shell", "edit", "glob", "grep", "read"}

// OpenCodePrepareAnonymousBody shapes a Chat Completions body for the anonymous
// free-tier lane: streaming forced on, usage reporting attached, and the core
// OpenCode tool definitions injected with their genuine byte-for-byte
// definitions. Bodies that already declare a recognised tool keep the client's
// tool order; only missing core tools are appended. Non-object bodies are
// returned unchanged.
func OpenCodePrepareAnonymousBody(body []byte) []byte {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	changed := false
	if streaming, ok := payload["stream"].(bool); !ok || !streaming {
		payload["stream"] = true
		changed = true
	}
	options, ok := payload["stream_options"].(map[string]any)
	if !ok {
		payload["stream_options"] = map[string]any{"include_usage": true}
		changed = true
	} else if includeUsage, ok := options["include_usage"].(bool); !ok || !includeUsage {
		options["include_usage"] = true
		changed = true
	}
	if openCodeEnsureTools(payload) {
		changed = true
	}
	if !changed {
		return body
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return encoded
}

func openCodeEnsureTools(payload map[string]any) bool {
	definitions, errParse := openCodeToolDefinitions()
	if errParse != nil || len(definitions) == 0 {
		return false
	}
	raw, exists := payload["tools"]
	if !exists {
		payload["tools"] = openCodeToolset(definitions, openCodeCoreTools)
		return true
	}
	items, ok := raw.([]any)
	if !ok {
		return false
	}
	present := make(map[string]bool, len(items))
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if fn, ok := entry["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok && name != "" {
				present[name] = true
			}
		}
	}
	missing := make([]string, 0, len(openCodeCoreTools))
	for _, name := range openCodeCoreTools {
		if !present[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return false
	}
	payload["tools"] = append(items, openCodeToolset(definitions, missing)...)
	return true
}

func openCodeToolset(definitions map[string]any, names []string) []any {
	tools := make([]any, 0, len(names))
	for _, name := range names {
		def, ok := definitions[name].(map[string]any)
		if !ok {
			continue
		}
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        name,
				"description": def["description"],
				"parameters":  def["parameters"],
			},
		})
	}
	return tools
}

func openCodeToolDefinitions() (map[string]any, error) {
	var definitions map[string]any
	if err := json.Unmarshal(OpenCodeToolsJSON, &definitions); err != nil {
		return nil, err
	}
	return definitions, nil
}
