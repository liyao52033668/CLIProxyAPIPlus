package management

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func newBatchDownloadTestHandler(t *testing.T) *Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	return NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
}

func doBatchDownload(h *Handler, names []string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	query := url.Values{}
	for _, name := range names {
		query.Add("name", name)
	}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/download-batch?"+query.Encode(), nil)
	h.DownloadAuthFilesBatch(ctx)
	return rec
}

func readBatchZip(t *testing.T, rec *httptest.ResponseRecorder) map[string][]byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("response body is not a valid zip: %v", err)
	}
	contents := make(map[string][]byte, len(reader.File))
	for _, file := range reader.File {
		rc, errOpen := file.Open()
		if errOpen != nil {
			t.Fatalf("failed to open zip entry %q: %v", file.Name, errOpen)
		}
		data, errRead := io.ReadAll(rc)
		rc.Close()
		if errRead != nil {
			t.Fatalf("failed to read zip entry %q: %v", file.Name, errRead)
		}
		contents[file.Name] = data
	}
	return contents
}

func TestDownloadAuthFilesBatch_ReturnsZipOfRequestedFiles(t *testing.T) {
	h := newBatchDownloadTestHandler(t)
	authDir := h.cfg.AuthDir
	expected := map[string][]byte{
		"one.json": []byte(`{"type":"codex","n":1}`),
		"two.json": []byte(`{"type":"qoder","n":2}`),
	}
	for name, data := range expected {
		if err := os.WriteFile(filepath.Join(authDir, name), data, 0o600); err != nil {
			t.Fatalf("failed to write auth file: %v", err)
		}
	}

	rec := doBatchDownload(h, []string{"one.json", "two.json", "missing.json"})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); contentType != "application/zip" {
		t.Fatalf("expected application/zip content type, got %q", contentType)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment; filename=\"auth-files_") {
		t.Fatalf("unexpected content disposition: %q", rec.Header().Get("Content-Disposition"))
	}
	if got := rec.Header().Get("X-Auth-Files-Count"); got != "2" {
		t.Fatalf("expected X-Auth-Files-Count 2, got %q", got)
	}
	if got := rec.Header().Get("X-Auth-Files-Missing"); got != `["missing.json"]` {
		t.Fatalf("expected X-Auth-Files-Missing [\"missing.json\"], got %q", got)
	}
	contents := readBatchZip(t, rec)
	if len(contents) != len(expected) {
		t.Fatalf("expected %d zip entries, got %d: %v", len(expected), len(contents), contents)
	}
	for name, want := range expected {
		if got, ok := contents[name]; !ok || string(got) != string(want) {
			t.Fatalf("unexpected content for %q: %q", name, got)
		}
	}
}

func TestDownloadAuthFilesBatch_SkipsUnsafeAndNonJSONNames(t *testing.T) {
	h := newBatchDownloadTestHandler(t)
	authDir := h.cfg.AuthDir
	secret := []byte(`{"secret":true}`)
	if err := os.WriteFile(filepath.Join(filepath.Dir(authDir), "secret.json"), secret, 0o600); err != nil {
		t.Fatalf("failed to write external file: %v", err)
	}
	expected := []byte(`{"type":"kimi"}`)
	if err := os.WriteFile(filepath.Join(authDir, "ok.json"), expected, 0o600); err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	rec := doBatchDownload(h, []string{"../secret.json", `..\secret.json`, "notes.txt", "ok.json"})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	contents := readBatchZip(t, rec)
	if len(contents) != 1 {
		t.Fatalf("expected only ok.json in the archive, got %v", contents)
	}
	if got := contents["ok.json"]; string(got) != string(expected) {
		t.Fatalf("unexpected content for ok.json: %q", got)
	}
	for _, name := range []string{"../secret.json", `..\secret.json`, "notes.txt"} {
		if _, exists := contents[name]; exists {
			t.Fatalf("unsafe or non-json entry %q must not appear in the archive", name)
		}
	}
}

func TestDownloadAuthFilesBatch_NotFoundWhenNothingMatches(t *testing.T) {
	h := newBatchDownloadTestHandler(t)

	rec := doBatchDownload(h, []string{"missing.json"})

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusNotFound, rec.Code, rec.Body.String())
	}
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected a JSON error body, got %q", rec.Body.String())
	}
	if payload.Error == "" {
		t.Fatalf("expected a non-empty error message")
	}
}

func TestDownloadAuthFilesBatch_RejectsEmptyAndOversizedRequests(t *testing.T) {
	h := newBatchDownloadTestHandler(t)

	if rec := doBatchDownload(h, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d for empty request, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}

	// 去重后仍超过上限才会被拒绝，因此用不同长度的名字
	many := make([]string, 0, maxBatchDownloadFiles+1)
	for i := 0; i <= maxBatchDownloadFiles; i++ {
		many = append(many, "file-"+strings.Repeat("a", i+1)+".json")
	}
	if rec := doBatchDownload(h, many); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d for oversized request, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}
