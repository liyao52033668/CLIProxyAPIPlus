package store

import "github.com/router-for-me/CLIProxyAPI/v7/internal/authfiles"

func shouldIgnoreAuxiliaryJSON(path, baseDir string) bool {
	return authfiles.ShouldIgnoreAuxiliaryJSON(path, baseDir)
}
