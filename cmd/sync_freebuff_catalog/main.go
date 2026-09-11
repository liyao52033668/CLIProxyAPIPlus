// Command sync_freebuff_catalog regenerates the Freebuff (Codebuff) model
// catalog from the upstream Codebuff source.
//
// Upstream serves no model-list endpoint: its catalog is compiled into the
// clients (common/src/constants/freebuff-models.ts) and the model -> root agent
// mapping lives in free-agents.ts. This command parses both and emits
// internal/registry/freebuff_catalog_generated.go so the proxy tracks upstream
// instead of drifting from a hand-copied snapshot.
//
// Usage:
//
//	go run ./cmd/sync_freebuff_catalog                  # print the catalog to stdout
//	go run ./cmd/sync_freebuff_catalog -write            # regenerate the catalog file
//	go run ./cmd/sync_freebuff_catalog -source-dir <p>   # parse a local checkout (offline)
//
// Flags:
//
//	-ref         <ref>   Upstream git ref to read (default "main")
//	-source-dir  <path>  Directory holding the constants .ts files; skips the network
//	-out         <path>  Output file when -write is set (default the registry file)
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	repoSlug       = "CodebuffAI/codebuff"
	rawHost        = "raw.githubusercontent.com"
	sourcePath     = "common/src/constants"
	defaultRef     = "main"
	defaultOut     = "internal/registry/freebuff_catalog_generated.go"
	requestTimeout = 30 * time.Second
)

// upstreamFiles are parsed in order; the first definition of a constant wins.
var upstreamFiles = []string{
	"freebuff-model-ids.ts",
	"freebuff-model-entitlements.ts",
	"model-config.ts",
	"freebuff-models.ts",
	"free-agents.ts",
}

func main() {
	ref := flag.String("ref", defaultRef, "upstream git ref to read")
	sourceDir := flag.String("source-dir", "", "local directory holding the constants .ts files (skips the network)")
	out := flag.String("out", defaultOut, "output file path")
	write := flag.Bool("write", false, "write the generated catalog to -out")
	flag.Parse()

	var files map[string]string
	var err error
	if strings.TrimSpace(*sourceDir) != "" {
		files, err = readLocal(*sourceDir)
	} else {
		files, err = fetchSources(*ref)
	}
	if err != nil {
		fatalf("load upstream sources: %v", err)
	}

	entries, err := buildCatalog(files)
	if err != nil {
		fatalf("build catalog: %v", err)
	}
	if len(entries) == 0 {
		fatalf("build catalog: parsed zero models, refusing to emit an empty catalog")
	}

	src, err := renderCatalog(entries, *ref)
	if err != nil {
		fatalf("render catalog: %v", err)
	}

	if !*write {
		if _, errWrite := os.Stdout.Write(src); errWrite != nil {
			fatalf("write stdout: %v", errWrite)
		}
		return
	}

	if err = os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fatalf("create output directory: %v", err)
	}
	if err = os.WriteFile(*out, src, 0o644); err != nil {
		fatalf("write %s: %v", *out, err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d models, upstream %s@%s)\n", *out, len(entries), repoSlug, *ref)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "sync_freebuff_catalog: "+format+"\n", args...)
	os.Exit(1)
}

func readLocal(dir string) (map[string]string, error) {
	files := make(map[string]string, len(upstreamFiles))
	for _, name := range upstreamFiles {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		files[name] = string(data)
	}
	return files, nil
}

func fetchSources(ref string) (map[string]string, error) {
	client := &http.Client{Timeout: requestTimeout}
	ctx := context.Background()
	files := make(map[string]string, len(upstreamFiles))
	for _, name := range upstreamFiles {
		raw := "https://" + rawHost + "/" + repoSlug + "/" + ref + "/" + sourcePath + "/" + name
		if err := validateURL(raw); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("GET %s: %w", raw, err)
		}
		body := resp.Body
		if resp.StatusCode != http.StatusOK {
			_ = body.Close()
			return nil, fmt.Errorf("GET %s: status %d", raw, resp.StatusCode)
		}
		var buf bytes.Buffer
		if _, err = buf.ReadFrom(body); err != nil {
			_ = body.Close()
			return nil, fmt.Errorf("read %s: %w", raw, err)
		}
		if err = body.Close(); err != nil {
			return nil, fmt.Errorf("close %s: %w", raw, err)
		}
		files[name] = buf.String()
	}
	return files, nil
}

// validateURL refuses anything but the allowlisted upstream host over http/https,
// and rejects loopback, private and reserved addresses.
func validateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("refusing scheme %q", u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	if host != rawHost {
		return fmt.Errorf("refusing host %q, only %s is allowed", host, rawHost)
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified()) {
		return fmt.Errorf("refusing private or loopback host %q", host)
	}
	return nil
}

// ---------------------------------------------------------------------------
// catalog model
// ---------------------------------------------------------------------------

type catalogEntry struct {
	ID            string
	DisplayName   string
	AgentID       string
	LegacyAgentID string
	InPicker      bool
	ContextLength int
	Thinking      []string
}

type modelObject struct {
	constName   string
	idConst     string
	displayName string
	effortsExpr string
}

func buildCatalog(files map[string]string) ([]catalogEntry, error) {
	table := newConstTable()
	for _, name := range upstreamFiles {
		content, ok := files[name]
		if !ok {
			return nil, fmt.Errorf("missing upstream file %q", name)
		}
		table.collect(content)
	}

	objects := table.modelObjects()

	defaultID, err := table.resolveName("DEFAULT_FREEBUFF_MODEL_ID")
	if err != nil {
		return nil, fmt.Errorf("resolve default model: %w", err)
	}

	picker, err := table.pickerOrder()
	if err != nil {
		return nil, err
	}

	agentByIDConst, err := table.base3AgentMap(files["free-agents.ts"])
	if err != nil {
		return nil, err
	}

	contextWindows, defaultContext, err := table.contextWindows()
	if err != nil {
		return nil, err
	}

	paused, err := table.pausedModelIDs()
	if err != nil {
		return nil, err
	}

	roots, err := table.rootAgentIDs()
	if err != nil {
		return nil, err
	}

	// order: upstream default first, then the picker order, then the rest.
	ordered := make([]string, 0, len(objects))
	seen := make(map[string]bool, len(objects))
	appendConst := func(name string) {
		if name == "" || seen[name] {
			return
		}
		if _, ok := objects[name]; !ok {
			return
		}
		seen[name] = true
		ordered = append(ordered, name)
	}

	var defaultConst string
	for name, obj := range objects {
		id, errResolve := table.resolveName(obj.idConst)
		if errResolve != nil {
			continue
		}
		if id == defaultID {
			defaultConst = name
		}
	}
	appendConst(defaultConst)
	for _, name := range picker {
		appendConst(name)
	}
	rest := make([]string, 0, len(objects))
	for name := range objects {
		if !seen[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	ordered = append(ordered, rest...)

	entries := make([]catalogEntry, 0, len(ordered))
	for _, constName := range ordered {
		obj := objects[constName]
		id, errResolve := table.resolveName(obj.idConst)
		if errResolve != nil {
			return nil, fmt.Errorf("model %s: resolve %s: %w", constName, obj.idConst, errResolve)
		}
		if paused[id] {
			continue
		}
		agent, ok := agentByIDConst[obj.idConst]
		if !ok || agent == "" {
			// No upstream root agent means the model cannot actually run here.
			continue
		}
		entry := catalogEntry{
			ID:            id,
			DisplayName:   obj.displayName,
			AgentID:       agent,
			InPicker:      pickerContains(picker, constName),
			ContextLength: contextWindows[obj.idConst],
		}
		if entry.DisplayName == "" {
			entry.DisplayName = id
		}
		if entry.ContextLength == 0 {
			entry.ContextLength = defaultContext
		}
		if legacy := base2Twin(agent); legacy != "" && roots[legacy] {
			entry.LegacyAgentID = legacy
		}
		if obj.effortsExpr != "" {
			entry.Thinking = table.literalArray(obj.effortsExpr)
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return nil, errors.New("no models survived filtering; upstream layout may have changed")
	}
	return entries, nil
}

func pickerContains(picker []string, name string) bool {
	for _, item := range picker {
		if item == name {
			return true
		}
	}
	return false
}

// base2Twin maps a base3 root agent id onto its base2 rollback twin. Upstream
// registers both generations under the same suffix.
func base2Twin(agent string) string {
	const base3 = "base3-"
	if !strings.HasPrefix(agent, base3) {
		return ""
	}
	return "base2-" + strings.TrimPrefix(agent, base3)
}

// ---------------------------------------------------------------------------
// TypeScript constant table
// ---------------------------------------------------------------------------

var (
	reTopConst  = regexp.MustCompile(`(?m)^[ \t]*(?:export[ \t]+)?const[ \t]+([A-Za-z_$][A-Za-z0-9_$]*)[ \t]*(?::[^=\n]*)?=`)
	reIdent     = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)
	reMemberRef = regexp.MustCompile(`^([A-Za-z_$][A-Za-z0-9_$]*)\.([A-Za-z_$][A-Za-z0-9_$]*)$`)
	reModelObj  = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*_MODEL$`)

	reObjID      = regexp.MustCompile(`(?m)^[ \t]*id:[ \t]*([A-Za-z_$][A-Za-z0-9_$]*)`)
	reObjDisplay = regexp.MustCompile(`(?m)^[ \t]*displayName:[ \t]*'([^']*)'`)
	reObjEfforts = regexp.MustCompile(`(?m)^[ \t]*efforts:[ \t]*([A-Za-z_$][A-Za-z0-9_$]*)`)
	reMapEntry   = regexp.MustCompile(`\[[ \t]*([A-Za-z_$][A-Za-z0-9_$]*)[ \t]*\]:[ \t]*'([^']*)'`)
	reCtxEntry   = regexp.MustCompile(`\[[ \t]*([A-Za-z_$][A-Za-z0-9_$]*)[ \t]*\]:[ \t]*([0-9][0-9_]*)`)
)

type constTable struct {
	order []string
	strs  map[string]string
	objs  map[string]map[string]string
}

func newConstTable() *constTable {
	return &constTable{
		strs: map[string]string{},
		objs: map[string]map[string]string{},
	}
}

func (t *constTable) collect(src string) {
	clean := stripComments(src)
	for _, m := range reTopConst.FindAllStringSubmatchIndex(clean, -1) {
		name := clean[m[2]:m[3]]
		if _, dup := t.strs[name]; dup {
			continue
		}
		expr, _ := exprAfter(clean, m[1])
		if expr == "" {
			continue
		}
		t.strs[name] = expr
		t.order = append(t.order, name)
		if strings.HasPrefix(expr, "{") {
			t.objs[name] = objectStringFields(expr)
		}
	}
}

// literal resolves a constant expression to a string: a quoted literal, an
// IDENT, or an OBJ.field member reference.
func (t *constTable) literal(expr string) (string, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return "", errors.New("empty constant expression")
	}
	if expr[0] == '\'' || expr[0] == '"' {
		end := skipString(expr, 0)
		if end <= 0 || end >= len(expr) {
			return "", fmt.Errorf("unterminated string literal in %q", expr)
		}
		return expr[1:end], nil
	}
	if m := reMemberRef.FindStringSubmatch(expr); m != nil {
		fields, ok := t.objs[m[1]]
		if !ok {
			return "", fmt.Errorf("unknown constant object %q", m[1])
		}
		value, ok := fields[m[2]]
		if !ok {
			return "", fmt.Errorf("object %q has no string field %q", m[1], m[2])
		}
		return value, nil
	}
	if reIdent.MatchString(expr) {
		return t.resolveSeen(expr, map[string]bool{})
	}
	return "", fmt.Errorf("unsupported constant expression %q", expr)
}

func (t *constTable) resolveName(name string) (string, error) {
	return t.resolveSeen(name, map[string]bool{})
}

func (t *constTable) resolveSeen(name string, seen map[string]bool) (string, error) {
	if seen[name] {
		return "", fmt.Errorf("cyclic constant reference %q", name)
	}
	seen[name] = true
	expr, ok := t.strs[name]
	if !ok {
		return "", fmt.Errorf("unknown constant %q", name)
	}
	return t.literal(expr)
}

func (t *constTable) resolveInt(name string) (int, error) {
	expr := strings.TrimSpace(t.strs[name])
	expr = strings.ReplaceAll(expr, "_", "")
	if expr == "" {
		return 0, fmt.Errorf("unknown constant %q", name)
	}
	var value int
	if _, err := fmt.Sscanf(expr, "%d", &value); err != nil {
		return 0, fmt.Errorf("constant %q is not numeric: %q", name, expr)
	}
	return value, nil
}

// modelObjects returns the model definition objects (`const X_MODEL = { ... }`).
func (t *constTable) modelObjects() map[string]modelObject {
	out := map[string]modelObject{}
	for _, name := range t.order {
		if !reModelObj.MatchString(name) {
			continue
		}
		expr := t.strs[name]
		if !strings.HasPrefix(expr, "{") {
			continue
		}
		obj := modelObject{constName: name}
		if m := reObjID.FindStringSubmatch(expr); m != nil {
			obj.idConst = m[1]
		}
		if obj.idConst == "" {
			continue
		}
		if m := reObjDisplay.FindStringSubmatch(expr); m != nil {
			obj.displayName = m[1]
		}
		if m := reObjEfforts.FindStringSubmatch(expr); m != nil {
			obj.effortsExpr = m[1]
		}
		out[name] = obj
	}
	return out
}

// pickerOrder returns the model object consts offered in the upstream picker
// (FREEBUFF_MODELS), in upstream display order.
func (t *constTable) pickerOrder() ([]string, error) {
	expr := t.strs["FREEBUFF_MODELS"]
	if expr == "" {
		return nil, errors.New("FREEBUFF_MODELS not found upstream")
	}
	var out []string
	for _, ident := range identArray(expr) {
		if reModelObj.MatchString(ident) {
			out = append(out, ident)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("FREEBUFF_MODELS parsed to zero entries")
	}
	return out, nil
}

// base3AgentMap merges the Web and CLI model -> base3 root agent maps. The two
// surfaces share a root id wherever they offer the same model.
func (t *constTable) base3AgentMap(src string) (map[string]string, error) {
	out := map[string]string{}
	for _, constName := range []string{
		"FREEBUFF_WEB_BASE3_AGENT_ID_BY_MODEL",
		"FREEBUFF_CLI_BASE3_AGENT_ID_BY_MODEL",
	} {
		expr := t.strs[constName]
		if expr == "" {
			return nil, fmt.Errorf("%s not found upstream", constName)
		}
		found := false
		for _, m := range reMapEntry.FindAllStringSubmatch(expr, -1) {
			found = true
			if existing, dup := out[m[1]]; dup && existing != m[2] {
				return nil, fmt.Errorf("%s maps to both %q and %q across surfaces", m[1], existing, m[2])
			}
			out[m[1]] = m[2]
		}
		if !found {
			return nil, fmt.Errorf("%s parsed to zero entries", constName)
		}
	}
	return out, nil
}

func (t *constTable) contextWindows() (map[string]int, int, error) {
	expr := t.strs["FREEBUFF_MODEL_CONTEXT_WINDOWS"]
	if expr == "" {
		return nil, 0, errors.New("FREEBUFF_MODEL_CONTEXT_WINDOWS not found upstream")
	}
	out := map[string]int{}
	for _, m := range reCtxEntry.FindAllStringSubmatch(expr, -1) {
		var value int
		if _, err := fmt.Sscanf(strings.ReplaceAll(m[2], "_", ""), "%d", &value); err != nil {
			return nil, 0, fmt.Errorf("context window for %s is not numeric: %q", m[1], m[2])
		}
		out[m[1]] = value
	}
	fallback, err := t.resolveInt("FREEBUFF_DEFAULT_CONTEXT_WINDOW")
	if err != nil {
		return nil, 0, fmt.Errorf("resolve default context window: %w", err)
	}
	return out, fallback, nil
}

// pausedModelIDs resolves FREEBUFF_PAUSED_FREE_MODEL_IDS to wire ids. Upstream
// keeps withdrawn models recognised so released clients can be coerced; we omit
// them instead, because offering a withdrawn row only produces failures.
func (t *constTable) pausedModelIDs() (map[string]bool, error) {
	out := map[string]bool{}
	expr := t.strs["FREEBUFF_PAUSED_FREE_MODEL_IDS"]
	if expr == "" {
		return out, nil
	}
	for _, ident := range identArray(expr) {
		id, err := t.resolveName(ident)
		if err != nil {
			// Not a model-id constant (e.g. a helper reference): ignore.
			continue
		}
		out[id] = true
	}
	return out, nil
}

// rootAgentIDs returns the enumerated base2 rollback roots.
func (t *constTable) rootAgentIDs() (map[string]bool, error) {
	out := map[string]bool{}
	expr := t.strs["FREEBUFF_ROOT_AGENT_IDS"]
	if expr == "" {
		return out, nil
	}
	for _, id := range literalArray(expr) {
		out[id] = true
	}
	return out, nil
}

func (t *constTable) literalArray(expr string) []string {
	return literalArray(strings.TrimSpace(t.strs[expr]))
}

// ---------------------------------------------------------------------------
// tiny TypeScript scanners
// ---------------------------------------------------------------------------

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentChar(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

func skipSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}

// skipString returns the index of the closing quote for the string starting at
// s[i]. It returns len(s)-1 when the literal is unterminated.
func skipString(s string, i int) int {
	if i >= len(s) {
		return len(s) - 1
	}
	quote := s[i]
	i++
	for i < len(s) {
		if s[i] == '\\' {
			i += 2
			continue
		}
		if s[i] == quote {
			return i
		}
		i++
	}
	return len(s) - 1
}

// stripComments removes // and /* */ comments, preserving string contents.
func stripComments(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	i := 0
	for i < len(src) {
		c := src[i]
		if c == '\'' || c == '"' || c == '`' {
			end := skipString(src, i)
			b.WriteString(src[i : end+1])
			i = end + 1
			continue
		}
		if c == '/' && i+1 < len(src) {
			if src[i+1] == '/' {
				for i < len(src) && src[i] != '\n' {
					i++
				}
				continue
			}
			if src[i+1] == '*' {
				i += 2
				for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
					i++
				}
				i += 2
				continue
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// exprAfter returns the expression that follows pos, ending at the first
// depth-0 newline, semicolon or closing bracket.
func exprAfter(src string, pos int) (string, int) {
	i := skipSpace(src, pos)
	if i >= len(src) {
		return "", i
	}
	start := i
	depth := 0
	for i < len(src) {
		c := src[i]
		if c == '\'' || c == '"' || c == '`' {
			i = skipString(src, i) + 1
			continue
		}
		switch c {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth <= 0 {
				return strings.TrimSpace(src[start : i+1]), i + 1
			}
		case '\n', ';':
			if depth == 0 {
				return strings.TrimSpace(src[start:i]), i
			}
		}
		i++
	}
	return strings.TrimSpace(src[start:i]), i
}

// objectStringFields reads the depth-1 `name: 'literal'` fields of an object
// literal expression.
func objectStringFields(expr string) map[string]string {
	out := map[string]string{}
	depth := 0
	i := 0
	for i < len(expr) {
		c := expr[i]
		if c == '\'' || c == '"' || c == '`' {
			i = skipString(expr, i) + 1
			continue
		}
		if c == '{' {
			depth++
			i++
			continue
		}
		if c == '}' {
			depth--
			i++
			continue
		}
		if depth == 1 && isIdentStart(c) {
			j := i
			for j < len(expr) && isIdentChar(expr[j]) {
				j++
			}
			name := expr[i:j]
			k := skipSpace(expr, j)
			if k < len(expr) && expr[k] == ':' {
				k = skipSpace(expr, k+1)
				if k < len(expr) && (expr[k] == '\'' || expr[k] == '"') {
					end := skipString(expr, k)
					out[name] = expr[k+1 : end]
					i = end + 1
					continue
				}
			}
			i = j
			continue
		}
		i++
	}
	return out
}

// literalArray collects the string literals of an array expression.
func literalArray(expr string) []string {
	var out []string
	i := 0
	for i < len(expr) {
		c := expr[i]
		if c == '\'' || c == '"' || c == '`' {
			end := skipString(expr, i)
			out = append(out, expr[i+1:end])
			i = end + 1
			continue
		}
		i++
	}
	return out
}

// identArray collects the identifiers of an array expression, skipping strings.
func identArray(expr string) []string {
	var out []string
	i := 0
	for i < len(expr) {
		c := expr[i]
		if c == '\'' || c == '"' || c == '`' {
			i = skipString(expr, i) + 1
			continue
		}
		if isIdentStart(c) {
			j := i
			for j < len(expr) && isIdentChar(expr[j]) {
				j++
			}
			out = append(out, expr[i:j])
			i = j
			continue
		}
		i++
	}
	return out
}

// ---------------------------------------------------------------------------
// rendering
// ---------------------------------------------------------------------------

func renderCatalog(entries []catalogEntry, ref string) ([]byte, error) {
	var b strings.Builder
	b.WriteString("// Code generated by cmd/sync_freebuff_catalog. DO NOT EDIT.\n")
	b.WriteString("//\n")
	b.WriteString("// Upstream exposes no model-list endpoint, so this catalog is generated by\n")
	b.WriteString("// parsing the upstream TypeScript catalog and model -> root agent maps.\n")
	b.WriteString("//\n")
	b.WriteString("// Source: " + repoSlug + "@" + ref + "\n")
	b.WriteString("//   - " + sourcePath + "/freebuff-models.ts (default model, picker order, context windows, reasoning ladders)\n")
	b.WriteString("//   - " + sourcePath + "/free-agents.ts     (model -> base3 root agent, base2 rollback roots)\n")
	b.WriteString("//\n")
	b.WriteString("// Regenerate with:\n")
	b.WriteString("//\n")
	b.WriteString("//\tgo run ./cmd/sync_freebuff_catalog -write\n")
	b.WriteString("package registry\n\n")
	b.WriteString("// freebuffCatalog is the Freebuff (Codebuff) model directory. The first entry\n")
	b.WriteString("// is upstream's default model; a bare \"freebuff\"/\"codebuff\" request resolves to it.\n")
	b.WriteString("var freebuffCatalog = []FreebuffCatalogEntry{\n")
	for _, e := range entries {
		b.WriteString("\t{\n")
		fmt.Fprintf(&b, "\t\tID:            %q,\n", e.ID)
		fmt.Fprintf(&b, "\t\tDisplayName:   %q,\n", e.DisplayName)
		fmt.Fprintf(&b, "\t\tAgentID:       %q,\n", e.AgentID)
		if e.LegacyAgentID != "" {
			fmt.Fprintf(&b, "\t\tLegacyAgentID: %q,\n", e.LegacyAgentID)
		}
		if e.InPicker {
			b.WriteString("\t\tInPicker:      true,\n")
		}
		fmt.Fprintf(&b, "\t\tContextLength: %d,\n", e.ContextLength)
		if len(e.Thinking) > 0 {
			fmt.Fprintf(&b, "\t\tThinking:      &ThinkingSupport{Levels: []string{%s}},\n", quoteList(e.Thinking))
		}
		b.WriteString("\t},\n")
	}
	b.WriteString("}\n")

	src, err := format.Source([]byte(b.String()))
	if err != nil {
		return nil, fmt.Errorf("gofmt generated source: %w\n%s", err, b.String())
	}
	return src, nil
}

func quoteList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, v := range values {
		quoted = append(quoted, fmt.Sprintf("%q", v))
	}
	return strings.Join(quoted, ", ")
}
