package thinking

import "testing"

func TestClaudeOutputEffort(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"present", `{"thinking":{"type":"enabled"},"output_config":{"effort":"max"}}`, "max"},
		{"uppercase normalized", `{"output_config":{"effort":"HIGH"}}`, "high"},
		{"whitespace trimmed", `{"output_config":{"effort":"  low  "}}`, "low"},
		{"absent", `{"thinking":{"type":"enabled"}}`, ""},
		{"non-string ignored", `{"output_config":{"effort":5}}`, ""},
		{"empty", `{}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClaudeOutputEffort([]byte(tc.body)); got != tc.want {
				t.Fatalf("ClaudeOutputEffort() = %q, want %q", got, tc.want)
			}
		})
	}
}
