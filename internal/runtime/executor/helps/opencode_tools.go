// Package helps: opencode_tools.go embeds the official OpenCode CLI core tool
// definitions. The OpenCode free tier validates the definition of every tool
// whose name it recognises byte-for-byte against the official client, so a
// synthesised stub is rejected with 403 FreeTierError. Captured from
// @opencode/cli 2.0.x and mirrored from jasonxu114514/opencode2api.
package helps

import _ "embed"

// OpenCodeToolsJSON holds the five core agent tools (shell, edit, glob, grep,
// read) in the wire shape the free tier expects, keyed by tool name.
//
//go:embed opencode_tools.json
var OpenCodeToolsJSON []byte
