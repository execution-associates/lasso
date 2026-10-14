package main

import (
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// pasteChunkBytes is the part size clients send a big paste in: well under
// Cloudflare's 100 MB request-body cap in front of lasso.orangecountyai.com.
// The web and Mac clients restate it; lasso takes a part of any size.
const pasteChunkBytes = 50 << 20

// pasteUploadIdle is how long a part-way upload waits for its next part before
// its file is closed and removed: a client that went away leaves no half file.
const pasteUploadIdle = 10 * time.Minute

// pasteUploadReply answers a part that is not the last.
type pasteUploadReply struct {
	Path     string `json:"path"`
	Upload   string `json:"upload"`
	Received int64  `json:"received"`
}

// pasteUpload is a file being written in parts. Its writer stays open between
// requests, so a part appends through the same handle on any backend (an sftp
// file on another host included) with no seek or reopen.
type pasteUpload struct {
	mu       sync.Mutex
	be       Backend
	path     string
	out      io.WriteCloser
	size     int64
	received int64
	done     bool
	timer    *time.Timer
	// last is the latest part's arrival: a timer that fired just as a part
	// came in and reset it must not end an upload that is still moving.
	last time.Time
}

type pasteUploadTable struct {
	mu sync.Mutex
	m  map[string]*pasteUpload
}

var pasteUploads = &pasteUploadTable{m: map[string]*pasteUpload{}}

// begin records an upload whose first part landed and answers its token, the
// only thing a next part needs (and the authority for appending to it).
func (t *pasteUploadTable) begin(be Backend, path string, out io.WriteCloser, size, received int64) string {
	tok := randHex(16)
	u := &pasteUpload{be: be, path: path, out: out, size: size, received: received, last: time.Now()}
	t.mu.Lock()
	t.m[tok] = u
	t.mu.Unlock()
	u.timer = time.AfterFunc(pasteUploadIdle, func() { t.abandon(tok) })
	return tok
}

func (t *pasteUploadTable) get(tok string) *pasteUpload {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.m[tok]
}

func (t *pasteUploadTable) drop(tok string) {
	t.mu.Lock()
	delete(t.m, tok)
	t.mu.Unlock()
}

// abandon closes and removes an upload that stopped short.
func (t *pasteUploadTable) abandon(tok string) {
	u := t.get(tok)
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.done || time.Since(u.last) < pasteUploadIdle {
		return
	}
	u.fail()
	t.drop(tok)
}

// fail ends the upload and removes its partial file; u.mu is held.
func (u *pasteUpload) fail() {
	u.done = true
	u.timer.Stop()
	_ = u.out.Close()
	_ = u.be.RemoveAll(u.path)
}

// servePasteChunk appends one part to the upload `tok` names. ?offset= must be
// the bytes received so far, so a retried or reordered part is refused rather
// than written twice; a refusal or a failed write ends the upload and removes
// its file, and the client starts over.
func servePasteChunk(w http.ResponseWriter, r *http.Request, tok string) {
	u := pasteUploads.get(tok)
	if u == nil {
		http.Error(w, "no such upload (it may have expired)", http.StatusNotFound)
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.done {
		http.Error(w, "no such upload (it may have expired)", http.StatusNotFound)
		return
	}
	u.timer.Stop()
	u.last = time.Now()
	off, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || off != u.received {
		u.fail()
		pasteUploads.drop(tok)
		http.Error(w, "offset "+r.URL.Query().Get("offset")+" is not "+strconv.FormatInt(u.received, 10), http.StatusConflict)
		return
	}
	n, copyErr := io.Copy(u.out, http.MaxBytesReader(w, r.Body, u.size-u.received))
	u.received += n
	switch {
	case copyErr != nil:
		u.fail()
		pasteUploads.drop(tok)
		http.Error(w, "write: "+copyErr.Error(), http.StatusBadRequest)
		return
	case n == 0:
		u.fail()
		pasteUploads.drop(tok)
		http.Error(w, "empty part", http.StatusBadRequest)
		return
	case u.received < u.size:
		u.last = time.Now()
		u.timer.Reset(pasteUploadIdle)
		writeJSON(w, pasteUploadReply{Path: u.path, Upload: tok, Received: u.received})
		return
	}
	u.done = true
	pasteUploads.drop(tok)
	if err := u.out.Close(); err != nil {
		_ = u.be.RemoveAll(u.path)
		http.Error(w, "write: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"path": u.path})
}
