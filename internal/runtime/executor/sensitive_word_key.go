package executor

import (
	"sort"
	"strings"
)

// sensitiveWordConfigKey builds a deterministic cache key for a
// (words, replacements) configuration pair so a matcher is rebuilt only when the
// effective configuration changes.
func sensitiveWordConfigKey(words []string, replacements map[string]string) string {
	var builder strings.Builder
	builder.WriteString("w:")
	builder.WriteString(strings.Join(words, "\x00"))
	if len(replacements) > 0 {
		keys := make([]string, 0, len(replacements))
		for phrase := range replacements {
			keys = append(keys, phrase)
		}
		sort.Strings(keys)
		builder.WriteString("\x01r:")
		for _, phrase := range keys {
			builder.WriteString(phrase)
			builder.WriteString("\x02")
			builder.WriteString(replacements[phrase])
			builder.WriteString("\x03")
		}
	}
	return builder.String()
}
