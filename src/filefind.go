package main

import (
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The file viewer's quick open (⌘P) lists every file under the tree's root
// once, and the browser fuzzy-matches against that list as the human types,
// so a keystroke never costs a round trip (an ssh one, on a remote host).
//
// Inside a git repository the list is `git ls-files`: one command on that
// host, already honouring .gitignore, which is what keeps node_modules and
// build output out of the results. Anywhere else it is a walk over the
// backend's ReadDir (SFTP on a remote), so it skips hidden and dependency
// directories and stops at a file cap or a time budget, saying so.
const (
	fileFindCap    = 50000
	fileFindBudget = 4 * time.Second
	fileFindDepth  = 16
)

// fileFindSkipDirs are never descended into by the walk. Hidden directories
// (.git, .venv, .cache…) are skipped by their leading dot.
var fileFindSkipDirs = map[string]bool{
	"node_modules": true,
	"__pycache__":  true,
}

func serveFileFind(w http.ResponseWriter, r *http.Request) {
	// Host first, as in serveFiles: ~ expands against that host's home.
	be, err := namedHostBackend(r.URL.Query().Get("host"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	root := filepath.Clean(expandTildeOn(be, r.URL.Query().Get("root")))
	if !filepath.IsAbs(root) {
		http.Error(w, "root must be absolute", http.StatusBadRequest)
		return
	}
	if info, err := be.Stat(root); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	} else if !info.IsDir() {
		http.Error(w, "not a directory", http.StatusBadRequest)
		return
	}
	files, truncated, source := findFiles(be, root)
	writeJSON(w, map[string]any{
		"root":      root,
		"files":     files,
		"truncated": truncated,
		"source":    source,
	})
}

// findFiles returns root-relative, slash-separated file paths, sorted.
func findFiles(be Backend, root string) (files []string, truncated bool, source string) {
	if files, truncated, ok := gitListFiles(be, root); ok {
		return files, truncated, "git"
	}
	files, truncated = walkFiles(be, root, time.Now().Add(fileFindBudget))
	return files, truncated, "walk"
}

// gitListFiles lists tracked and untracked-but-not-ignored files below root,
// relative to it (git does that itself when run from a subdirectory). ok is
// false when root is not inside a work tree, or git is missing on that host.
func gitListFiles(be Backend, root string) (files []string, truncated bool, ok bool) {
	out, err := be.GitOut(root, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, false, false
	}
	// --cached still names a file deleted from the work tree; and a file both
	// staged and modified can appear twice. Neither is worth a second git call
	// to filter: a stale entry fails to open like any vanished file would.
	seen := make(map[string]bool)
	for _, p := range strings.Split(out, "\x00") {
		if p == "" || seen[p] {
			continue
		}
		if len(files) >= fileFindCap {
			truncated = true
			break
		}
		seen[p] = true
		files = append(files, p)
	}
	sort.Strings(files)
	return files, truncated, true
}

// walkFiles is the breadth-first fallback outside a repository. Breadth-first
// so that when the cap or the deadline cuts it short, what was listed is the
// shallow part of the tree a human is most likely after.
func walkFiles(be Backend, root string, deadline time.Time) (files []string, truncated bool) {
	type dir struct {
		rel   string
		depth int
	}
	queue := []dir{{"", 0}}
	for len(queue) > 0 {
		if time.Now().After(deadline) {
			return sortedFiles(files), true
		}
		d := queue[0]
		queue = queue[1:]
		entries, err := be.ReadDir(filepath.Join(root, filepath.FromSlash(d.rel)))
		if err != nil {
			continue // unreadable subdirectory: skip it, keep the rest
		}
		for _, e := range entries {
			rel := e.Name
			if d.rel != "" {
				rel = d.rel + "/" + e.Name
			}
			if e.Dir {
				if strings.HasPrefix(e.Name, ".") || fileFindSkipDirs[e.Name] {
					continue
				}
				if d.depth+1 >= fileFindDepth {
					truncated = true
					continue
				}
				queue = append(queue, dir{rel, d.depth + 1})
				continue
			}
			if len(files) >= fileFindCap {
				return sortedFiles(files), true
			}
			files = append(files, rel)
		}
	}
	return sortedFiles(files), truncated
}

func sortedFiles(files []string) []string {
	sort.Strings(files)
	return files
}
