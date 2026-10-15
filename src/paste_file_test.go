package main

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// recordingPasteBackend stands in for the host a drop targets: it records the
// directory made and the bytes streamed, and never touches a real filesystem.
type recordingPasteBackend struct {
	Backend
	name       string
	dir        string
	mkdirPath  string
	mkdirMode  fs.FileMode
	createPath string
	created    *closeRecorder
	removed    string
}

type closeRecorder struct {
	bytes.Buffer
	closed bool
}

func (c *closeRecorder) Close() error { c.closed = true; return nil }

func (b *recordingPasteBackend) Name() string         { return b.name }
func (b *recordingPasteBackend) PasteFileDir() string { return b.dir }
func (b *recordingPasteBackend) MkdirAll(path string, mode fs.FileMode) error {
	b.mkdirPath, b.mkdirMode = path, mode
	return nil
}
func (b *recordingPasteBackend) Create(path string) (io.WriteCloser, error) {
	b.createPath, b.created = path, &closeRecorder{}
	return b.created, nil
}
func (b *recordingPasteBackend) RemoveAll(path string) error {
	b.removed = path
	return nil
}

func TestServePasteFileWritesThroughTargetBackend(t *testing.T) {
	for _, tc := range []struct {
		name        string
		backend     string
		dir         string
		requestURL  string
		contentType string
		wantSuffix  string
	}{
		{
			name:        "clipboard image stays local and is named by its type",
			backend:     "local",
			dir:         "/home/local/.lasso/uploads/dropped-files",
			requestURL:  "/api/paste-file",
			contentType: "image/png",
			wantSuffix:  ".png",
		},
		{
			name:        "native attach target writes remotely",
			backend:     "ticket500",
			dir:         "/home/stephan/.lasso/uploads/dropped-files",
			requestURL:  "/api/paste-file?host=ticket500",
			contentType: "image/png",
			wantSuffix:  ".png",
		},
		{
			// The whole point of the drop: a picked file is not an image, and
			// keeps the name that makes its path readable in a prompt.
			name:        "a picked document keeps its own filename",
			backend:     "local",
			dir:         "/home/local/.lasso/uploads/dropped-files",
			requestURL:  "/api/paste-file?name=Q3%20notes.pdf",
			contentType: "application/pdf",
			wantSuffix:  "-Q3 notes.pdf",
		},
		{
			// A filename is client-supplied: only its base may reach the path.
			name:        "a traversing filename cannot escape the drop dir",
			backend:     "local",
			dir:         "/home/local/.lasso/uploads/dropped-files",
			requestURL:  "/api/paste-file?name=..%2F..%2F.ssh%2Fauthorized_keys",
			contentType: "text/plain",
			wantSuffix:  "-authorized_keys",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			be := &recordingPasteBackend{name: tc.backend, dir: tc.dir}
			swapBackend(t, be)
			body := []byte("dropped bytes")
			req := httptest.NewRequest(http.MethodPost, tc.requestURL, bytes.NewReader(body))
			req.Header.Set("Content-Type", tc.contentType)
			rec := httptest.NewRecorder()

			servePasteFile(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
			}
			var response struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if filepath.Dir(response.Path) != tc.dir ||
				!strings.HasSuffix(response.Path, tc.wantSuffix) {
				t.Errorf("response path = %q, want %q in %q", response.Path, tc.wantSuffix, tc.dir)
			}
			if be.mkdirPath != tc.dir || be.mkdirMode != 0o755 {
				t.Errorf("MkdirAll = (%q, %o), want (%q, 755)", be.mkdirPath, be.mkdirMode, tc.dir)
			}
			if be.createPath != response.Path {
				t.Errorf("Create = %q, want %q", be.createPath, response.Path)
			}
			if be.created == nil || !bytes.Equal(be.created.Bytes(), body) || !be.created.closed {
				t.Errorf("streamed %q (closed %v), want %q closed", be.created.Bytes(), be.created.closed, body)
			}
			if be.removed != "" {
				t.Errorf("removed %q on a good write", be.removed)
			}
		})
	}
}

func TestServePasteFileDropsAnEmptyBody(t *testing.T) {
	be := &recordingPasteBackend{name: "local", dir: "/home/local/.lasso/uploads/dropped-files"}
	swapBackend(t, be)
	req := httptest.NewRequest(http.MethodPost, "/api/paste-file?name=empty.txt", strings.NewReader(""))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()

	servePasteFile(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	// A zero-byte file left behind would be a dead path in someone's prompt.
	if be.removed != be.createPath {
		t.Errorf("removed %q, want the created %q", be.removed, be.createPath)
	}
}

// A file bigger than one request body goes in parts: the first names the
// whole size and opens the upload, each next part appends at its offset, and
// the last answers the path alone.
func TestServePasteFileInParts(t *testing.T) {
	be := &recordingPasteBackend{name: "local", dir: "/home/local/.lasso/uploads/dropped-files"}
	swapBackend(t, be)
	post := func(url, body string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
		req.Header.Set("Content-Type", "video/quicktime")
		rec := httptest.NewRecorder()
		servePasteFile(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	code, first := post("/api/paste-file?name=rec.mov&size=9", "abc")
	if code != http.StatusOK || first["upload"] == nil || first["received"] != float64(3) {
		t.Fatalf("first part = %d %v, want an open upload at 3", code, first)
	}
	tok := first["upload"].(string)
	if be.created.closed {
		t.Fatal("the file was closed with parts still to come")
	}
	code, mid := post("/api/paste-file?upload="+tok+"&offset=3", "def")
	if code != http.StatusOK || mid["received"] != float64(6) || mid["path"] != first["path"] {
		t.Fatalf("middle part = %d %v", code, mid)
	}
	code, last := post("/api/paste-file?upload="+tok+"&offset=6", "ghi")
	if code != http.StatusOK || last["upload"] != nil || last["path"] != first["path"] {
		t.Fatalf("last part = %d %v, want the path alone", code, last)
	}
	if be.created.String() != "abcdefghi" || !be.created.closed {
		t.Errorf("wrote %q (closed %v)", be.created.String(), be.created.closed)
	}
	if code, _ := post("/api/paste-file?upload="+tok+"&offset=9", "x"); code != http.StatusNotFound {
		t.Errorf("a part after the end = %d, want 404", code)
	}
}

// A part at the wrong offset (a retry, a reorder) or past the declared size
// ends the upload and removes its file, rather than writing bytes twice.
func TestServePasteFilePartRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, url, body string
		want            int
	}{
		{"wrong offset", "offset=1", "def", http.StatusConflict},
		{"past the size", "offset=3", "defghijk", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			be := &recordingPasteBackend{name: "local", dir: "/d"}
			swapBackend(t, be)
			req := httptest.NewRequest(http.MethodPost, "/api/paste-file?name=a.bin&size=6", strings.NewReader("abc"))
			rec := httptest.NewRecorder()
			servePasteFile(rec, req)
			var first struct{ Upload, Path string }
			_ = json.Unmarshal(rec.Body.Bytes(), &first)
			req = httptest.NewRequest(http.MethodPost, "/api/paste-file?upload="+first.Upload+"&"+tc.url, strings.NewReader(tc.body))
			rec = httptest.NewRecorder()
			servePasteFile(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if be.removed != first.Path || !be.created.closed {
				t.Errorf("removed %q (closed %v), want %q removed", be.removed, be.created.closed, first.Path)
			}
			if pasteUploads.get(first.Upload) != nil {
				t.Error("the upload is still open")
			}
		})
	}
}

func TestServePasteFileRefusesOversizeDeclaration(t *testing.T) {
	be := &recordingPasteBackend{name: "local", dir: "/d"}
	swapBackend(t, be)
	req := httptest.NewRequest(http.MethodPost, "/api/paste-file?size=99999999999", strings.NewReader("abc"))
	rec := httptest.NewRecorder()
	servePasteFile(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge || be.createPath != "" {
		t.Errorf("status = %d, created %q; want 413 and nothing created", rec.Code, be.createPath)
	}
}

// The New Agent dialog stages each attachment as its own raw body, a big one
// in parts, all into the one staging dir create-agent later reads.
func TestServeAgentUploadFileInParts(t *testing.T) {
	t.Setenv("LASSO_DIR", "/lasso")
	be := &recordingPasteBackend{name: "local"}
	swapBackend(t, be)
	post := func(url, body string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
		req.Header.Set("Content-Type", "video/quicktime")
		rec := httptest.NewRecorder()
		serveAgentUpload(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	code, first := post("/api/agent-upload?name=Screen%20Recording.mov&size=6", "abc")
	if code != http.StatusOK || first["upload"] == nil || first["received"] != float64(3) {
		t.Fatalf("first part = %d %v, want an open upload at 3", code, first)
	}
	dir, _ := first["upload_dir"].(string)
	want := filepath.Join("/lasso/uploads", dir, "Screen Recording.mov")
	if dir == "" || be.createPath != want {
		t.Fatalf("created %q in dir %q, want %q", be.createPath, dir, want)
	}
	code, last := post("/api/agent-upload?upload="+first["upload"].(string)+"&offset=3", "def")
	if code != http.StatusOK || last["upload"] != nil {
		t.Fatalf("last part = %d %v", code, last)
	}
	if be.created.String() != "abcdef" || !be.created.closed {
		t.Errorf("wrote %q (closed %v)", be.created.String(), be.created.closed)
	}

	// A second file joins the first one's dir, whole.
	code, second := post("/api/agent-upload?name=notes.txt&dir="+dir, "hi")
	if code != http.StatusOK || second["upload_dir"] != dir || be.createPath != filepath.Join("/lasso/uploads", dir, "notes.txt") {
		t.Fatalf("second file = %d %v at %q", code, second, be.createPath)
	}

	// ?dir= is client-supplied: it may only name a staging id.
	be.createPath = ""
	if code, _ := post("/api/agent-upload?name=x&dir=..%2Fworktrees", "x"); code != http.StatusBadRequest || be.createPath != "" {
		t.Errorf("a traversing dir = %d, created %q; want 400 and nothing", code, be.createPath)
	}
}
