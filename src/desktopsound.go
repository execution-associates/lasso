package main

// The inbox arrival sound the Mac app plays, uploaded by the human and kept
// on the server so every Mac using this lasso plays the same one. One file at
// <lassoDir>/desktop/sound, served at /api/desktop/sound; its metadata rides
// in ui_state (uiState.DesktopSound), so a client learns of a change through
// the ui_state_rev it already watches and downloads the file again only when
// the sha256 there differs from its cached copy's.
//
// The type is told by the bytes, never by Content-Type or a name: the file is
// served from lasso's own origin, so it is only ever a format a Mac plays.
// The audio itself is never logged.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// desktopSoundMax caps an upload. An arrival chime is a second or two; a
// megabyte is a generous WAV of that and keeps a mistaken pick (a song) out.
const desktopSoundMax = 1 << 20

// desktopSoundNameMax bounds the display name, in runes.
const desktopSoundNameMax = 64

// desktopSoundDefaultName is the name a sound uploaded without one wears.
const desktopSoundDefaultName = "Custom sound"

// desktopSound is uiState.DesktopSound: what a client needs to show the
// sound and to decide whether its cached copy is current.
type desktopSound struct {
	Name        string `json:"name"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
	// UpdatedAt is when it was uploaded, in unix milliseconds.
	UpdatedAt int64 `json:"updated_at"`
}

// desktopSoundMu serializes the file and its metadata together, so two
// uploads racing cannot leave ui_state describing the file that lost. Taken
// before uiStateMu, never after it.
var desktopSoundMu sync.Mutex

func desktopSoundDir() string  { return filepath.Join(lassoDir(), "desktop") }
func desktopSoundPath() string { return filepath.Join(desktopSoundDir(), "sound") }

// mp4AudioBrands are the ftyp brands an audio-only MP4 (M4A, AAC) carries,
// as its major brand or among its compatible ones.
var mp4AudioBrands = map[string]bool{
	"M4A ": true, "M4B ": true, "M4P ": true, "mp42": true, "mp41": true,
	"isom": true, "iso2": true, "iso4": true, "iso5": true, "iso6": true,
	"dash": true, "3gp4": true, "3gp5": true,
}

// sniffDesktopSound names the audio format b holds, "" when it is none a
// Mac plays out of the box that this accepts: MP3, WAV, AIFF/AIFC, AAC or
// ALAC in MP4, and CAF.
func sniffDesktopSound(b []byte) string {
	switch {
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WAVE":
		return "audio/wav"
	case len(b) >= 12 && string(b[0:4]) == "FORM" && (string(b[8:12]) == "AIFF" || string(b[8:12]) == "AIFC"):
		return "audio/aiff"
	case len(b) >= 8 && string(b[0:4]) == "caff" && b[4] == 0 && b[5] == 1:
		return "audio/x-caf"
	case len(b) >= 16 && string(b[4:8]) == "ftyp":
		// The ftyp box: size, "ftyp", major brand, minor version, then
		// compatible brands to the end of the box.
		n := int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3])
		if n < 16 || n > len(b) {
			n = min(len(b), 64)
		}
		if mp4AudioBrands[string(b[8:12])] {
			return "audio/mp4"
		}
		for i := 16; i+4 <= n; i += 4 {
			if mp4AudioBrands[string(b[i:i+4])] {
				return "audio/mp4"
			}
		}
		return ""
	case len(b) >= 10 && string(b[0:3]) == "ID3":
		return "audio/mpeg"
	case len(b) >= 4 && mpegAudioFrame(b):
		return "audio/mpeg"
	}
	return ""
}

// mpegAudioFrame reports whether b starts with an MPEG audio frame header:
// eleven sync bits, then a version, layer, bitrate and sample rate that are
// not the reserved values. Layer "00" is excluded, which is also what keeps
// an ADTS AAC stream (sync, layer 00) out.
func mpegAudioFrame(b []byte) bool {
	if b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return false
	}
	version := (b[1] >> 3) & 3
	layer := (b[1] >> 1) & 3
	bitrate := b[2] >> 4
	rate := (b[2] >> 2) & 3
	return version != 1 && layer != 0 && bitrate != 0xF && rate != 3
}

// sanitizeDesktopSoundName makes a caller's display name safe to show: no
// control or format characters, runs of space collapsed, at most
// desktopSoundNameMax runes, and the default when nothing is left.
func sanitizeDesktopSoundName(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == unicode.ReplacementChar {
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	out := []rune(b.String())
	if len(out) > desktopSoundNameMax {
		out = []rune(strings.TrimRightFunc(string(out[:desktopSoundNameMax]), unicode.IsSpace))
	}
	if len(out) == 0 {
		return desktopSoundDefaultName
	}
	return string(out)
}

func serveDesktopSound(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		getDesktopSound(w, r)
	case http.MethodPut:
		putDesktopSound(w, r)
	case http.MethodDelete:
		deleteDesktopSound(w)
	default:
		w.Header().Set("Allow", "GET, HEAD, PUT, DELETE")
		http.Error(w, "GET, PUT or DELETE", http.StatusMethodNotAllowed)
	}
}

// getDesktopSound serves the stored file. The type is sniffed again and the
// ETag hashed from the file itself rather than read from ui_state, so the
// answer is right even when an older lasso sharing lasso.db rewrote ui_state
// without the metadata.
func getDesktopSound(w http.ResponseWriter, r *http.Request) {
	desktopSoundMu.Lock()
	b, err := os.ReadFile(desktopSoundPath())
	desktopSoundMu.Unlock()
	if errors.Is(err, fs.ErrNotExist) {
		http.Error(w, "no desktop sound", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ct := sniffDesktopSound(b)
	if ct == "" {
		http.Error(w, "no desktop sound", http.StatusNotFound)
		return
	}
	sum := sha256.Sum256(b)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Cache-Control", "no-cache")
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", ct)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(b)
	}
}

// etagMatches reports whether an If-None-Match header names etag: "*", or a
// comma-separated list compared weakly (a W/ prefix is ignored), as RFC 9110
// asks of If-None-Match.
func etagMatches(header, etag string) bool {
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimPrefix(strings.TrimSpace(t), "W/")
		if t == "*" || t == etag {
			return true
		}
	}
	return false
}

func putDesktopSound(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, desktopSoundMax))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "a desktop sound is at most 1 MiB", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(b) == 0 {
		http.Error(w, "empty body: send the audio file as the request body", http.StatusBadRequest)
		return
	}
	ct := sniffDesktopSound(b)
	if ct == "" {
		http.Error(w, "not a supported audio file (MP3, WAV, AIFF, M4A or CAF)", http.StatusUnsupportedMediaType)
		return
	}
	sum := sha256.Sum256(b)
	meta := &desktopSound{
		Name:        sanitizeDesktopSoundName(r.URL.Query().Get("name")),
		SHA256:      hex.EncodeToString(sum[:]),
		Size:        int64(len(b)),
		ContentType: ct,
		UpdatedAt:   time.Now().UnixMilli(),
	}

	desktopSoundMu.Lock()
	defer desktopSoundMu.Unlock()
	if err := writeDesktopSoundFile(b); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	saved, err := setDesktopSoundMeta(meta)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, saved)
}

func deleteDesktopSound(w http.ResponseWriter) {
	desktopSoundMu.Lock()
	defer desktopSoundMu.Unlock()
	if err := os.Remove(desktopSoundPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := setDesktopSoundMeta(nil); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeDesktopSoundFile replaces the stored file atomically: a reader sees
// the old sound or the new one, never half of either.
func writeDesktopSoundFile(b []byte) error {
	dir := desktopSoundDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".sound-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := io.Copy(f, bytes.NewReader(b)); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, desktopSoundPath()); err != nil {
		return err
	}
	ok = true
	return nil
}

// setDesktopSoundMeta writes uiState.DesktopSound (nil clears it) and bumps
// ui_state_rev when that changed anything. A re-upload of the same bytes
// under the same name keeps the stored record, upload time included, so it
// moves no rev and sends no client to download again.
func setDesktopSoundMeta(meta *desktopSound) (*desktopSound, error) {
	uiStateMu.Lock()
	defer uiStateMu.Unlock()
	us, err := getUIState()
	if err != nil {
		return nil, err
	}
	cur := us.DesktopSound
	if cur == nil && meta == nil {
		return nil, nil
	}
	if cur != nil && meta != nil && cur.SHA256 == meta.SHA256 && cur.Name == meta.Name &&
		cur.Size == meta.Size && cur.ContentType == meta.ContentType {
		return cur, nil
	}
	us.DesktopSound = meta
	if err := saveUIState(us); err != nil {
		return nil, err
	}
	if srvHub != nil {
		srvHub.bumpUIStateRev()
	}
	return meta, nil
}
