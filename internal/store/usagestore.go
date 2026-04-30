package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

// UsageStore is the file-backed usage statistics persister.
// It saves and loads usage stats as a JSON file in the configured data directory.
type UsageStore struct {
	dataDir string
}

// NewUsageStore creates a file-based usage statistics persister.
// The stats file will be stored at <dataDir>/usage-stats.json.
func NewUsageStore(dataDir string) *UsageStore {
	return &UsageStore{dataDir: dataDir}
}

func (s *UsageStore) filePath() string {
	if s.dataDir == "" {
		return "usage-stats.json"
	}
	return filepath.Join(s.dataDir, "usage-stats.json")
}

// LoadUsage reads a previously saved usage snapshot from disk.
func (s *UsageStore) LoadUsage() (*usage.StatisticsSnapshot, error) {
	path := s.filePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read usage stats file: %w", err)
	}
	var snapshot usage.StatisticsSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("parse usage stats file: %w", err)
	}
	return &snapshot, nil
}

// SaveUsage writes the usage snapshot to disk atomically after merging with existing data.
func (s *UsageStore) SaveUsage(snapshot *usage.StatisticsSnapshot) error {
	if snapshot == nil {
		return nil
	}

	// 先加载现有数据
	existing, err := s.LoadUsage()
	if err != nil {
		return err
	}

	// 创建临时统计结构用于合并
	stats := usage.NewRequestStatistics()

	// 如果有现有数据，先合并进去
	if existing != nil {
		stats.MergeSnapshot(*existing)
	}

	finalStats := usage.NewRequestStatistics()
	if existing != nil {
		finalStats.MergeSnapshot(*existing)
	}
	finalStats.MergeSnapshot(*snapshot)

	mergedSnapshot := finalStats.Snapshot()

	data, err := json.MarshalIndent(&mergedSnapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal usage stats: %w", err)
	}

	path := s.filePath()
	if s.dataDir != "" {
		if err := os.MkdirAll(s.dataDir, 0o700); err != nil {
			return fmt.Errorf("create usage stats directory: %w", err)
		}
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write usage stats temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename usage stats file: %w", err)
	}
	return nil
}
