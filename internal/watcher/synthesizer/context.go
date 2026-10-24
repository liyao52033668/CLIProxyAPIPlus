package synthesizer

import (
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// SynthesisContext provides the context needed for auth synthesis.
type SynthesisContext struct {
	// Config is the current configuration
	Config *config.Config
	// AuthDir is the directory containing auth files
	AuthDir string
	// Now is the current time for timestamps
	Now time.Time
	// IDGenerator generates stable IDs for auth entries
	IDGenerator *StableIDGenerator
	// IncompleteFiles records existing auth JSON file paths whose os.ReadFile
	// failed with an error other than fs.ErrNotExist, so callers can preserve
	// the previously synthesized credentials instead of dropping them.
	IncompleteFiles map[string]struct{}
}

// RecordIncompleteFile records an existing auth JSON file whose os.ReadFile
// failed with an error other than fs.ErrNotExist.
func (c *SynthesisContext) RecordIncompleteFile(path string) {
	if c == nil || path == "" {
		return
	}
	if c.IncompleteFiles == nil {
		c.IncompleteFiles = make(map[string]struct{})
	}
	c.IncompleteFiles[path] = struct{}{}
}
