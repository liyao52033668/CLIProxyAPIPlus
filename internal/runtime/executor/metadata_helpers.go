package executor

import "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"

func metadataString(metadata map[string]any, key string) string {
	return helps.MetadataString(metadata, key)
}
