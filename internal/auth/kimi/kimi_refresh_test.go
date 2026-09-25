package kimi

import (
	"net/http"
)

// kimiRoundTripFunc adapts a function into an http.RoundTripper for tests.
type kimiRoundTripFunc func(*http.Request) (*http.Response, error)

func (f kimiRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// resetKimiRefreshGroupForTest resets the refresh single-flight group between tests.
// The local tree performs refreshes without a single-flight group, so this is a no-op
// kept for parity with the upstream test suite.
func resetKimiRefreshGroupForTest() {}
