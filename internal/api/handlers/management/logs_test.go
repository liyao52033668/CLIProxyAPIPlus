package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func newLogsTestHandler(dir string, loggingToFile bool) *Handler {
	h := NewHandlerWithoutConfigFilePath(&config.Config{LoggingToFile: loggingToFile}, nil)
	h.SetLogDirectory(dir)
	return h
}

func TestGetRequestLogByID_SelectsLatestOnReusedID(t *testing.T) {
	dir := t.TempDir()
	h := newLogsTestHandler(dir, true)

	oldPath := filepath.Join(dir, "v1_chat_completions-2026-09-23T100000-00000000.log")
	newPath := filepath.Join(dir, "v1_chat_completions-2026-09-23T110000-00000000.log")

	if errWrite := os.WriteFile(oldPath, []byte("old log content"), 0o644); errWrite != nil {
		t.Fatalf("write old log: %v", errWrite)
	}
	oldTime := time.Now().Add(-time.Hour)
	if errChtimes := os.Chtimes(oldPath, oldTime, oldTime); errChtimes != nil {
		t.Fatalf("chtimes old log: %v", errChtimes)
	}

	if errWrite := os.WriteFile(newPath, []byte("new log content"), 0o644); errWrite != nil {
		t.Fatalf("write new log: %v", errWrite)
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "00000000"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/logs/request/00000000", nil)

	h.GetRequestLogByID(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}

	if body := rec.Body.String(); body != "new log content" {
		t.Fatalf("body = %q, want %q", body, "new log content")
	}
}

func TestGetRequestLogByID_SelectsLatestOnSameModTimeTieBreak(t *testing.T) {
	dir := t.TempDir()
	h := newLogsTestHandler(dir, true)

	file1 := filepath.Join(dir, "v1_chat_completions-2026-09-23T100000-00000000.log")
	file2 := filepath.Join(dir, "v1_chat_completions-2026-09-23T100000_1-00000000.log")

	if errWrite := os.WriteFile(file1, []byte("file1 content"), 0o644); errWrite != nil {
		t.Fatalf("write file1: %v", errWrite)
	}
	if errWrite := os.WriteFile(file2, []byte("file2 content"), 0o644); errWrite != nil {
		t.Fatalf("write file2: %v", errWrite)
	}
	sameTime := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	if errChtimes := os.Chtimes(file1, sameTime, sameTime); errChtimes != nil {
		t.Fatalf("chtimes file1: %v", errChtimes)
	}
	if errChtimes := os.Chtimes(file2, sameTime, sameTime); errChtimes != nil {
		t.Fatalf("chtimes file2: %v", errChtimes)
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "00000000"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/logs/request/00000000", nil)

	h.GetRequestLogByID(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}

	if body := rec.Body.String(); body != "file2 content" {
		t.Fatalf("body = %q, want %q", body, "file2 content")
	}
}

func TestGetRequestLogByID_SelectsLatestOnSameModTimeSequence9Vs10(t *testing.T) {
	dir := t.TempDir()
	h := newLogsTestHandler(dir, true)

	file9 := filepath.Join(dir, "v1_chat_completions-2026-09-23T100000_9-00000000.log")
	file10 := filepath.Join(dir, "v1_chat_completions-2026-09-23T100000_10-00000000.log")

	if errWrite := os.WriteFile(file9, []byte("file9 content"), 0o644); errWrite != nil {
		t.Fatalf("write file9: %v", errWrite)
	}
	if errWrite := os.WriteFile(file10, []byte("file10 content"), 0o644); errWrite != nil {
		t.Fatalf("write file10: %v", errWrite)
	}
	sameTime := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	if errChtimes := os.Chtimes(file9, sameTime, sameTime); errChtimes != nil {
		t.Fatalf("chtimes file9: %v", errChtimes)
	}
	if errChtimes := os.Chtimes(file10, sameTime, sameTime); errChtimes != nil {
		t.Fatalf("chtimes file10: %v", errChtimes)
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "00000000"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/logs/request/00000000", nil)

	h.GetRequestLogByID(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}

	if body := rec.Body.String(); body != "file10 content" {
		t.Fatalf("body = %q, want %q (numerical sequence 10 must beat 9)", body, "file10 content")
	}
}

func TestGetRequestLogByID_SelectsLatestAcrossTimestampsWithSequenceOnModTimeTie(t *testing.T) {
	dir := t.TempDir()
	h := newLogsTestHandler(dir, true)

	oldFileWithSeq := filepath.Join(dir, "v1_chat_completions-2026-09-23T100000_9-00000000.log")
	newFileWithoutSeq := filepath.Join(dir, "v1_chat_completions-2026-09-23T110000-00000000.log")

	if errWrite := os.WriteFile(oldFileWithSeq, []byte("old with seq content"), 0o644); errWrite != nil {
		t.Fatalf("write oldFileWithSeq: %v", errWrite)
	}
	if errWrite := os.WriteFile(newFileWithoutSeq, []byte("new without seq content"), 0o644); errWrite != nil {
		t.Fatalf("write newFileWithoutSeq: %v", errWrite)
	}
	sameTime := time.Date(2026, 9, 23, 11, 0, 0, 0, time.UTC)
	if errChtimes := os.Chtimes(oldFileWithSeq, sameTime, sameTime); errChtimes != nil {
		t.Fatalf("chtimes oldFileWithSeq: %v", errChtimes)
	}
	if errChtimes := os.Chtimes(newFileWithoutSeq, sameTime, sameTime); errChtimes != nil {
		t.Fatalf("chtimes newFileWithoutSeq: %v", errChtimes)
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "00000000"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/logs/request/00000000", nil)

	h.GetRequestLogByID(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}

	if body := rec.Body.String(); body != "new without seq content" {
		t.Fatalf("body = %q, want %q (timestamp T110000 must beat T100000 even if older had higher sequence)", body, "new without seq content")
	}
}

func TestGetRequestLogByID_SelectsLatestCrossYearBoundaryOnModTimeTie(t *testing.T) {
	dir := t.TempDir()
	h := newLogsTestHandler(dir, true)

	oldYearFile := filepath.Join(dir, "v1_chat_completions-2026-12-31T235959-00000000.log")
	newYearFile := filepath.Join(dir, "v1_chat_completions-2027-01-01T000000-00000000.log")

	if errWrite := os.WriteFile(oldYearFile, []byte("2026 content"), 0o644); errWrite != nil {
		t.Fatalf("write oldYearFile: %v", errWrite)
	}
	if errWrite := os.WriteFile(newYearFile, []byte("2027 content"), 0o644); errWrite != nil {
		t.Fatalf("write newYearFile: %v", errWrite)
	}
	sameTime := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if errChtimes := os.Chtimes(oldYearFile, sameTime, sameTime); errChtimes != nil {
		t.Fatalf("chtimes oldYearFile: %v", errChtimes)
	}
	if errChtimes := os.Chtimes(newYearFile, sameTime, sameTime); errChtimes != nil {
		t.Fatalf("chtimes newYearFile: %v", errChtimes)
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "00000000"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/logs/request/00000000", nil)

	h.GetRequestLogByID(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}

	if body := rec.Body.String(); body != "2027 content" {
		t.Fatalf("body = %q, want %q (2027-01-01 must beat 2026-12-31 on mod time tie)", body, "2027 content")
	}
}

func TestGetLogs_BeforeFiltersByTimestamp(t *testing.T) {
	dir := t.TempDir()
	h := newLogsTestHandler(dir, true)

	content := "" +
		"2026-10-16 10:00:01 early line\n" +
		"2026-10-16 10:00:02 middle line\n" +
		"2026-10-16 10:00:03 late line\n"
	if errWrite := os.WriteFile(filepath.Join(dir, "main.log"), []byte(content), 0o644); errWrite != nil {
		t.Fatalf("write main.log: %v", errWrite)
	}

	before := time.Date(2026, 10, 16, 10, 0, 2, 0, time.Local).Unix()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/logs?before="+strconv.FormatInt(before, 10), nil)

	h.GetLogs(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Lines []string `json:"lines"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &body); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v", errUnmarshal)
	}

	if len(body.Lines) != 2 {
		t.Fatalf("lines = %v, want 2 lines before cutoff", body.Lines)
	}
	if body.Lines[0] != "2026-10-16 10:00:01 early line" || body.Lines[1] != "2026-10-16 10:00:02 middle line" {
		t.Fatalf("lines = %v, want early+middle lines", body.Lines)
	}
}

func TestGetLogs_AfterAndBeforeWindow(t *testing.T) {
	dir := t.TempDir()
	h := newLogsTestHandler(dir, true)

	content := "" +
		"2026-10-16 10:00:01 early line\n" +
		"2026-10-16 10:00:02 in window\n" +
		"2026-10-16 10:00:03 in window too\n" +
		"2026-10-16 10:00:04 late line\n"
	if errWrite := os.WriteFile(filepath.Join(dir, "main.log"), []byte(content), 0o644); errWrite != nil {
		t.Fatalf("write main.log: %v", errWrite)
	}

	after := time.Date(2026, 10, 16, 10, 0, 1, 0, time.Local).Unix()
	before := time.Date(2026, 10, 16, 10, 0, 3, 0, time.Local).Unix()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet,
		"/v0/management/logs?after="+strconv.FormatInt(after, 10)+"&before="+strconv.FormatInt(before, 10), nil)

	h.GetLogs(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Lines []string `json:"lines"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &body); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v", errUnmarshal)
	}

	if len(body.Lines) != 2 {
		t.Fatalf("lines = %v, want 2 lines inside window", body.Lines)
	}
	if body.Lines[0] != "2026-10-16 10:00:02 in window" || body.Lines[1] != "2026-10-16 10:00:03 in window too" {
		t.Fatalf("lines = %v, want window lines only", body.Lines)
	}
}

func TestGetLogs_BeforeKeepsContinuationLines(t *testing.T) {
	dir := t.TempDir()
	h := newLogsTestHandler(dir, true)

	// 无时间戳的续行跟随前一行的时间戳归属：before 截止点之后的续行不能泄漏
	content := "" +
		"2026-10-16 10:00:01 block one\n" +
		"continuation of block one\n" +
		"2026-10-16 10:00:03 block two\n" +
		"continuation of block two\n"
	if errWrite := os.WriteFile(filepath.Join(dir, "main.log"), []byte(content), 0o644); errWrite != nil {
		t.Fatalf("write main.log: %v", errWrite)
	}

	before := time.Date(2026, 10, 16, 10, 0, 2, 0, time.Local).Unix()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/logs?before="+strconv.FormatInt(before, 10), nil)

	h.GetLogs(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Lines []string `json:"lines"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &body); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v", errUnmarshal)
	}

	want := []string{
		"2026-10-16 10:00:01 block one",
		"continuation of block one",
	}
	if len(body.Lines) != len(want) {
		t.Fatalf("lines = %v, want %v", body.Lines, want)
	}
	for i, line := range want {
		if body.Lines[i] != line {
			t.Fatalf("lines[%d] = %q, want %q", i, body.Lines[i], line)
		}
	}
}
