package proto

import (
	"bytes"
	"compress/gzip"
	"errors"
	"testing"
)

func gzipBytes(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// The api5 agent host gzips the end-of-stream trailer (flags 0x03). Parsing the
// compressed bytes as JSON used to fail on the gzip magic and replace the real
// upstream error with a decoding error.
func TestParseConnectEndStreamFrameDecompressesGzipTrailer(t *testing.T) {
	raw := []byte(`{"error":{"code":"resource_exhausted","message":"Error"}}`)
	flags := ConnectEndStreamFlag | ConnectCompressionFlag

	err := ParseConnectEndStreamFrame(flags, gzipBytes(t, raw))
	var connErr *ConnectError
	if !errors.As(err, &connErr) {
		t.Fatalf("expected *ConnectError, got %v", err)
	}
	if connErr.Code != "resource_exhausted" {
		t.Fatalf("Code = %q, want resource_exhausted", connErr.Code)
	}
}

// The api2 agent host sends the same trailer uncompressed (flags 0x02).
func TestParseConnectEndStreamFramePlainTrailer(t *testing.T) {
	raw := []byte(`{"error":{"code":"unauthenticated","message":"nope"}}`)

	err := ParseConnectEndStreamFrame(ConnectEndStreamFlag, raw)
	var connErr *ConnectError
	if !errors.As(err, &connErr) {
		t.Fatalf("expected *ConnectError, got %v", err)
	}
	if connErr.Code != "unauthenticated" {
		t.Fatalf("Code = %q, want unauthenticated", connErr.Code)
	}
}

func TestParseConnectEndStreamFrameNoError(t *testing.T) {
	if err := ParseConnectEndStreamFrame(ConnectEndStreamFlag, []byte(`{}`)); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}
