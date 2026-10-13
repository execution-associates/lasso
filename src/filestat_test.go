package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// /api/file-stat, /api/file's within= and X-Lasso-Mtime, and /api/file-write's
// base_mtime: what the Mac app's file windows (mac/PLAN.md §26) watch, confine
// and save through.

func postFileStat(t *testing.T, req fileStatReq) (*httptest.ResponseRecorder, fileStatResp) {
	t.Helper()
	body, _ := json.Marshal(req)
	rec := httptest.NewRecorder()
	serveFileStat(rec, httptest.NewRequest(http.MethodPost, "/api/file-stat", bytes.NewReader(body)))
	var out fileStatResp
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v (%s)", err, rec.Body.String())
		}
	}
	return rec, out
}

func TestFileStatBogusHostRefusedBeforeRead(t *testing.T) {
	spy := &fsSpyBackend{Backend: &localBackend{}}
	swapBackend(t, spy)
	rec, _ := postFileStat(t, fileStatReq{Host: "no-such-host-xyz", Paths: []string{"/"}})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if spy.fsReads != 0 {
		t.Errorf("filesystem read %d time(s) for a refused host", spy.fsReads)
	}
}

func TestFileStatReportsEachPathInOrder(t *testing.T) {
	swapBackend(t, &localBackend{})
	dir := t.TempDir()
	f := filepath.Join(dir, "a.md")
	if err := os.WriteFile(f, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec, out := postFileStat(t, fileStatReq{Paths: []string{f, filepath.Join(dir, "gone.png"), dir, "rel/x"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	if out.Host != "local" || len(out.Files) != 4 {
		t.Fatalf("got %+v", out)
	}
	a, gone, d, rel := out.Files[0], out.Files[1], out.Files[2], out.Files[3]
	info, _ := os.Stat(f)
	if a.Path != f || !a.Exists || a.Dir || a.Size != 5 || a.Mtime != info.ModTime().UnixMilli() || a.Error != "" {
		t.Errorf("file entry = %+v", a)
	}
	if gone.Exists || gone.Error != "" {
		t.Errorf("a missing file is exists:false with no error, got %+v", gone)
	}
	if !d.Exists || !d.Dir {
		t.Errorf("dir entry = %+v", d)
	}
	if rel.Exists || rel.Error == "" {
		t.Errorf("a relative path is an error, got %+v", rel)
	}

	// A rewrite moves the size and the mtime.
	later := info.ModTime().Add(2 * time.Second)
	if err := os.WriteFile(f, []byte("hello again"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(f, later, later)
	_, out = postFileStat(t, fileStatReq{Paths: []string{f}})
	if out.Files[0].Size != 11 || out.Files[0].Mtime != later.UnixMilli() {
		t.Errorf("after a rewrite: %+v", out.Files[0])
	}
}

func TestFileStatExpandsTildeOnResolvedBackend(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "note.txt"), []byte("x"), 0o644)
	swapBackend(t, &fakeHomeBackend{Backend: &localBackend{}, home: home})
	_, out := postFileStat(t, fileStatReq{Paths: []string{"~/note.txt"}})
	if len(out.Files) != 1 || !out.Files[0].Exists || out.Files[0].Path != "~/note.txt" {
		t.Errorf("got %+v", out)
	}
}

func TestFileStatRefusesBadRequests(t *testing.T) {
	swapBackend(t, &localBackend{})
	rec := httptest.NewRecorder()
	serveFileStat(rec, httptest.NewRequest(http.MethodGet, "/api/file-stat", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET = %d, want 405", rec.Code)
	}
	if rec, _ := postFileStat(t, fileStatReq{}); rec.Code != http.StatusBadRequest {
		t.Errorf("no paths = %d, want 400", rec.Code)
	}
	many := make([]string, maxFileStatPaths+1)
	for i := range many {
		many[i] = "/tmp"
	}
	if rec, _ := postFileStat(t, fileStatReq{Paths: many}); rec.Code != http.StatusBadRequest {
		t.Errorf("%d paths = %d, want 400", len(many), rec.Code)
	}
}

func getFile(t *testing.T, path, within string) *httptest.ResponseRecorder {
	t.Helper()
	q := "/api/file?path=" + path
	if within != "" {
		q += "&within=" + within
	}
	rec := httptest.NewRecorder()
	serveFile(rec, httptest.NewRequest(http.MethodGet, q, nil))
	return rec
}

func TestServeFileSendsMtime(t *testing.T) {
	swapBackend(t, &localBackend{})
	f := filepath.Join(t.TempDir(), "a.txt")
	os.WriteFile(f, []byte("x"), 0o644)
	info, _ := os.Stat(f)
	rec := getFile(t, f, "")
	if got := rec.Header().Get("X-Lasso-Mtime"); got != strconv.FormatInt(info.ModTime().UnixMilli(), 10) {
		t.Errorf("X-Lasso-Mtime = %q", got)
	}
}

func TestServeFileWithinConfinesToTheFolder(t *testing.T) {
	home := t.TempDir()
	swapBackend(t, &fakeHomeBackend{Backend: &localBackend{}, home: home})
	site := filepath.Join(home, "proj", "site")
	for _, d := range []string{"css", ".git", "img"} {
		os.MkdirAll(filepath.Join(site, d), 0o755)
	}
	os.WriteFile(filepath.Join(site, "index.html"), []byte("<p>"), 0o644)
	os.WriteFile(filepath.Join(site, "css", "a.css"), []byte("p{}"), 0o644)
	os.WriteFile(filepath.Join(site, ".git", "config"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(site, ".env"), []byte("SECRET=1"), 0o644)
	os.WriteFile(filepath.Join(home, "proj", "secret.txt"), []byte("x"), 0o644)
	os.Symlink(filepath.Join(home, "proj"), filepath.Join(site, "img", "up"))
	os.Symlink(filepath.Join(home, "proj", "secret.txt"), filepath.Join(site, "link.txt"))

	cases := []struct {
		path, within string
		want         int
	}{
		{filepath.Join(site, "index.html"), site, http.StatusOK},
		{filepath.Join(site, "css", "a.css"), site, http.StatusOK},
		{filepath.Join(site, "css", "a.css"), "~/proj/site", http.StatusOK},
		{filepath.Join(site, "..", "secret.txt"), site, http.StatusForbidden},
		{filepath.Join(site, ".env"), site, http.StatusForbidden},
		{filepath.Join(site, ".git", "config"), site, http.StatusForbidden},
		{filepath.Join(site, "img", "up", "secret.txt"), site, http.StatusForbidden},
		{filepath.Join(site, "link.txt"), site, http.StatusForbidden},
		{site, site, http.StatusForbidden},
		{filepath.Join(site, "index.html"), "relative/dir", http.StatusForbidden},
		// A root at the home directory or above it reaches only its own files.
		{filepath.Join(home, "proj", "secret.txt"), home, http.StatusForbidden},
		{filepath.Join(site, "index.html"), "/", http.StatusForbidden},
	}
	for _, c := range cases {
		if got := getFile(t, c.path, c.within).Code; got != c.want {
			t.Errorf("%s within %s = %d, want %d", c.path, c.within, got, c.want)
		}
	}
	os.WriteFile(filepath.Join(home, "page.html"), []byte("<p>"), 0o644)
	if got := getFile(t, filepath.Join(home, "page.html"), home).Code; got != http.StatusOK {
		t.Errorf("a file directly in the home root = %d, want 200", got)
	}
}

func postWrite(t *testing.T, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	serveFileWrite(rec, httptest.NewRequest(http.MethodPost, "/api/file-write", bytes.NewReader(b)))
	return rec
}

func TestFileWriteBaseMtime(t *testing.T) {
	swapBackend(t, &localBackend{})
	f := filepath.Join(t.TempDir(), "a.md")
	os.WriteFile(f, []byte("disk"), 0o644)
	info, _ := os.Stat(f)
	base := info.ModTime().UnixMilli()

	rec := postWrite(t, map[string]any{"path": f, "content": "mine", "base_mtime": base - 1000})
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale base = %d, want 409", rec.Code)
	}
	var conflict struct {
		Error string `json:"error"`
		Mtime int64  `json:"mtime"`
	}
	json.Unmarshal(rec.Body.Bytes(), &conflict)
	if conflict.Mtime != base || conflict.Error == "" {
		t.Errorf("409 body = %s", rec.Body.String())
	}
	if b, _ := os.ReadFile(f); string(b) != "disk" {
		t.Errorf("a refused write changed the file to %q", b)
	}

	rec = postWrite(t, map[string]any{"path": f, "content": "mine", "base_mtime": base})
	if rec.Code != http.StatusOK {
		t.Fatalf("matching base = %d (%s)", rec.Code, rec.Body.String())
	}
	var ok struct {
		OK    bool  `json:"ok"`
		Mtime int64 `json:"mtime"`
	}
	json.Unmarshal(rec.Body.Bytes(), &ok)
	after, _ := os.Stat(f)
	if !ok.OK || ok.Mtime != after.ModTime().UnixMilli() {
		t.Errorf("200 body = %s", rec.Body.String())
	}
	if b, _ := os.ReadFile(f); string(b) != "mine" {
		t.Errorf("file = %q", b)
	}
	// No base_mtime: the web editor's save, unchanged.
	if rec := postWrite(t, map[string]any{"path": f, "content": "web"}); rec.Code != http.StatusOK {
		t.Errorf("no base = %d", rec.Code)
	}
}

// A text file over the preview cap is answered with a placeholder, marked so an
// editor opens it read-only instead of saving the placeholder over the file.
func TestServeFileMarksTheTooLargePlaceholder(t *testing.T) {
	swapBackend(t, &localBackend{})
	dir := t.TempDir()
	big := filepath.Join(dir, "big.log")
	os.WriteFile(big, bytes.Repeat([]byte("x"), maxPreview+1), 0o644)
	rec := getFile(t, big, "")
	if got := rec.Header().Get("X-Lasso-Too-Large"); got != strconv.Itoa(maxPreview+1) {
		t.Errorf("X-Lasso-Too-Large = %q", got)
	}
	small := filepath.Join(dir, "small.log")
	os.WriteFile(small, []byte("x"), 0o644)
	if got := getFile(t, small, "").Header().Get("X-Lasso-Too-Large"); got != "" {
		t.Errorf("a file under the cap is marked %q", got)
	}
}
