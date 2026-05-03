package managementasset

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:web_static
var embeddedWebFS embed.FS

// GetEmbeddedFileSystem returns the embedded web static files as an http.FileSystem.
// Returns nil if no embedded files are available.
func GetEmbeddedFileSystem() http.FileSystem {
	sub, err := fs.Sub(embeddedWebFS, "web_static")
	if err != nil {
		return nil
	}
	return http.FS(sub)
}

// GetEmbeddedFS returns the raw fs.FS for the embedded web static files.
func GetEmbeddedFS() fs.FS {
	sub, err := fs.Sub(embeddedWebFS, "web_static")
	if err != nil {
		return nil
	}
	return sub
}

// HasEmbeddedAssets returns true if embedded web assets are available.
func HasEmbeddedAssets() bool {
	fsys := GetEmbeddedFileSystem()
	if fsys == nil {
		return false
	}
	f, err := fsys.Open("index.html")
	if err != nil {
		return false
	}
	defer func() {
		_ = f.Close()
	}()
	return true
}
