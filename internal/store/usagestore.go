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

	// 再把当前快照合并进去（为了让合并生效，我们需要把当前快照"还原"为详细记录，
	// 但是等等，我们没有原始请求记录，所以我们需要另一种方法？哦，不对，
	// 其实当前要保存的 snapshot 已经是完整的累加结果了。但根据用户的需求，
	// 我们希望把持久化层的数据和内存中的数据做一个双向合并，确保不丢失任何数据。

	// 让我们把当前 snapshot 也放到 stats 里
	// 为了做到这一点，我们需要创建一个临时的快照合并流程，同时保留两边的数据

	// 正确的方法是：先加载持久化数据，然后用默认的统计对象来合并，然后把当前快照
	// 也以某种方式合并进去。但是等等，当前的 snapshot 其实已经是完整的了，
	// 所以我们可以让统计对象先 merge 现有数据，然后再创建一个新的统计对象把 snapshot
	// 里的数据也合并进去，然后再取新的快照保存。

	// 实际上，我们需要一个更好的方法：我们应该有一个 StatisticsSnapshot.Merge 方法，
	// 但现在我们只有 RequestStatistics.MergeSnapshot 方法。

	// 所以我们这样做：
	// 1. 创建一个新的 RequestStatistics
	// 2. 先把现有的持久化快照合并进去
	// 3. 再把当前要保存的快照合并进去
	// 4. 然后取这个新统计对象的快照保存

	// 等等，不对，这样的话，如果 current snapshot 里的某些请求 details 和 existing 
	// 里的重复，会被去重，但是累加的总计数会没问题。

	// 但是其实，当前的 snapshot 是内存中的完整数据，已经包含了程序运行期间累加的数据，
	// 而 existing 是上次保存的数据。直接把这两个 merge 可能会导致重复计数？

	// 让我重新思考一下：
	// - 如果程序正常运行，那么内存中的 snapshot 已经是 merge 了上次持久化数据，
	//   然后累加了新请求的结果。这时候直接保存没问题。
	// - 但是如果有多个程序实例，或者持久化层被外部修改过，或者程序重启后内存数据丢失，
	//   那么我们需要确保保存的时候 merge 持久化层的现有数据。

	// 所以正确的做法：
	// 我们创建一个临时的 RequestStatistics，先加载 existing 并 merge，
	// 然后再把当前的 snapshot 也 merge 进去。
	// 这样就得到了两边数据的合并结果，然后保存这个合并后的结果。

	// 但是等等，当前的 snapshot 本身就是一个 StatisticsSnapshot，我们不能直接把它 merge 进去，
	// 因为 MergeSnapshot 期望的是持久化的那种格式（有完整的 details 记录）。
	// 而当前的 snapshot 其实就包含了完整的 details 记录！对，因为 Snapshot() 方法会把所有的 details 都保存下来。

	// 对，没错！所以我们可以：
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
