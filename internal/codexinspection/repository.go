package codexinspection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type SnapshotRepository interface {
	Load(context.Context) (LatestSnapshot, error)
	Save(context.Context, LatestSnapshot) error
}

type SnapshotExternalStore interface {
	CodexInspectionSnapshotPath() string
	LoadCodexInspectionSnapshot(context.Context) ([]byte, bool, error)
	SaveCodexInspectionSnapshot(context.Context, []byte) error
}

type FileSnapshotRepository struct {
	path     string
	external SnapshotExternalStore
}

func NewFileSnapshotRepository(path string, external ...SnapshotExternalStore) *FileSnapshotRepository {
	var snapshotStore SnapshotExternalStore
	if len(external) > 0 {
		snapshotStore = external[0]
	}
	return &FileSnapshotRepository{path: path, external: snapshotStore}
}

func (r *FileSnapshotRepository) Load(ctx context.Context) (LatestSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return LatestSnapshot{}, err
	}

	data, err := r.loadSnapshotData(ctx)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultSnapshot(), nil
	}
	if err != nil {
		return LatestSnapshot{}, err
	}

	var snapshot LatestSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return LatestSnapshot{}, fmt.Errorf("decode snapshot file: %w", err)
	}

	snapshot.Settings = applyDefaultSettings(snapshot.Settings)
	if snapshot.Results == nil {
		snapshot.Results = []InspectionResultItem{}
	}
	if snapshot.ActionLogs == nil {
		snapshot.ActionLogs = []InspectionActionLog{}
	}

	return snapshot, nil
}

func (r *FileSnapshotRepository) Save(ctx context.Context, snapshot LatestSnapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if snapshot.Results == nil {
		snapshot.Results = []InspectionResultItem{}
	}
	if snapshot.ActionLogs == nil {
		snapshot.ActionLogs = []InspectionActionLog{}
	}

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode snapshot file: %w", err)
	}
	data = append(data, '\n')

	if err := r.writeSnapshotFile(data); err != nil {
		return err
	}
	if r.external != nil {
		if err := r.external.SaveCodexInspectionSnapshot(ctx, data); err != nil {
			return fmt.Errorf("persist snapshot to external store: %w", err)
		}
	}

	return nil
}

func (r *FileSnapshotRepository) loadSnapshotData(ctx context.Context) ([]byte, error) {
	data, err := os.ReadFile(r.path)
	if err == nil {
		return data, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read snapshot file: %w", err)
	}
	if r.external == nil {
		return nil, err
	}
	data, ok, err := r.external.LoadCodexInspectionSnapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("load snapshot from external store: %w", err)
	}
	if !ok {
		return nil, os.ErrNotExist
	}
	if err := r.writeSnapshotFile(data); err != nil {
		return nil, err
	}
	return data, nil
}

func (r *FileSnapshotRepository) writeSnapshotFile(data []byte) error {
	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create snapshot dir: %w", err)
	}

	tmpFile, err := os.CreateTemp(dir, filepath.Base(r.path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp snapshot file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write temp snapshot file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp snapshot file: %w", err)
	}
	if err := os.Rename(tmpPath, r.path); err != nil {
		return fmt.Errorf("rename temp snapshot file: %w", err)
	}
	return nil
}
