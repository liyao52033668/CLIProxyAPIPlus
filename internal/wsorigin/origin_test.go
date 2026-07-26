package wsorigin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSameOrigin(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		host   string
		origin string
		want   bool
	}{
		{name: "no origin", host: "api.example.com", want: true},
		{name: "same origin", host: "api.example.com", origin: "https://api.example.com", want: true},
		{name: "same origin with port", host: "api.example.com:8443", origin: "https://api.example.com:8443", want: true},
		{name: "different host", host: "api.example.com", origin: "https://attacker.example", want: false},
		{name: "different port", host: "api.example.com:8443", origin: "https://api.example.com", want: false},
		{name: "null origin", host: "api.example.com", origin: "null", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "http://"+tc.host+"/v1/ws", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if got := SameOrigin(req); got != tc.want {
				t.Fatalf("SameOrigin() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestSameOriginRejectsNilRequest(t *testing.T) {
	t.Parallel()
	if SameOrigin(nil) {
		t.Fatal("SameOrigin(nil) = true, want false")
	}
}
