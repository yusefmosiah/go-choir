package sandbox

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultFilesRoot = "/tmp/go-choir-files"

// requireAuth checks that the X-Authenticated-User header exists, providing
// defense-in-depth auth gating at the sandbox level. The proxy validates the
// JWT and injects this header; this check ensures direct access to the sandbox
// without proxy authentication is denied.
func requireAuth(r *http.Request) error {
	user := r.Header.Get("X-Authenticated-User")
	if user == "" {
		return fmt.Errorf("missing authenticated user identity")
	}
	return nil
}

// FileEntry represents a single file or directory in a listing response.
type FileEntry struct {
	Name     string `json:"name"`
	Type     string `json:"type"` // "file" or "directory"
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
}

// FileErrorResponse represents an error returned by the file API.
type FileErrorResponse struct {
	Error string `json:"error"`
}

// FilesHandler provides HTTP handlers for file browser operations.
type FilesHandler struct {
	rootDir string
}

// NewFilesHandler creates a new file browser handler rooted at rootDir.
// If rootDir is empty, the SANDBOX_FILES_ROOT env var is used, falling back
// to /tmp/go-choir-files.
func NewFilesHandler(rootDir string) *FilesHandler {
	if rootDir == "" {
		rootDir = os.Getenv("SANDBOX_FILES_ROOT")
	}
	if rootDir == "" {
		rootDir = defaultFilesRoot
	}
	// Ensure root directory exists.
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		log.Printf("files: could not create root directory %s: %v", rootDir, err)
	}
	return &FilesHandler{rootDir: rootDir}
}

// RootDir returns the configured root directory path.
func (fh *FilesHandler) RootDir() string {
	return fh.rootDir
}

// resolvePath safely resolves a user-supplied relative path against the
// sandbox root. It returns an error if the resolved path escapes the root.
func (fh *FilesHandler) resolvePath(relativePath string) (string, error) {
	// Remove leading slashes to treat the path as relative, then clean.
	rel := strings.TrimLeft(relativePath, "/")
	cleaned := filepath.Clean(rel)

	// filepath.Join with a cleaned relative path will always produce a path
	// under root. But if cleaned starts with "..", Join will walk up. Check
	// for that explicitly.
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes sandbox root")
	}

	absPath := filepath.Join(fh.rootDir, cleaned)

	// Final safety check: the result must be under the root.
	if !strings.HasPrefix(absPath, fh.rootDir+string(filepath.Separator)) && absPath != fh.rootDir {
		return "", fmt.Errorf("path escapes sandbox root")
	}
	return absPath, nil
}

// HandleListRoot handles GET /api/files — lists the root directory contents.
// It verifies the X-Authenticated-User header as defense-in-depth auth gating
// (the proxy validates the JWT and injects this header).
func (fh *FilesHandler) HandleListRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeFileError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if err := requireAuth(r); err != nil {
		writeFileError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	fh.listDirectory(w, fh.rootDir)
}

// HandleFileByPath handles GET/POST/DELETE /api/files/{path} — operates on
// a specific file or directory identified by the URL path suffix.
// It verifies the X-Authenticated-User header as defense-in-depth auth gating.
func (fh *FilesHandler) HandleFileByPath(w http.ResponseWriter, r *http.Request) {
	// Verify authentication before any file operations.
	if err := requireAuth(r); err != nil {
		writeFileError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	// Extract the path after "/api/files/".
	suffix := strings.TrimPrefix(r.URL.Path, "/api/files/")
	if suffix == "" {
		// Redirect to root listing.
		fh.HandleListRoot(w, r)
		return
	}

	absPath, err := fh.resolvePath(suffix)
	if err != nil {
		writeFileError(w, http.StatusForbidden, "access denied")
		return
	}

	switch r.Method {
	case http.MethodGet:
		fh.handleGet(w, r, absPath)
	case http.MethodPost:
		fh.handleCreateDirectory(w, r, absPath)
	case http.MethodPut:
		fh.handleUpdateFile(w, r, absPath)
	case http.MethodDelete:
		fh.handleDelete(w, r, absPath)
	default:
		writeFileError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleGet either lists a directory's contents or serves a file for download.
func (fh *FilesHandler) handleGet(w http.ResponseWriter, r *http.Request, absPath string) {
	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeFileError(w, http.StatusNotFound, "not found")
			return
		}
		if os.IsPermission(err) {
			writeFileError(w, http.StatusForbidden, "access denied")
			return
		}
		writeFileError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if info.IsDir() {
		fh.listDirectory(w, absPath)
		return
	}

	// Serve file for download.
	fh.serveFile(w, r, absPath, info)
}

// listDirectory returns a JSON array of FileEntry objects for the given directory.
func (fh *FilesHandler) listDirectory(w http.ResponseWriter, dirPath string) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeFileError(w, http.StatusNotFound, "directory not found")
			return
		}
		if os.IsPermission(err) {
			writeFileError(w, http.StatusForbidden, "access denied")
			return
		}
		writeFileError(w, http.StatusInternalServerError, "internal error")
		return
	}

	var fileEntries []FileEntry
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue // skip entries we can't stat
		}

		entryType := "file"
		if entry.IsDir() {
			entryType = "directory"
		}

		fileEntries = append(fileEntries, FileEntry{
			Name:     entry.Name(),
			Type:     entryType,
			Size:     info.Size(),
			Modified: info.ModTime().UTC().Format(time.RFC3339),
		})
	}

	// Return empty array instead of null when no entries.
	if fileEntries == nil {
		fileEntries = []FileEntry{}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(fileEntries)
}

// serveFile streams a file for download with appropriate headers.
func (fh *FilesHandler) serveFile(w http.ResponseWriter, r *http.Request, absPath string, info fs.FileInfo) {
	file, err := os.Open(absPath)
	if err != nil {
		if os.IsPermission(err) {
			writeFileError(w, http.StatusForbidden, "access denied")
			return
		}
		writeFileError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, info.Name()))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

// handleCreateDirectory creates a new directory at the given path.
func (fh *FilesHandler) handleCreateDirectory(w http.ResponseWriter, r *http.Request, absPath string) {
	// Check if it already exists.
	info, err := os.Stat(absPath)
	if err == nil {
		// Path already exists.
		if info.IsDir() {
			writeFileError(w, http.StatusConflict, "directory already exists")
		} else {
			writeFileError(w, http.StatusConflict, "a file with that name already exists")
		}
		return
	}
	if !os.IsNotExist(err) {
		if os.IsPermission(err) {
			writeFileError(w, http.StatusForbidden, "access denied")
			return
		}
		writeFileError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Ensure parent directory exists.
	parentDir := filepath.Dir(absPath)
	if _, err := os.Stat(parentDir); os.IsNotExist(err) {
		writeFileError(w, http.StatusNotFound, "parent directory not found")
		return
	}

	if err := os.Mkdir(absPath, 0o755); err != nil {
		writeFileError(w, http.StatusInternalServerError, "failed to create directory")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "created",
		"message": "directory created",
	})
}

// handleUpdateFile writes the request body to a regular file. If the file
// does not exist, it is created. Parent directories must already exist.
func (fh *FilesHandler) handleUpdateFile(w http.ResponseWriter, r *http.Request, absPath string) {
	info, err := os.Stat(absPath)
	if err == nil && info.IsDir() {
		writeFileError(w, http.StatusConflict, "path is a directory")
		return
	}
	if err != nil && !os.IsNotExist(err) {
		if os.IsPermission(err) {
			writeFileError(w, http.StatusForbidden, "access denied")
			return
		}
		writeFileError(w, http.StatusInternalServerError, "internal error")
		return
	}

	parentDir := filepath.Dir(absPath)
	if _, err := os.Stat(parentDir); os.IsNotExist(err) {
		writeFileError(w, http.StatusNotFound, "parent directory not found")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeFileError(w, http.StatusInternalServerError, "failed to read body")
		return
	}

	mode := os.FileMode(0o644)
	if info != nil {
		mode = info.Mode()
		if mode == 0 {
			mode = 0o644
		}
	}

	if err := os.WriteFile(absPath, body, mode); err != nil {
		if os.IsPermission(err) {
			writeFileError(w, http.StatusForbidden, "access denied")
			return
		}
		writeFileError(w, http.StatusInternalServerError, "failed to write file")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "updated",
		"message": "file saved",
	})
}

// handleDelete removes a file or directory at the given path.
func (fh *FilesHandler) handleDelete(w http.ResponseWriter, r *http.Request, absPath string) {
	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeFileError(w, http.StatusNotFound, "not found")
			return
		}
		if os.IsPermission(err) {
			writeFileError(w, http.StatusForbidden, "access denied")
			return
		}
		writeFileError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if info.IsDir() {
		// Only remove empty directories to prevent accidental data loss.
		entries, err := os.ReadDir(absPath)
		if err != nil {
			writeFileError(w, http.StatusInternalServerError, "failed to read directory")
			return
		}
		if len(entries) > 0 {
			writeFileError(w, http.StatusConflict, "directory not empty")
			return
		}
		if err := os.Remove(absPath); err != nil {
			writeFileError(w, http.StatusInternalServerError, "failed to delete directory")
			return
		}
	} else {
		if err := os.Remove(absPath); err != nil {
			writeFileError(w, http.StatusInternalServerError, "failed to delete file")
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// RegisterFileRoutes registers all file browser routes on the given server.
func RegisterFileRoutes(s interface{ HandleFunc(string, http.HandlerFunc) }, fh *FilesHandler) {
	s.HandleFunc("/api/files", fh.HandleListRoot)
	s.HandleFunc("/api/files/", fh.HandleFileByPath)
}

// writeFileError writes a JSON error response with the given status code.
func writeFileError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(FileErrorResponse{Error: message})
}
