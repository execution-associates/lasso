package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
)

// POST /api/file-stat: one round trip that stats a batch of files on one host.
// The Mac app's file windows (mac/PLAN.md §26) watch the file they show plus
// every file its page pulled in (a markdown document's images), and polling
// /api/file with a HEAD per path would cost one request per image per second
// per window, over Cloudflare for a remote lasso. Paths answer in request
// order, a missing file is exists:false rather than an error, and a host lasso
// may not drive is refused before any read, as on every file endpoint.

const maxFileStatPaths = 64

type fileStatReq struct {
	Host  string   `json:"host"`
	Paths []string `json:"paths"`
}

type fileStatEntry struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Dir    bool   `json:"dir"`
	Size   int64  `json:"size"`
	Mtime  int64  `json:"mtime"` // unix milliseconds, the same clock as X-Lasso-Mtime
	Error  string `json:"error,omitempty"`
}

type fileStatResp struct {
	Host  string          `json:"host"`
	Files []fileStatEntry `json:"files"`
}

func serveFileStat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req fileStatReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Paths) == 0 || len(req.Paths) > maxFileStatPaths {
		http.Error(w, "paths must name 1 to 64 files", http.StatusBadRequest)
		return
	}
	be, err := namedHostBackend(req.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	out := fileStatResp{Host: be.Name(), Files: make([]fileStatEntry, len(req.Paths))}
	// A remote host answers over one pooled SFTP connection, which serves
	// concurrent requests; a few at a time keeps a 64-path batch from paying
	// 64 sequential round trips without flooding the channel.
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, p := range req.Paths {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			out.Files[i] = statOne(be, p)
		}()
	}
	wg.Wait()
	writeJSON(w, out)
}

func statOne(be Backend, raw string) fileStatEntry {
	e := fileStatEntry{Path: raw}
	path := filepath.Clean(expandTildeOn(be, raw))
	if !filepath.IsAbs(path) {
		e.Error = "path must be absolute"
		return e
	}
	info, err := be.Stat(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			e.Error = err.Error()
		}
		return e
	}
	e.Exists = true
	e.Dir = info.IsDir()
	e.Size = info.Size()
	e.Mtime = info.ModTime().UnixMilli()
	return e
}

// errNotWithin is /api/file's 403 for a path outside its `within` root.
var errNotWithin = errors.New("outside the allowed folder")

// fileWithin is /api/file's `within=<root>`: the Mac app renders an HTML file
// whose own scripts run, and lets it load its sibling files (CSS, images, JS)
// through this. Native code confines the page lexically as well, but the
// server is the check that sees the filesystem: the path must sit under root,
// no segment below root may be hidden (a project's .env, .git, .ssh), and no
// segment may be a symlink, which could point anywhere. A root at or above the
// home directory (an HTML file saved straight into ~ or /) would otherwise
// hand the page the whole home, so there only the root's own files count.
func fileWithin(be Backend, root, path string) error {
	root = filepath.Clean(expandTildeOn(be, root))
	if !filepath.IsAbs(root) {
		return errNotWithin
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return errNotWithin
	}
	segs := strings.Split(rel, "/")
	for _, s := range segs {
		if s == "" || strings.HasPrefix(s, ".") {
			return errNotWithin
		}
	}
	if len(segs) > 1 && rootTooWide(be, root) {
		return errNotWithin
	}
	cur := root
	for _, s := range segs {
		cur = filepath.Join(cur, s)
		info, err := be.Lstat(cur)
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return errNotWithin
		}
	}
	return nil
}

// rootTooWide reports whether root is /, the host's home, or above the home.
func rootTooWide(be Backend, root string) bool {
	if root == "/" {
		return true
	}
	home, err := be.HomeDir()
	if err != nil || home == "" {
		return false
	}
	home = filepath.Clean(home)
	return root == home || strings.HasPrefix(home, root+"/")
}
