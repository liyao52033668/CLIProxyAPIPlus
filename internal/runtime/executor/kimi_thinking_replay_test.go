package executor

import "testing"

func TestKimiThinkingReplayModelFamily(t *testing.T) {
	cases := []struct {
		model string
		want  string
	}{
		{model: "kimi-k3", want: "k3"},
		{model: "kimi-k3-256k(high)", want: "k3"},
		{model: "kimi-k2.7-code", want: "kimi-for-coding"},
		{model: "kimi-k2.7-code-highspeed", want: "kimi-for-coding-highspeed"},
		{model: "kimi-k2.8", want: "kimi-for-coding"},
		{model: "kimi-k2.8-code", want: "kimi-for-coding"},
		{model: "kimi-k2.8(max)", want: "kimi-for-coding"},
		{model: "kimi-k2.8-code[1m](high)", want: "kimi-for-coding"},
		{model: "kimi-for-coding", want: "kimi-for-coding"},
		{model: "kimi-for-coding-highspeed(high)", want: "kimi-for-coding-highspeed"},
	}
	for _, tc := range cases {
		if got := kimiThinkingReplayModelFamily(tc.model); got != tc.want {
			t.Fatalf("kimiThinkingReplayModelFamily(%q) = %q, want %q", tc.model, got, tc.want)
		}
	}
}
