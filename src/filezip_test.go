package main

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestServeFileZip(t *testing.T) {
	swapBackend(t, &localBackend{})
	dir := filepath.Join(t.TempDir(), "proj")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "sub", "empty"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("beta"), 0o644))
	// A symlink out of the tree must not be followed.
	must(os.Symlink("/", filepath.Join(dir, "escape")))

	rec := httptest.NewRecorder()
	serveFileZip(rec, httptest.NewRequest("GET", "/api/file-zip?path="+url.QueryEscape(dir), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="proj.zip"` {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	must(err)
	var names []string
	contents := map[string]string{}
	for _, f := range zr.File {
		names = append(names, f.Name)
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		must(err)
		b, _ := io.ReadAll(rc)
		rc.Close()
		contents[f.Name] = string(b)
	}
	sort.Strings(names)
	want := []string{"proj/", "proj/a.txt", "proj/sub/", "proj/sub/b.txt", "proj/sub/empty/"}
	if len(names) != len(want) {
		t.Fatalf("entries = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("entries = %v, want %v", names, want)
		}
	}
	if contents["proj/a.txt"] != "alpha" || contents["proj/sub/b.txt"] != "beta" {
		t.Fatalf("contents = %v", contents)
	}
}

func TestServeFileZipRefusesFile(t *testing.T) {
	swapBackend(t, &localBackend{})
	f := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	serveFileZip(rec, httptest.NewRequest("GET", "/api/file-zip?path="+url.QueryEscape(f), nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}
