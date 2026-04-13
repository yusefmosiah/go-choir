package sandbox

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// setupFileTest creates a temporary root directory and a FilesHandler for testing.
func setupFileTest(t *testing.T) (*FilesHandler, string) {
	t.Helper()
	rootDir := t.TempDir()
	fh := NewFilesHandler(rootDir)
	return fh, rootDir
}

// --- GET /api/files (root listing) ---

func TestListRootDirectory(t *testing.T) {
	fh, root := setupFileTest(t)

	// Create test entries in root.
	os.MkdirAll(filepath.Join(root, "documents"), 0o755)
	os.WriteFile(filepath.Join(root, "readme.txt"), []byte("hello"), 0o644)

	req := httptest.NewRequest(http.MethodGet, "/api/files", nil)
	w := httptest.NewRecorder()
	fh.HandleListRoot(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var entries []FileEntry
	if err := json.NewDecoder(w.Body).Decode(&entries); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Sort for deterministic comparison.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	if entries[0].Name != "documents" {
		t.Errorf("expected first entry name 'documents', got %q", entries[0].Name)
	}
	if entries[0].Type != "directory" {
		t.Errorf("expected documents type 'directory', got %q", entries[0].Type)
	}
	if entries[1].Name != "readme.txt" {
		t.Errorf("expected second entry name 'readme.txt', got %q", entries[1].Name)
	}
	if entries[1].Type != "file" {
		t.Errorf("expected readme.txt type 'file', got %q", entries[1].Type)
	}
	if entries[1].Size != 5 {
		t.Errorf("expected readme.txt size 5, got %d", entries[1].Size)
	}
	if entries[1].Modified == "" {
		t.Error("expected non-empty modified field")
	}
}

func TestListEmptyRootDirectory(t *testing.T) {
	fh, _ := setupFileTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/files", nil)
	w := httptest.NewRecorder()
	fh.HandleListRoot(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Verify we get an empty JSON array, not null.
	if w.Body.String() == "null" {
		t.Fatal("expected empty JSON array, got null")
	}

	var entries []FileEntry
	if err := json.NewDecoder(w.Body).Decode(&entries); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

func TestListRootRejectsNonGet(t *testing.T) {
	fh, _ := setupFileTest(t)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/files", nil)
			w := httptest.NewRecorder()
			fh.HandleListRoot(w, req)

			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("expected 405 for %s, got %d", method, w.Code)
			}
		})
	}
}

func TestListRootReturnsJSONContentType(t *testing.T) {
	fh, _ := setupFileTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/files", nil)
	w := httptest.NewRecorder()
	fh.HandleListRoot(w, req)

	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type 'application/json', got %q", ct)
	}
}

// --- GET /api/files/{path} (subdirectory listing or file download) ---

func TestGetSubdirectoryListing(t *testing.T) {
	fh, root := setupFileTest(t)

	// Create subdirectory with entries.
	os.MkdirAll(filepath.Join(root, "documents"), 0o755)
	os.WriteFile(filepath.Join(root, "documents", "notes.txt"), []byte("my notes"), 0o644)

	req := httptest.NewRequest(http.MethodGet, "/api/files/documents", nil)
	w := httptest.NewRecorder()
	fh.HandleFileByPath(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var entries []FileEntry
	if err := json.NewDecoder(w.Body).Decode(&entries); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Name != "notes.txt" {
		t.Errorf("expected name 'notes.txt', got %q", entries[0].Name)
	}
}

func TestGetNonexistentPathReturns404(t *testing.T) {
	fh, _ := setupFileTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/files/nonexistent", nil)
	w := httptest.NewRecorder()
	fh.HandleFileByPath(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}

	var errResp FileErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if errResp.Error != "not found" {
		t.Errorf("expected error 'not found', got %q", errResp.Error)
	}
}

func TestGetFileDownload(t *testing.T) {
	fh, root := setupFileTest(t)

	content := []byte("file content for download")
	os.WriteFile(filepath.Join(root, "download.txt"), content, 0o644)

	req := httptest.NewRequest(http.MethodGet, "/api/files/download.txt", nil)
	w := httptest.NewRecorder()
	fh.HandleFileByPath(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Check Content-Disposition header.
	cd := w.Header().Get("Content-Disposition")
	if cd != `attachment; filename="download.txt"` {
		t.Errorf("expected Content-Disposition for download, got %q", cd)
	}

	if w.Body.String() != string(content) {
		t.Errorf("expected body %q, got %q", string(content), w.Body.String())
	}
}

func TestGetNestedSubdirectory(t *testing.T) {
	fh, root := setupFileTest(t)

	// Create nested structure.
	os.MkdirAll(filepath.Join(root, "a", "b", "c"), 0o755)
	os.WriteFile(filepath.Join(root, "a", "b", "c", "deep.txt"), []byte("deep file"), 0o644)

	req := httptest.NewRequest(http.MethodGet, "/api/files/a/b/c", nil)
	w := httptest.NewRecorder()
	fh.HandleFileByPath(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var entries []FileEntry
	if err := json.NewDecoder(w.Body).Decode(&entries); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Name != "deep.txt" {
		t.Errorf("expected name 'deep.txt', got %q", entries[0].Name)
	}
}

// --- POST /api/files/{path} (create directory) ---

func TestCreateDirectory(t *testing.T) {
	fh, root := setupFileTest(t)

	req := httptest.NewRequest(http.MethodPost, "/api/files/new-folder", nil)
	w := httptest.NewRecorder()
	fh.HandleFileByPath(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Verify directory was actually created.
	info, err := os.Stat(filepath.Join(root, "new-folder"))
	if err != nil {
		t.Fatalf("directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected a directory")
	}
}

func TestCreateDirectoryReturnsConflictIfExists(t *testing.T) {
	fh, root := setupFileTest(t)

	// Pre-create the directory.
	os.MkdirAll(filepath.Join(root, "existing"), 0o755)

	req := httptest.NewRequest(http.MethodPost, "/api/files/existing", nil)
	w := httptest.NewRecorder()
	fh.HandleFileByPath(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}

	var errResp FileErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if errResp.Error != "directory already exists" {
		t.Errorf("expected error 'directory already exists', got %q", errResp.Error)
	}
}

func TestCreateDirectoryConflictWithFile(t *testing.T) {
	fh, root := setupFileTest(t)

	// Pre-create a file with the same name.
	os.WriteFile(filepath.Join(root, "conflict"), []byte("data"), 0o644)

	req := httptest.NewRequest(http.MethodPost, "/api/files/conflict", nil)
	w := httptest.NewRecorder()
	fh.HandleFileByPath(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}

func TestCreateDirectoryInSubdirectory(t *testing.T) {
	fh, root := setupFileTest(t)

	// Pre-create parent directory.
	os.MkdirAll(filepath.Join(root, "docs"), 0o755)

	req := httptest.NewRequest(http.MethodPost, "/api/files/docs/subfolder", nil)
	w := httptest.NewRecorder()
	fh.HandleFileByPath(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Verify it exists.
	info, err := os.Stat(filepath.Join(root, "docs", "subfolder"))
	if err != nil {
		t.Fatalf("subdirectory not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected a directory")
	}
}

func TestCreateDirectoryParentNotFound(t *testing.T) {
	fh, _ := setupFileTest(t)

	// Try to create a directory in a nonexistent parent.
	req := httptest.NewRequest(http.MethodPost, "/api/files/nonexistent/new-folder", nil)
	w := httptest.NewRecorder()
	fh.HandleFileByPath(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// --- DELETE /api/files/{path} (delete file/folder) ---

func TestDeleteFile(t *testing.T) {
	fh, root := setupFileTest(t)

	// Create a file to delete.
	os.WriteFile(filepath.Join(root, "temp.txt"), []byte("temp"), 0o644)

	req := httptest.NewRequest(http.MethodDelete, "/api/files/temp.txt", nil)
	w := httptest.NewRecorder()
	fh.HandleFileByPath(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}

	// Verify file is gone.
	if _, err := os.Stat(filepath.Join(root, "temp.txt")); !os.IsNotExist(err) {
		t.Error("expected file to be deleted")
	}
}

func TestDeleteEmptyDirectory(t *testing.T) {
	fh, root := setupFileTest(t)

	// Create an empty directory to delete.
	os.MkdirAll(filepath.Join(root, "empty-dir"), 0o755)

	req := httptest.NewRequest(http.MethodDelete, "/api/files/empty-dir", nil)
	w := httptest.NewRecorder()
	fh.HandleFileByPath(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}

	// Verify directory is gone.
	if _, err := os.Stat(filepath.Join(root, "empty-dir")); !os.IsNotExist(err) {
		t.Error("expected directory to be deleted")
	}
}

func TestDeleteNonexistentReturns404(t *testing.T) {
	fh, _ := setupFileTest(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/files/nonexistent", nil)
	w := httptest.NewRecorder()
	fh.HandleFileByPath(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestDeleteNonEmptyDirectoryReturns409(t *testing.T) {
	fh, root := setupFileTest(t)

	// Create a non-empty directory.
	os.MkdirAll(filepath.Join(root, "notempty"), 0o755)
	os.WriteFile(filepath.Join(root, "notempty", "file.txt"), []byte("data"), 0o644)

	req := httptest.NewRequest(http.MethodDelete, "/api/files/notempty", nil)
	w := httptest.NewRecorder()
	fh.HandleFileByPath(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}

// --- Path traversal protection ---

func TestPathTraversalBlocked(t *testing.T) {
	fh, _ := setupFileTest(t)

	// Test various path traversal patterns - resolvePath should catch them
	// before any filesystem access.
	paths := []string{
		"../../etc/passwd",
		"../../../tmp/something",
		"../..",
	}
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			_, err := fh.resolvePath(p)
			if err == nil {
				t.Errorf("expected error for path traversal %q, but got nil", p)
			}
		})
	}
}

func TestPathTraversalHTTPBlocked(t *testing.T) {
	fh, _ := setupFileTest(t)

	// Simulate a URL with path traversal (as the HTTP mux would see it after
	// stripping the prefix). Note: in real routing, Go's http.ServeMux
	// will clean path components, so this is defense-in-depth.
	req := httptest.NewRequest(http.MethodGet, "/api/files/..%2F..%2Fetc%2Fpasswd", nil)
	w := httptest.NewRecorder()
	fh.HandleFileByPath(w, req)

	// The path gets cleaned by resolvePath - should be blocked or 404.
	// Either way, no file contents from outside the root should be returned.
	if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
		t.Errorf("expected 403 or 404, got %d", w.Code)
	}
}

// --- Special characters in filenames ---

func TestSpecialCharactersInFilenames(t *testing.T) {
	fh, root := setupFileTest(t)

	// Create files with special characters.
	specialNames := []string{
		"file with spaces.txt",
		"file(1).txt",
		"file_underscore.txt",
		"file-dash.txt",
	}
	for _, name := range specialNames {
		err := os.WriteFile(filepath.Join(root, name), []byte("content"), 0o644)
		if err != nil {
			t.Fatalf("failed to create file %q: %v", name, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/files", nil)
	w := httptest.NewRecorder()
	fh.HandleListRoot(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var entries []FileEntry
	if err := json.NewDecoder(w.Body).Decode(&entries); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(entries) != len(specialNames) {
		t.Fatalf("expected %d entries, got %d", len(specialNames), len(entries))
	}

	// Check all names are present.
	found := map[string]bool{}
	for _, e := range entries {
		found[e.Name] = true
	}
	for _, name := range specialNames {
		if !found[name] {
			t.Errorf("expected entry %q not found", name)
		}
	}
}

func TestCreateAndGetDirectoryWithSpaces(t *testing.T) {
	fh, root := setupFileTest(t)

	dirName := "my folder"
	// URL-encode the space for the HTTP request path.
	req := httptest.NewRequest(http.MethodPost, "/api/files/my%20folder", nil)
	w := httptest.NewRecorder()
	fh.HandleFileByPath(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Verify directory exists.
	if _, err := os.Stat(filepath.Join(root, dirName)); err != nil {
		t.Fatalf("directory not created: %v", err)
	}
}

func TestDeleteFileWithSpaces(t *testing.T) {
	fh, root := setupFileTest(t)

	fileName := "file with spaces.txt"
	os.WriteFile(filepath.Join(root, fileName), []byte("content"), 0o644)

	// URL-encode the space for the HTTP request path.
	req := httptest.NewRequest(http.MethodDelete, "/api/files/file%20with%20spaces.txt", nil)
	w := httptest.NewRecorder()
	fh.HandleFileByPath(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}

	if _, err := os.Stat(filepath.Join(root, fileName)); !os.IsNotExist(err) {
		t.Error("expected file to be deleted")
	}
}

// --- Unsupported methods ---

func TestFileByPathRejectsUnsupportedMethods(t *testing.T) {
	fh, _ := setupFileTest(t)

	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/files/something", nil)
			w := httptest.NewRecorder()
			fh.HandleFileByPath(w, req)

			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("expected 405 for %s, got %d", method, w.Code)
			}
		})
	}
}

// --- End-to-end workflow test ---

func TestFileBrowserWorkflow(t *testing.T) {
	fh, _ := setupFileTest(t)

	// 1. List root (should be empty).
	req := httptest.NewRequest(http.MethodGet, "/api/files", nil)
	w := httptest.NewRecorder()
	fh.HandleListRoot(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("step 1: expected 200, got %d", w.Code)
	}
	var entries []FileEntry
	json.NewDecoder(w.Body).Decode(&entries)
	if len(entries) != 0 {
		t.Fatalf("step 1: expected empty root, got %d entries", len(entries))
	}

	// 2. Create a directory.
	req = httptest.NewRequest(http.MethodPost, "/api/files/projects", nil)
	w = httptest.NewRecorder()
	fh.HandleFileByPath(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("step 2: expected 201, got %d", w.Code)
	}

	// 3. List root (should show projects directory).
	req = httptest.NewRequest(http.MethodGet, "/api/files", nil)
	w = httptest.NewRecorder()
	fh.HandleListRoot(w, req)
	json.NewDecoder(w.Body).Decode(&entries)
	if len(entries) != 1 || entries[0].Name != "projects" {
		t.Fatalf("step 3: expected 1 entry named 'projects', got %v", entries)
	}

	// 4. Create a subdirectory inside projects.
	req = httptest.NewRequest(http.MethodPost, "/api/files/projects/my-app", nil)
	w = httptest.NewRecorder()
	fh.HandleFileByPath(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("step 4: expected 201, got %d", w.Code)
	}

	// 5. List projects (should show my-app).
	req = httptest.NewRequest(http.MethodGet, "/api/files/projects", nil)
	w = httptest.NewRecorder()
	fh.HandleFileByPath(w, req)
	json.NewDecoder(w.Body).Decode(&entries)
	if len(entries) != 1 || entries[0].Name != "my-app" {
		t.Fatalf("step 5: expected 1 entry named 'my-app', got %v", entries)
	}

	// 6. Delete the subdirectory.
	req = httptest.NewRequest(http.MethodDelete, "/api/files/projects/my-app", nil)
	w = httptest.NewRecorder()
	fh.HandleFileByPath(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("step 6: expected 204, got %d", w.Code)
	}

	// 7. Verify it's gone from the listing.
	req = httptest.NewRequest(http.MethodGet, "/api/files/projects", nil)
	w = httptest.NewRecorder()
	fh.HandleFileByPath(w, req)
	json.NewDecoder(w.Body).Decode(&entries)
	if len(entries) != 0 {
		t.Fatalf("step 7: expected empty projects directory, got %d entries", len(entries))
	}
}

// --- JSON error format tests ---

func TestErrorResponsesAreJSON(t *testing.T) {
	fh, _ := setupFileTest(t)

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"not found", http.MethodGet, "/api/files/nope"},
		{"conflict on create", http.MethodPost, "/api/files/"},
		{"delete not found", http.MethodDelete, "/api/files/nope"},
	}

	// Create the conflict case by creating something first.
	os.MkdirAll(filepath.Join(fh.rootDir, "conflict"), 0o755)

	tests = append(tests, struct {
		name   string
		method string
		path   string
	}{"conflict existing", http.MethodPost, "/api/files/conflict"})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			w := httptest.NewRecorder()
			fh.HandleFileByPath(w, req)

			ct := w.Header().Get("Content-Type")
			if ct != "application/json" {
				t.Errorf("expected Content-Type 'application/json', got %q", ct)
			}

			// Verify body parses as JSON error.
			var errResp FileErrorResponse
			if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
				t.Fatalf("failed to decode error response: %v", err)
			}
			if errResp.Error == "" {
				t.Error("expected non-empty error message")
			}
		})
	}
}

// --- Default root directory ---

func TestNewFilesHandlerDefaultRoot(t *testing.T) {
	// Test that default root is used when none provided and env is not set.
	// We just verify the handler doesn't panic and has a non-empty root.
	fh := NewFilesHandler("")
	if fh.RootDir() == "" {
		t.Error("expected non-empty root directory")
	}
}
