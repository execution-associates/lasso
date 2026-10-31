package main

import (
	"archive/zip"
	"io"
	"io/fs"
	"log"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
)

// serveFileZip streams a directory as a zip download: GET /api/file-zip?path=
// &host=. Same reach as /api/file (any absolute path on the host the request
// names), so it adds no access, only a format.
//
// The archive is written straight to the response, so nothing is buffered or
// staged on disk however large the folder is. Once the first byte is out the
// status is committed: a read error after that ends the stream early and the
// browser reports a failed or truncated download, which is the honest result.
//
// Only directories and regular files go in. Symlinks are skipped, never
// followed, so a link to / cannot turn a small folder into the whole disk, and
// a FIFO or device would block or never end on Open.
func serveFileZip(w http.ResponseWriter, r *http.Request) {
	be, err := reqHostBackend(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	root := filepath.Clean(expandTildeOn(be, r.URL.Query().Get("path")))
	if !filepath.IsAbs(root) {
		http.Error(w, "path must be absolute", http.StatusBadRequest)
		return
	}
	info, err := be.Stat(root)
	if err != nil || !info.IsDir() {
		http.Error(w, "not a directory", http.StatusNotFound)
		return
	}
	base := filepath.Base(root)
	if base == "/" || base == "." {
		base = "root"
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(base+".zip"))
	w.Header().Set("Cache-Control", "no-store")

	zw := zip.NewWriter(w)
	if err := zipDir(r, be, zw, root, base, true); err != nil {
		// Headers are gone; all we can do is stop and leave the archive
		// without its central directory, which every unzipper rejects.
		log.Printf("file-zip %s on %s: %v", root, be.Name(), err)
		return
	}
	if err := zw.Close(); err != nil {
		log.Printf("file-zip %s on %s: %v", root, be.Name(), err)
	}
}

// zipDir adds dir's tree under the archive prefix name. Entry names always use
// forward slashes, whatever the host.
func zipDir(r *http.Request, be Backend, zw *zip.Writer, dir, name string, top bool) error {
	if err := r.Context().Err(); err != nil {
		return err
	}
	if _, err := zw.Create(name + "/"); err != nil {
		return err
	}
	ents, err := be.ReadDir(dir)
	if err != nil {
		// An unreadable subfolder (permissions) should not sink the whole
		// download: it goes in empty.
		if top {
			return err
		}
		return nil
	}
	for _, e := range ents {
		full := filepath.Join(dir, e.Name)
		entry := path.Join(name, e.Name)
		li, err := be.Lstat(full)
		if err != nil {
			continue
		}
		switch {
		case li.IsDir():
			if err := zipDir(r, be, zw, full, entry, false); err != nil {
				return err
			}
		case li.Mode().IsRegular():
			if err := zipFile(be, zw, full, entry, li); err != nil {
				return err
			}
		}
	}
	return nil
}

func zipFile(be Backend, zw *zip.Writer, full, entry string, info fs.FileInfo) error {
	f, err := be.Open(full)
	if err != nil {
		return nil // unreadable file: skip it, keep the rest
	}
	defer f.Close()
	hdr := &zip.FileHeader{Name: entry, Method: zip.Deflate, Modified: info.ModTime()}
	hdr.SetMode(info.Mode())
	out, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, f)
	return err
}
