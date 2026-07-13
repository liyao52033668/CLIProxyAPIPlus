package api

import "testing"

func TestNormalizeCORSOrigin(t *testing.T) {
	cases := map[string]string{
		"http://localhost:5173":        "http://localhost:5173",
		"HTTP://LocalHost:5173/":       "http://localhost:5173",
		"localhost:5173":               "http://localhost:5173",
		"https://admin.example.com":    "https://admin.example.com",
		"https://admin.example.com/x":  "https://admin.example.com",
		"":                             "",
		"ftp://example.com":            "",
	}
	for in, want := range cases {
		if got := normalizeCORSOrigin(in); got != want {
			t.Fatalf("normalizeCORSOrigin(%q)=%q want %q", in, got, want)
		}
	}
}

func TestOriginAllowedByList(t *testing.T) {
	allowed := []string{"http://localhost:5173", "https://admin.example.com"}
	if !originAllowedByList("http://localhost:5173", allowed) {
		t.Fatal("expected localhost allowed")
	}
	if !originAllowedByList("HTTP://LOCALHOST:5173", allowed) {
		t.Fatal("expected case-insensitive match")
	}
	if !originAllowedByList("https://admin.example.com/path", allowed) {
		t.Fatal("expected host match ignoring path")
	}
	if originAllowedByList("http://evil.example.com", allowed) {
		t.Fatal("unexpected allow for foreign origin")
	}
	if originAllowedByList("http://localhost:5173", nil) {
		t.Fatal("empty list must deny")
	}
}
