package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/minio/minio-go/v7"
)

const (
	codexInspectionSnapshotKey = "management/codex-inspection-latest.json"
	codexInspectionSnapshotID  = "codex-inspection-latest"
)

func (s *GitTokenStore) LoadCodexInspectionSnapshot(_ context.Context) ([]byte, bool, error) {
	if err := s.EnsureRepository(); err != nil {
		return nil, false, err
	}
	path := s.CodexInspectionSnapshotPath()
	if path == "" {
		return nil, false, fmt.Errorf("git store: repository path not configured")
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("git store: read codex inspection snapshot: %w", err)
	}
	return data, true, nil
}

func (s *GitTokenStore) SaveCodexInspectionSnapshot(_ context.Context, data []byte) error {
	if err := s.EnsureRepository(); err != nil {
		return err
	}
	path := s.CodexInspectionSnapshotPath()
	if path == "" {
		return fmt.Errorf("git store: repository path not configured")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := writeCodexInspectionSnapshotFile(path, data); err != nil {
		return fmt.Errorf("git store: write codex inspection snapshot: %w", err)
	}
	rel, err := s.relativeToRepo(path)
	if err != nil {
		return err
	}
	return s.commitAndPushLocked("Update codex inspection snapshot", rel)
}

func (s *GitTokenStore) CodexInspectionSnapshotPath() string {
	repoDir := s.repoDirSnapshot()
	if repoDir == "" {
		return ""
	}
	return filepath.Join(repoDir, codexInspectionSnapshotKey)
}

func (s *ObjectTokenStore) LoadCodexInspectionSnapshot(ctx context.Context) ([]byte, bool, error) {
	if s == nil || s.client == nil {
		return nil, false, nil
	}
	key := s.prefixedKey(codexInspectionSnapshotKey)
	_, err := s.client.StatObject(ctx, s.cfg.Bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if isObjectNotFound(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("object store: stat codex inspection snapshot: %w", err)
	}
	object, err := s.client.GetObject(ctx, s.cfg.Bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, false, fmt.Errorf("object store: fetch codex inspection snapshot: %w", err)
	}
	defer object.Close()
	data, err := io.ReadAll(object)
	if err != nil {
		return nil, false, fmt.Errorf("object store: read codex inspection snapshot: %w", err)
	}
	if err := writeCodexInspectionSnapshotFile(s.CodexInspectionSnapshotPath(), data); err != nil {
		return nil, false, fmt.Errorf("object store: write local codex inspection snapshot: %w", err)
	}
	return data, true, nil
}

func (s *ObjectTokenStore) SaveCodexInspectionSnapshot(ctx context.Context, data []byte) error {
	if s == nil || s.client == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeCodexInspectionSnapshotFile(s.CodexInspectionSnapshotPath(), data); err != nil {
		return fmt.Errorf("object store: write codex inspection snapshot: %w", err)
	}
	return s.putObject(ctx, codexInspectionSnapshotKey, data, "application/json")
}

func (s *ObjectTokenStore) CodexInspectionSnapshotPath() string {
	return filepath.Join(s.spoolRoot, codexInspectionSnapshotKey)
}

func (s *PostgresStore) LoadCodexInspectionSnapshot(ctx context.Context) ([]byte, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, nil
	}
	query := selectContentByIDSQL(s.fullTableName(s.cfg.ConfigTable))
	var content string
	err := s.db.QueryRowContext(ctx, query, codexInspectionSnapshotID).Scan(&content)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("postgres store: load codex inspection snapshot: %w", err)
	}
	data := []byte(content)
	if err := writeCodexInspectionSnapshotFile(s.CodexInspectionSnapshotPath(), data); err != nil {
		return nil, false, fmt.Errorf("postgres store: write local codex inspection snapshot: %w", err)
	}
	return data, true, nil
}

func (s *PostgresStore) SaveCodexInspectionSnapshot(ctx context.Context, data []byte) error {
	if s == nil || s.db == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeCodexInspectionSnapshotFile(s.CodexInspectionSnapshotPath(), data); err != nil {
		return fmt.Errorf("postgres store: write codex inspection snapshot: %w", err)
	}
	query := upsertRecordSQL(s.fullTableName(s.cfg.ConfigTable))
	if _, err := s.db.ExecContext(ctx, query, codexInspectionSnapshotID, string(data)); err != nil {
		return fmt.Errorf("postgres store: upsert codex inspection snapshot: %w", err)
	}
	return nil
}

func (s *PostgresStore) CodexInspectionSnapshotPath() string {
	return filepath.Join(s.spoolRoot, codexInspectionSnapshotKey)
}

func writeCodexInspectionSnapshotFile(path string, data []byte) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
