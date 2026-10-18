package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func writeTree(t *testing.T, root string, files ...string) {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func fileFind(t *testing.T, root string) (files []string, truncated bool, source string) {
	t.Helper()
	rec := httptest.NewRecorder()
	serveFileFind(rec, httptest.NewRequest(http.MethodGet, "/api/file-find?root="+url.QueryEscape(root), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Files     []string `json:"files"`
		Truncated bool     `json:"truncated"`
		Source    string   `json:"source"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Files, out.Truncated, out.Source
}

// Inside a repository the list is git's: ignored files stay out, untracked
// ones are in, and a subdirectory root lists relative to itself.
func TestFileFindGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	swapBackend(t, &localBackend{})
	root := t.TempDir()
	if err := exec.Command("git", "init", "-q", root).Run(); err != nil {
		t.Fatal(err)
	}
	writeTree(t, root, ".gitignore", "src/main.go", "src/web/app.ts", "build/out.bin", "notes.md")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("build/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	add := exec.Command("git", "-C", root, "add", "src/main.go")
	if err := add.Run(); err != nil {
		t.Fatal(err)
	}

	files, truncated, source := fileFind(t, root)
	want := []string{".gitignore", "notes.md", "src/main.go", "src/web/app.ts"}
	if source != "git" || truncated || !reflect.DeepEqual(files, want) {
		t.Fatalf("got %v %v %q, want %v git", files, truncated, source, want)
	}

	files, _, _ = fileFind(t, filepath.Join(root, "src"))
	if want := []string{"main.go", "web/app.ts"}; !reflect.DeepEqual(files, want) {
		t.Fatalf("subdir root: got %v, want %v", files, want)
	}
}

// Outside a repository it walks, skipping hidden and dependency directories.
func TestFileFindWalk(t *testing.T) {
	swapBackend(t, &localBackend{})
	root := t.TempDir()
	writeTree(t, root, "a.txt", "dir/b.txt", ".hidden/c.txt", "node_modules/x/d.js", ".env")

	files, truncated, source := fileFind(t, root)
	want := []string{".env", "a.txt", "dir/b.txt"}
	if source != "walk" || truncated || !reflect.DeepEqual(files, want) {
		t.Fatalf("got %v %v %q, want %v walk", files, truncated, source, want)
	}
}

// A walk past its deadline answers with what it has, marked truncated.
func TestFileFindWalkDeadline(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "a.txt", "dir/b.txt")
	files, truncated := walkFiles(&localBackend{}, root, time.Now().Add(-time.Second))
	if !truncated || len(files) != 0 {
		t.Fatalf("got %v %v, want nothing, truncated", files, truncated)
	}
}

func TestFileFindRefusesRelativeRoot(t *testing.T) {
	swapBackend(t, &localBackend{})
	rec := httptest.NewRecorder()
	serveFileFind(rec, httptest.NewRequest(http.MethodGet, "/api/file-find?root=rel", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}
