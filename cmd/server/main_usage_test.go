package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	usageconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/entities"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/repository"
	"gorm.io/gorm"
)

func TestPersistGitStoreUsageDatabasePreservesUsageAcrossRestart(t *testing.T) {
	root := t.TempDir()
	remoteDir := filepath.Join(root, "remote.git")
	if _, err := git.PlainInit(remoteDir, true); err != nil {
		t.Fatalf("init bare remote: %v", err)
	}

	store1 := store.NewGitTokenStore(remoteDir, "", "", "")
	store1.SetBaseDir(filepath.Join(root, "workspace-1", "auths"))
	if err := store1.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository workspace-1: %v", err)
	}

	db1, err := repository.OpenDatabase(usageconfig.Config{SQLitePath: filepath.Join(store1.AuthDir(), "usage.db")})
	if err != nil {
		t.Fatalf("OpenDatabase workspace-1: %v", err)
	}
	cleanupUsageDB(t, db1)

	if _, _, err := repository.InsertUsageEvents(db1, []entities.UsageEvent{{
		EventKey:        "event-1",
		APIGroupKey:     "provider-a",
		Model:           "claude-sonnet",
		Timestamp:       time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		Source:          "source-a",
		AuthIndex:       "2",
		LatencyMS:       321,
		InputTokens:     10,
		OutputTokens:    5,
		ReasoningTokens: 2,
		CachedTokens:    1,
		TotalTokens:     18,
	}}); err != nil {
		t.Fatalf("InsertUsageEvents workspace-1: %v", err)
	}

	if err := persistGitStoreUsageDatabase(store1, db1); err != nil {
		t.Fatalf("persistGitStoreUsageDatabase: %v", err)
	}

	store2 := store.NewGitTokenStore(remoteDir, "", "", "")
	store2.SetBaseDir(filepath.Join(root, "workspace-2", "auths"))
	if err := store2.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository workspace-2: %v", err)
	}

	usageDBPath2 := filepath.Join(store2.AuthDir(), "usage.db")
	if err := restoreGitStoreUsageDatabase(store2, usageDBPath2); err != nil {
		t.Fatalf("restoreGitStoreUsageDatabase: %v", err)
	}

	db2, err := repository.OpenDatabase(usageconfig.Config{SQLitePath: usageDBPath2})
	if err != nil {
		t.Fatalf("OpenDatabase workspace-2: %v", err)
	}
	cleanupUsageDB(t, db2)

	snapshot, err := repository.BuildUsageSnapshot(db2)
	if err != nil {
		t.Fatalf("BuildUsageSnapshot workspace-2: %v", err)
	}
	if snapshot.TotalRequests != 1 {
		t.Fatalf("total_requests after restart = %d, want 1", snapshot.TotalRequests)
	}
}

func TestPersistObjectStoreUsageDatabasePreservesUsageAcrossRestart(t *testing.T) {
	root := t.TempDir()
	server := newObjectStoreTestServer(t)
	store1, err := store.NewObjectTokenStore(store.ObjectStoreConfig{
		Endpoint:  server.Endpoint(),
		Bucket:    "usage-test",
		AccessKey: "test-access",
		SecretKey: "test-secret",
		LocalRoot: filepath.Join(root, "objectstore-1"),
		UseSSL:    false,
		PathStyle: true,
	})
	if err != nil {
		t.Fatalf("NewObjectTokenStore store1: %v", err)
	}
	if err := store1.Bootstrap(context.Background(), ""); err != nil {
		t.Fatalf("Bootstrap store1: %v", err)
	}

	usagePath1 := filepath.Join(store1.AuthDir(), "usage.db")
	db1, err := repository.OpenDatabase(usageconfig.Config{SQLitePath: usagePath1})
	if err != nil {
		t.Fatalf("OpenDatabase store1: %v", err)
	}
	cleanupUsageDB(t, db1)

	if _, _, err := repository.InsertUsageEvents(db1, []entities.UsageEvent{{
		EventKey:        "event-1",
		APIGroupKey:     "provider-a",
		Model:           "claude-sonnet",
		Timestamp:       time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		Source:          "source-a",
		AuthIndex:       "2",
		LatencyMS:       321,
		InputTokens:     10,
		OutputTokens:    5,
		ReasoningTokens: 2,
		CachedTokens:    1,
		TotalTokens:     18,
	}}); err != nil {
		t.Fatalf("InsertUsageEvents store1: %v", err)
	}

	if err := persistObjectStoreUsageDatabase(store1, db1); err != nil {
		t.Fatalf("persistObjectStoreUsageDatabase: %v", err)
	}

	store2, err := store.NewObjectTokenStore(store.ObjectStoreConfig{
		Endpoint:  server.Endpoint(),
		Bucket:    "usage-test",
		AccessKey: "test-access",
		SecretKey: "test-secret",
		LocalRoot: filepath.Join(root, "objectstore-2"),
		UseSSL:    false,
		PathStyle: true,
	})
	if err != nil {
		t.Fatalf("NewObjectTokenStore store2: %v", err)
	}
	if err := store2.Bootstrap(context.Background(), ""); err != nil {
		t.Fatalf("Bootstrap store2: %v", err)
	}

	restoredUsagePath := filepath.Join(root, "restored", "usage.db")
	if err := restoreObjectStoreUsageDatabase(store2, restoredUsagePath); err != nil {
		t.Fatalf("restoreObjectStoreUsageDatabase: %v", err)
	}

	db2, err := repository.OpenDatabase(usageconfig.Config{SQLitePath: restoredUsagePath})
	if err != nil {
		t.Fatalf("OpenDatabase store2: %v", err)
	}
	cleanupUsageDB(t, db2)

	snapshot, err := repository.BuildUsageSnapshot(db2)
	if err != nil {
		t.Fatalf("BuildUsageSnapshot store2: %v", err)
	}
	if snapshot.TotalRequests != 1 {
		t.Fatalf("total_requests after restart = %d, want 1", snapshot.TotalRequests)
	}
}

func TestInitializeUsageDatabaseUsesPostgresOpenerForPostgresStore(t *testing.T) {
	originalPostgresOpener := openRuntimePostgresUsageDatabase
	originalSQLiteOpener := openRuntimeSQLiteUsageDatabase
	t.Cleanup(func() {
		openRuntimePostgresUsageDatabase = originalPostgresOpener
		openRuntimeSQLiteUsageDatabase = originalSQLiteOpener
	})

	var postgresCalled bool
	var sqliteCalled bool
	openRuntimePostgresUsageDatabase = func(dsn string) (*gorm.DB, error) {
		postgresCalled = true
		if dsn != "postgres://usage-test" {
			t.Fatalf("postgres dsn = %q, want %q", dsn, "postgres://usage-test")
		}
		return &gorm.DB{}, nil
	}
	openRuntimeSQLiteUsageDatabase = func(string) (*gorm.DB, error) {
		sqliteCalled = true
		return &gorm.DB{}, nil
	}

	db, err := initializeUsageDatabase(filepath.Join(t.TempDir(), "auths"), true, "postgres://usage-test", false, nil, false, nil)
	if err != nil {
		t.Fatalf("initializeUsageDatabase: %v", err)
	}
	if db == nil {
		t.Fatal("expected postgres usage database")
	}
	if !postgresCalled {
		t.Fatal("expected postgres opener to be called")
	}
	if sqliteCalled {
		t.Fatal("did not expect sqlite opener to be called")
	}
}

func TestPrepareRuntimeAuthDirAndUsageDatabaseResolvesAuthDirBeforeInitialization(t *testing.T) {
	resolvedAuthDir := filepath.Join(t.TempDir(), "resolved-auths")
	originalResolveAuthDir := resolveRuntimeAuthDir
	originalInitializeUsageDatabase := initializeRuntimeUsageDatabase
	t.Cleanup(func() {
		resolveRuntimeAuthDir = originalResolveAuthDir
		initializeRuntimeUsageDatabase = originalInitializeUsageDatabase
	})

	var resolveCalled bool
	var observedAuthDir string
	var initializeCalls int
	resolveRuntimeAuthDir = func(authDir string) (string, error) {
		if authDir != config.DefaultAuthDir {
			t.Fatalf("authDir passed to ResolveAuthDir = %q, want %q", authDir, config.DefaultAuthDir)
		}
		resolveCalled = true
		return resolvedAuthDir, nil
	}
	initializeRuntimeUsageDatabase = func(dataDir string, usePostgresStore bool, pgStoreDSN string, useGitStore bool, gitStoreInst *store.GitTokenStore, useObjectStore bool, objectStoreInst *store.ObjectTokenStore) (*gorm.DB, error) {
		initializeCalls++
		if !resolveCalled {
			t.Fatal("initializeUsageDatabase called before auth dir was resolved")
		}
		observedAuthDir = dataDir
		if dataDir != resolvedAuthDir {
			t.Fatalf("initializeUsageDatabase authDir = %q, want %q", dataDir, resolvedAuthDir)
		}
		return &gorm.DB{}, nil
	}

	cfg := &config.Config{
		AuthDir:                config.DefaultAuthDir,
		UsageStatisticsEnabled: true,
	}
	usageDB, authDirResolved, err := prepareRuntimeAuthDirAndUsageDatabase(cfg, false, "", false, nil, false, nil)
	if err != nil {
		t.Fatalf("prepareRuntimeAuthDirAndUsageDatabase: %v", err)
	}
	if usageDB == nil {
		t.Fatal("expected usage database to be initialized")
	}
	if !authDirResolved {
		t.Fatal("expected auth dir resolution to succeed")
	}
	if initializeCalls != 1 {
		t.Fatalf("initializeUsageDatabase called %d times, want 1", initializeCalls)
	}
	if observedAuthDir != resolvedAuthDir {
		t.Fatalf("observed auth dir = %q, want %q", observedAuthDir, resolvedAuthDir)
	}
	if cfg.AuthDir != resolvedAuthDir {
		t.Fatalf("cfg.AuthDir = %q, want %q", cfg.AuthDir, resolvedAuthDir)
	}
}

func cleanupUsageDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	if db == nil {
		return
	}
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err != nil {
			t.Errorf("db.DB: %v", err)
			return
		}
		if err := closeUsageSQLDB(sqlDB); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
}

type objectStoreTestServer struct {
	server  *httptest.Server
	objects map[string][]byte
	buckets map[string]bool
	mu      sync.Mutex
}

func newObjectStoreTestServer(t *testing.T) *objectStoreTestServer {
	t.Helper()
	stub := &objectStoreTestServer{
		objects: make(map[string][]byte),
		buckets: make(map[string]bool),
	}
	stub.server = httptest.NewServer(http.HandlerFunc(stub.handle))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *objectStoreTestServer) Endpoint() string {
	return strings.TrimPrefix(s.server.URL, "http://")
}

func (s *objectStoreTestServer) handle(w http.ResponseWriter, r *http.Request) {
	bucket, key := bucketAndKey(r.URL.Path)
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	switch {
	case r.Method == http.MethodHead && key == "":
		s.handleHeadBucket(w, bucket)
	case r.Method == http.MethodPut && key == "":
		s.handleCreateBucket(w, bucket)
	case r.Method == http.MethodGet && hasQueryKey(query, "location"):
		s.handleGetBucketLocation(w, bucket)
	case r.Method == http.MethodGet && query.Get("list-type") == "2":
		s.writeListBucketResult(w, bucket, query)
	case r.Method == http.MethodHead:
		s.handleStatObject(w, bucket, key)
	case r.Method == http.MethodGet:
		s.handleGetObject(w, bucket, key)
	case r.Method == http.MethodPut:
		s.handlePutObject(w, r, bucket, key)
	case r.Method == http.MethodDelete:
		s.handleDeleteObject(w, bucket, key)
	default:
		http.Error(w, "unexpected request", http.StatusNotImplemented)
	}
}

func bucketAndKey(path string) (bucket string, key string) {
	trimmed := strings.TrimPrefix(path, "/")
	if trimmed == "" {
		return "", ""
	}
	parts := strings.SplitN(trimmed, "/", 2)
	bucket = parts[0]
	if len(parts) == 2 {
		key = parts[1]
	}
	return bucket, key
}

func (s *objectStoreTestServer) handleHeadBucket(w http.ResponseWriter, bucket string) {
	if !s.bucketExists(bucket) {
		s.writeNoSuchBucket(w)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *objectStoreTestServer) handleCreateBucket(w http.ResponseWriter, bucket string) {
	s.mu.Lock()
	s.buckets[bucket] = true
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (s *objectStoreTestServer) handleGetBucketLocation(w http.ResponseWriter, bucket string) {
	if !s.bucketExists(bucket) {
		s.writeNoSuchBucket(w)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></LocationConstraint>`))
}

func (s *objectStoreTestServer) handleStatObject(w http.ResponseWriter, bucket, key string) {
	if !s.bucketExists(bucket) {
		s.writeNoSuchBucket(w)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[key]
	if !ok {
		s.writeNoSuchKey(w)
		return
	}
	w.Header().Set("ETag", "\"test-etag\"")
	w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
}

func (s *objectStoreTestServer) handleGetObject(w http.ResponseWriter, bucket, key string) {
	if !s.bucketExists(bucket) {
		s.writeNoSuchBucket(w)
		return
	}
	s.mu.Lock()
	data, ok := s.objects[key]
	s.mu.Unlock()
	if !ok {
		s.writeNoSuchKey(w)
		return
	}
	w.Header().Set("ETag", "\"test-etag\"")
	w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *objectStoreTestServer) handlePutObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	if !s.bucketExists(bucket) {
		s.writeNoSuchBucket(w)
		return
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") || looksLikeAWSChunked(data) {
		data, err = decodeChunkedObjectPayload(data)
		if err != nil {
			http.Error(w, fmt.Sprintf("decode chunked payload: %v", err), http.StatusBadRequest)
			return
		}
	}
	s.mu.Lock()
	s.objects[key] = data
	s.mu.Unlock()
	w.Header().Set("ETag", "\"test-etag\"")
	w.WriteHeader(http.StatusOK)
}

func (s *objectStoreTestServer) handleDeleteObject(w http.ResponseWriter, bucket, key string) {
	if !s.bucketExists(bucket) {
		s.writeNoSuchBucket(w)
		return
	}
	s.mu.Lock()
	delete(s.objects, key)
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *objectStoreTestServer) writeListBucketResult(w http.ResponseWriter, bucket string, query url.Values) {
	type contents struct {
		Key string `xml:"Key"`
	}
	type listBucketResult struct {
		XMLName  xml.Name   `xml:"ListBucketResult"`
		Xmlns    string     `xml:"xmlns,attr,omitempty"`
		Name     string     `xml:"Name"`
		Prefix   string     `xml:"Prefix"`
		KeyCount int        `xml:"KeyCount"`
		MaxKeys  int        `xml:"MaxKeys"`
		IsTrunc  bool       `xml:"IsTruncated"`
		Contents []contents `xml:"Contents"`
	}

	if !s.bucketExists(bucket) {
		s.writeNoSuchBucket(w)
		return
	}

	prefix := query.Get("prefix")
	result := listBucketResult{
		Xmlns:   "http://s3.amazonaws.com/doc/2006-03-01/",
		Name:    "usage-test",
		Prefix:  prefix,
		MaxKeys: 1000,
	}
	s.mu.Lock()
	for key := range s.objects {
		if strings.HasPrefix(key, prefix) {
			result.Contents = append(result.Contents, contents{Key: key})
		}
	}
	result.KeyCount = len(result.Contents)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/xml")
	_ = xml.NewEncoder(w).Encode(result)
}

func (s *objectStoreTestServer) bucketExists(bucket string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buckets[bucket]
}

func hasQueryKey(query url.Values, key string) bool {
	_, ok := query[key]
	return ok
}

func (s *objectStoreTestServer) writeNoSuchBucket(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte("<Error><Code>NoSuchBucket</Code></Error>"))
}

func (s *objectStoreTestServer) writeNoSuchKey(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte("<Error><Code>NoSuchKey</Code></Error>"))
}

func looksLikeAWSChunked(data []byte) bool {
	lineEnd := bytes.IndexByte(data, '\n')
	if lineEnd <= 0 {
		return false
	}
	line := strings.TrimSpace(string(data[:lineEnd]))
	if line == "" {
		return false
	}
	if idx := strings.Index(line, ";"); idx >= 0 {
		line = line[:idx]
	}
	for _, r := range line {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func decodeChunkedObjectPayload(data []byte) ([]byte, error) {
	reader := bufio.NewReader(bytes.NewReader(data))
	var decoded bytes.Buffer
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		sizeHex := line
		if idx := strings.Index(sizeHex, ";"); idx >= 0 {
			sizeHex = sizeHex[:idx]
		}
		var size int
		if _, err := fmt.Sscanf(sizeHex, "%x", &size); err != nil {
			return nil, err
		}
		if size == 0 {
			for {
				trailer, err := reader.ReadString('\n')
				if err != nil {
					return nil, err
				}
				if strings.TrimSpace(trailer) == "" {
					return decoded.Bytes(), nil
				}
			}
		}
		chunk := make([]byte, size)
		if _, err := io.ReadFull(reader, chunk); err != nil {
			return nil, err
		}
		decoded.Write(chunk)
		if _, err := reader.ReadString('\n'); err != nil {
			return nil, err
		}
	}
}
