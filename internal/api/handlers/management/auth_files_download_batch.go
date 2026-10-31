package management

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// maxBatchDownloadFiles caps one batch download request to bound resource usage.
const maxBatchDownloadFiles = 500

// DownloadAuthFilesBatch streams the requested auth files as a single zip archive.
//
// GET /v0/management/auth-files/download-batch?name=a.json&name=b.json
//
// Entries that are missing, unsafe (e.g. contain path separators), or not .json
// are skipped and reported via the X-Auth-Files-Missing response header (JSON
// array); the archive streams with whatever entries are available.
func (h *Handler) DownloadAuthFilesBatch(c *gin.Context) {
	names := uniqueAuthFileNames(c.QueryArray("name"))
	if len(names) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no names requested"})
		return
	}
	if len(names) > maxBatchDownloadFiles {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("too many files requested: %d (max %d)", len(names), maxBatchDownloadFiles),
		})
		return
	}

	authDir := h.cfg.AuthDir
	type batchEntry struct {
		name string
		path string
	}

	// First pass: validate names and stat files to collect candidates.
	candidates := make([]batchEntry, 0, len(names))
	missing := make([]string, 0)
	for _, name := range names {
		// Validate before touching the filesystem so path separators and
		// volume names can never reach filepath.Join.
		if isUnsafeAuthFileName(name) || !strings.HasSuffix(strings.ToLower(name), ".json") {
			missing = append(missing, name)
			continue
		}
		full := filepath.Join(authDir, name)
		info, errStat := os.Stat(full)
		if errStat != nil || info.IsDir() {
			missing = append(missing, name)
			continue
		}
		candidates = append(candidates, batchEntry{name: name, path: full})
	}
	if len(candidates) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "no requested files found"})
		return
	}

	// Second pass: read all files into memory before setting response headers.
	// This eliminates the TOCTOU race where X-Auth-Files-Count could over-report
	// if a file becomes unreadable between stat and read.
	type fileEntry struct {
		name string
		data []byte
	}
	files := make([]fileEntry, 0, len(candidates))
	for _, entry := range candidates {
		data, errRead := os.ReadFile(entry.path)
		if errRead != nil {
			// File became unreadable between stat and read; track as missing.
			missing = append(missing, entry.name)
			continue
		}
		files = append(files, fileEntry{name: entry.name, data: data})
	}
	if len(files) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "no requested files could be read"})
		return
	}

	// Encode missing names as a JSON array. This avoids the comma-separator
	// ambiguity (filenames can contain commas) and ensures control characters
	// are safely escaped for HTTP header transport.
	missingJSON, errMarshal := json.Marshal(missing)
	missingHeader := "[]"
	if errMarshal == nil {
		missingHeader = string(missingJSON)
	}

	zipName := fmt.Sprintf("auth-files_%s.zip", time.Now().Format("20060102_150405"))
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", zipName))
	// Count reflects files that were actually read, not just stat'd.
	c.Header("X-Auth-Files-Count", strconv.Itoa(len(files)))
	if len(missing) > 0 {
		c.Header("X-Auth-Files-Missing", missingHeader)
	}

	zipWriter := zip.NewWriter(c.Writer)
	modified := time.Now()
	for _, file := range files {
		// archive/zip retains a reference to the header, so a fresh value per entry
		// is required to keep central-directory names from aliasing.
		writer, errCreate := zipWriter.CreateHeader(&zip.FileHeader{
			Name:     file.name,
			Method:   zip.Deflate,
			Modified: modified,
		})
		if errCreate != nil {
			log.WithError(errCreate).Warnf("batch download: failed to create zip entry for %q", file.name)
			continue
		}
		if _, errWrite := writer.Write(file.data); errWrite != nil {
			// Stop writing on first failure — continuing wastes CPU and masks
			// the root cause (typically a client disconnect).
			log.WithError(errWrite).Warnf("batch download: failed to write zip entry for %q, aborting", file.name)
			break
		}
	}

	// Check the close error so corrupt/truncated zips are logged rather than
	// silently delivered to the client.
	if errClose := zipWriter.Close(); errClose != nil {
		log.WithError(errClose).Warn("batch download: failed to finalize zip archive")
	}
}
