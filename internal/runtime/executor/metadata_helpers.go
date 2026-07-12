package executor

import "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"

func metadataString(metadata map[string]any, keys ...string) string {
	return helps.MetadataString(metadata, keys...)
}
