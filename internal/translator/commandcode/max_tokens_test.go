package commandcode

import "testing"

func TestNormalizeMaxTokens(t *testing.T) {
	cases := []struct {
		in   int64
		want int64
	}{
		{in: 0, want: DefaultMaxTokens},
		{in: -1, want: DefaultMaxTokens},
		{in: 1, want: MinMaxTokens},
		{in: 15, want: MinMaxTokens},
		{in: 16, want: 16},
		{in: 32, want: 32},
	}
	for _, tc := range cases {
		if got := NormalizeMaxTokens(tc.in); got != tc.want {
			t.Fatalf("NormalizeMaxTokens(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
