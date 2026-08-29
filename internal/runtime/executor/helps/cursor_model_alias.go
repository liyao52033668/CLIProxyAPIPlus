package helps

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	log "github.com/sirupsen/logrus"
)

// Anthropic-style model name resolution for the Cursor provider.
//
// Cursor exposes its own model IDs through GetUsableModels (naming varies
// between catalog generations, e.g. "sonnet-4.6" or "claude-4-sonnet").
// Clients like Claude Code request Anthropic-style names ("claude-sonnet-4-5-20250929",
// "claude-haiku-4-5", "...-thinking") that may not exist upstream. This mirrors
// the model-mapping approach of community Cursor proxies: normalize the requested
// name, then map it onto the closest available Cursor model from the live
// catalog. Names that cannot be mapped are passed through unchanged so upstream
// errors stay visible. Nothing is hardcoded here: the catalog is the source of
// truth for both the target ID scheme and availability.

var (
	cursorTrailingVN   = regexp.MustCompile(`-v\d+$`)
	cursorTrailingDate = regexp.MustCompile(`-\d{8}$`)
	cursorVersionToken = regexp.MustCompile(`\d+(?:[.-]\d+)?`)
)

// cursorModelShape is the parsed form of a claude-family model name.
type cursorModelShape struct {
	family   string // "opus", "sonnet" or "haiku"
	version  string // dotted form, e.g. "4.5"; "" when absent
	thinking bool
}

// normalizeCursorModelName strips dated snapshots, "-vN" suffixes and casing so
// lookups are stable across naming styles.
func normalizeCursorModelName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	n = cursorTrailingVN.ReplaceAllString(n, "")
	n = cursorTrailingDate.ReplaceAllString(n, "")
	return n
}

// parseCursorModelShape extracts the claude family, version and thinking flag
// from a model name. ok is false for names outside the claude families.
func parseCursorModelShape(raw string) (cursorModelShape, bool) {
	n := normalizeCursorModelName(raw)
	shape := cursorModelShape{thinking: strings.Contains(n, "thinking")}
	base := strings.ReplaceAll(n, "thinking", "")
	base = strings.Trim(base, "-")
	switch {
	case strings.Contains(base, "opus"):
		shape.family = "opus"
	case strings.Contains(base, "sonnet"):
		shape.family = "sonnet"
	case strings.Contains(base, "haiku"):
		shape.family = "haiku"
	default:
		return cursorModelShape{}, false
	}
	// Drop the provider and family words so the first numeric token is the
	// version: "claude-sonnet-4-5" -> "4.5", "claude-4-sonnet" -> "4",
	// "claude-3.5-sonnet" -> "3.5".
	replacer := strings.NewReplacer("claude", "", shape.family, "")
	cleaned := strings.Trim(replacer.Replace(base), "-")
	if m := cursorVersionToken.FindString(cleaned); m != "" {
		shape.version = strings.ReplaceAll(m, "-", ".")
	}
	return shape, true
}

// cursorVersionParts splits a dotted version into comparable ints; missing
// minor reads as 0.
func cursorVersionParts(version string) (int, int) {
	major, minor := 0, 0
	for i, part := range strings.SplitN(version, ".", 2) {
		v, err := strconv.Atoi(part)
		if err != nil {
			continue
		}
		if i == 0 {
			major = v
		} else {
			minor = v
		}
	}
	return major, minor
}

// cursorVersionLess orders versions ascending ("4" < "4.5" < "4.6").
func cursorVersionLess(a, b string) bool {
	aMajor, aMinor := cursorVersionParts(a)
	bMajor, bMinor := cursorVersionParts(b)
	if aMajor != bMajor {
		return aMajor < bMajor
	}
	return aMinor < bMinor
}

// ResolveCursorModelID maps a requested model name onto the closest available
// Cursor model ID. catalog holds the currently available Cursor model IDs; it
// may be empty (registry not populated yet), in which case the name is returned
// unchanged. "default" and "auto" always pass through.
func ResolveCursorModelID(requested string, catalog []string) string {
	name := strings.TrimSpace(requested)
	if name == "" {
		return name
	}
	switch strings.ToLower(name) {
	case "default", "auto":
		return name
	}
	if len(catalog) == 0 {
		return name
	}
	// Exact (case-insensitive) hit — send as requested.
	for _, id := range catalog {
		if strings.EqualFold(strings.TrimSpace(id), name) {
			return name
		}
	}
	// Normalized hit: a dated snapshot or "-vN" suffix of a real catalog ID.
	norm := normalizeCursorModelName(name)
	for _, id := range catalog {
		if normalizeCursorModelName(id) == norm {
			log.Debugf("cursor: normalized model %q -> catalog id %q", name, id)
			return id
		}
	}
	// Anthropic-style mapping onto the live catalog.
	want, ok := parseCursorModelShape(name)
	if !ok {
		return name
	}
	if want.family == "haiku" {
		// Cursor has no haiku tier; substitute sonnet like community proxies do.
		want.family = "sonnet"
	}
	type candidate struct {
		id       string
		version  string
		thinking bool
	}
	var cands []candidate
	for _, id := range catalog {
		shape, okShape := parseCursorModelShape(id)
		if !okShape || shape.family != want.family {
			continue
		}
		cands = append(cands, candidate{id: id, version: shape.version, thinking: shape.thinking})
	}
	if len(cands) == 0 {
		return name
	}
	// Version preference: exact dotted match > same major (newest minor wins in
	// the tiebreak) > newest overall. A major-only request ("claude-sonnet-4")
	// means "latest 4.x", so catalog "4" and "4.x" entries compete equally.
	// Within a version score, prefer the requested thinking mode.
	versionMatch := func(c candidate) int {
		wantMajor, _ := cursorVersionParts(want.version)
		cMajor, _ := cursorVersionParts(c.version)
		switch {
		case want.version != "" && c.version == want.version && strings.Contains(want.version, "."):
			return 2
		case cMajor == wantMajor:
			return 1
		default:
			return 0
		}
	}
	best := candidate{}
	found := false
	for _, c := range cands {
		if !found {
			best, found = c, true
			continue
		}
		bestScore, cScore := versionMatch(best), versionMatch(c)
		switch {
		case cScore != bestScore:
			if cScore > bestScore {
				best = c
			}
		case c.thinking != best.thinking:
			if c.thinking == want.thinking {
				best = c
			}
		case cursorVersionLess(best.version, c.version):
			best = c
		}
	}
	if !found || best.id == "" {
		return name
	}
	log.Debugf("cursor: mapped model %q -> %q", name, best.id)
	return best.id
}

// ExpandCursorModelAliases appends Anthropic-style aliases ("claude-sonnet-4-6",
// "claude-sonnet-4-6-thinking", ...) for the Cursor claude-family models in the
// catalog so clients using Anthropic naming see real options in model listings.
// An alias is only advertised for an existing target model; existing IDs are
// never duplicated. Versions without a minor component are skipped to keep the
// listing unambiguous.
func ExpandCursorModelAliases(models []*registry.ModelInfo) []*registry.ModelInfo {
	if len(models) == 0 {
		return models
	}
	existing := make(map[string]bool, len(models))
	for _, m := range models {
		if m != nil && m.ID != "" {
			existing[strings.ToLower(m.ID)] = true
		}
	}
	expanded := models
	for _, m := range models {
		if m == nil || m.ID == "" {
			continue
		}
		shape, ok := parseCursorModelShape(m.ID)
		if !ok || shape.version == "" || !strings.Contains(shape.version, ".") {
			continue
		}
		alias := "claude-" + shape.family + "-" + strings.ReplaceAll(shape.version, ".", "-")
		if shape.thinking {
			alias += "-thinking"
		}
		if existing[strings.ToLower(alias)] {
			continue
		}
		aliasInfo := *m
		aliasInfo.ID = alias
		aliasInfo.Object = "model"
		aliasInfo.OwnedBy = "cursor"
		aliasInfo.Type = "cursor"
		aliasInfo.Description = "Anthropic-style alias of cursor model " + m.ID
		expanded = append(expanded, &aliasInfo)
		existing[strings.ToLower(alias)] = true
		log.Debugf("cursor: advertising model alias %q -> %q", alias, m.ID)
	}
	return expanded
}
