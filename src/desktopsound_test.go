package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// soundRequest drives serveDesktopSound once.
func soundRequest(t *testing.T, method, target string, body []byte, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, bytes.NewReader(body))
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	serveDesktopSound(w, r)
	return w
}

// soundSamples are minimal headers of each accepted format, padded so they
// read as a file rather than a stub.
func soundSamples() map[string][]byte {
	pad := func(b []byte) []byte { return append(b, bytes.Repeat([]byte{0}, 64)...) }
	ftyp := func(major string, compat ...string) []byte {
		box := []byte(major + "\x00\x00\x00\x00" + strings.Join(compat, ""))
		n := 8 + len(box)
		return pad(append([]byte{0, 0, 0, byte(n), 'f', 't', 'y', 'p'}, box...))
	}
	return map[string][]byte{
		"mp3 id3":         pad([]byte("ID3\x04\x00\x00\x00\x00\x00\x00")),
		"mp3 frame":       pad([]byte{0xFF, 0xFB, 0x90, 0x64}),
		"wav":             pad([]byte("RIFF\x24\x00\x00\x00WAVEfmt ")),
		"aiff":            pad([]byte("FORM\x00\x00\x00\x00AIFFCOMM")),
		"aifc":            pad([]byte("FORM\x00\x00\x00\x00AIFCFVER")),
		"m4a":             ftyp("M4A ", "M4A ", "mp42", "isom"),
		"mp4 compatible":  ftyp("qt  ", "M4A "),
		"caf":             pad([]byte("caff\x00\x01\x00\x00desc")),
		"junk":            pad([]byte("hello, world")),
		"svg":             []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"png":             pad([]byte("\x89PNG\r\n\x1a\n")),
		"adts aac":        pad([]byte{0xFF, 0xF1, 0x50, 0x80}),
		"reserved frame":  pad([]byte{0xFF, 0xFB, 0xF0, 0x64}),
		"video only mp4":  ftyp("qt  ", "qt  "),
		"riff not wave":   pad([]byte("RIFF\x24\x00\x00\x00AVI LIST")),
		"form not aiff":   pad([]byte("FORM\x00\x00\x00\x00ILBMBMHD")),
		"caf bad version": pad([]byte("caff\x00\x09\x00\x00desc")),
	}
}

func TestSniffDesktopSound(t *testing.T) {
	want := map[string]string{
		"mp3 id3":        "audio/mpeg",
		"mp3 frame":      "audio/mpeg",
		"wav":            "audio/wav",
		"aiff":           "audio/aiff",
		"aifc":           "audio/aiff",
		"m4a":            "audio/mp4",
		"mp4 compatible": "audio/mp4",
		"caf":            "audio/x-caf",
	}
	for name, b := range soundSamples() {
		if got := sniffDesktopSound(b); got != want[name] {
			t.Errorf("%s: sniffed %q, want %q", name, got, want[name])
		}
	}
	if got := sniffDesktopSound([]byte{0xFF}); got != "" {
		t.Errorf("one byte sniffed as %q", got)
	}
}

func TestDesktopSoundUploadIsSniffedNotTrusted(t *testing.T) {
	openTestDB(t)
	for name, b := range soundSamples() {
		// The header claims audio for every sample; only the bytes count.
		w := soundRequest(t, http.MethodPut, "/api/desktop/sound", b, map[string]string{"Content-Type": "audio/mpeg"})
		if sniffDesktopSound(b) == "" {
			if w.Code != http.StatusUnsupportedMediaType {
				t.Errorf("%s: PUT = %d, want 415", name, w.Code)
			}
		} else if w.Code != http.StatusOK {
			t.Errorf("%s: PUT = %d: %s", name, w.Code, w.Body.String())
		}
	}
}

func TestDesktopSoundLimits(t *testing.T) {
	openTestDB(t)
	if w := soundRequest(t, http.MethodPut, "/api/desktop/sound", nil, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("empty PUT = %d, want 400", w.Code)
	}
	wav := soundSamples()["wav"]
	big := append(append([]byte{}, wav...), make([]byte, desktopSoundMax)...)
	if w := soundRequest(t, http.MethodPut, "/api/desktop/sound", big, nil); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized PUT = %d, want 413", w.Code)
	}
	exact := append(append([]byte{}, wav...), make([]byte, desktopSoundMax-len(wav))...)
	if w := soundRequest(t, http.MethodPut, "/api/desktop/sound", exact, nil); w.Code != http.StatusOK {
		t.Fatalf("a PUT of exactly the cap = %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(desktopSoundPath()); err != nil {
		t.Fatalf("the accepted upload is not on disk: %v", err)
	}
	if w := soundRequest(t, http.MethodPost, "/api/desktop/sound", wav, nil); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d, want 405", w.Code)
	}
}

func TestDesktopSoundRoundTrip(t *testing.T) {
	openTestDB(t)
	rev := uiStateRevHub(t)

	if w := soundRequest(t, http.MethodGet, "/api/desktop/sound", nil, nil); w.Code != http.StatusNotFound {
		t.Fatalf("GET with none = %d, want 404", w.Code)
	}
	if us, _ := getUIState(); us.DesktopSound != nil {
		t.Fatalf("fresh install has a sound: %+v", us.DesktopSound)
	}
	r0 := rev()

	m4a := soundSamples()["m4a"]
	sum := sha256.Sum256(m4a)
	hexsum := hex.EncodeToString(sum[:])
	w := soundRequest(t, http.MethodPut, "/api/desktop/sound?name="+
		"%20Ding%0A%E2%80%8Bdong%09%20"+strings.Repeat("x", 80), m4a, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", w.Code, w.Body.String())
	}
	us, _ := getUIState()
	s := us.DesktopSound
	wantName := "Ding dong " + strings.Repeat("x", desktopSoundNameMax-len("Ding dong "))
	if s == nil || s.SHA256 != hexsum || s.Size != int64(len(m4a)) || s.ContentType != "audio/mp4" ||
		s.Name != wantName || s.UpdatedAt <= 0 {
		t.Fatalf("metadata: %+v", s)
	}
	if rev() != r0+1 {
		t.Fatalf("PUT did not bump ui_state_rev once: %d -> %d", r0, rev())
	}
	st, err := os.Stat(desktopSoundPath())
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("stored file: %v %v", st, err)
	}
	if dst, err := os.Stat(desktopSoundDir()); err != nil || dst.Mode().Perm() != 0o700 {
		t.Fatalf("sound dir: %v %v", dst, err)
	}

	w = soundRequest(t, http.MethodGet, "/api/desktop/sound", nil, nil)
	etag := `"` + hexsum + `"`
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), m4a) {
		t.Fatalf("GET = %d, %d bytes", w.Code, w.Body.Len())
	}
	if w.Header().Get("Content-Type") != "audio/mp4" || w.Header().Get("ETag") != etag ||
		w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("GET headers: %v", w.Header())
	}
	for _, inm := range []string{etag, `"other", W/` + etag, "*"} {
		w = soundRequest(t, http.MethodGet, "/api/desktop/sound", nil, map[string]string{"If-None-Match": inm})
		if w.Code != http.StatusNotModified || w.Body.Len() != 0 || w.Header().Get("ETag") != etag {
			t.Fatalf("If-None-Match %s = %d", inm, w.Code)
		}
	}
	if w = soundRequest(t, http.MethodGet, "/api/desktop/sound", nil, map[string]string{"If-None-Match": `"stale"`}); w.Code != http.StatusOK {
		t.Fatalf("stale If-None-Match = %d", w.Code)
	}

	// The same bytes under the same name change nothing.
	r1 := rev()
	soundRequest(t, http.MethodPut, "/api/desktop/sound?name="+"%20Ding%0A%E2%80%8Bdong%09%20"+strings.Repeat("x", 80), m4a, nil)
	if again, _ := getUIState(); rev() != r1 || again.DesktopSound.UpdatedAt != s.UpdatedAt {
		t.Fatalf("an identical re-upload moved the rev or the record")
	}

	// A new sound replaces it, without a name taking the default.
	wav := soundSamples()["wav"]
	if w = soundRequest(t, http.MethodPut, "/api/desktop/sound", wav, nil); w.Code != http.StatusOK {
		t.Fatalf("second PUT = %d", w.Code)
	}
	us, _ = getUIState()
	if us.DesktopSound.Name != desktopSoundDefaultName || us.DesktopSound.ContentType != "audio/wav" || rev() != r1+1 {
		t.Fatalf("replacement: %+v rev %d", us.DesktopSound, rev())
	}
	if w = soundRequest(t, http.MethodGet, "/api/desktop/sound", nil, map[string]string{"If-None-Match": etag}); w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), wav) {
		t.Fatalf("GET after replacement = %d", w.Code)
	}

	// DELETE removes it, once and idempotently.
	r2 := rev()
	if w = soundRequest(t, http.MethodDelete, "/api/desktop/sound", nil, nil); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d", w.Code)
	}
	if us, _ = getUIState(); us.DesktopSound != nil || rev() != r2+1 {
		t.Fatalf("after DELETE: %+v rev %d", us.DesktopSound, rev())
	}
	if _, err := os.Stat(desktopSoundPath()); !os.IsNotExist(err) {
		t.Fatalf("file survived DELETE: %v", err)
	}
	if w = soundRequest(t, http.MethodDelete, "/api/desktop/sound", nil, nil); w.Code != http.StatusNoContent || rev() != r2+1 {
		t.Fatalf("second DELETE = %d rev %d", w.Code, rev())
	}
	if w = soundRequest(t, http.MethodGet, "/api/desktop/sound", nil, nil); w.Code != http.StatusNotFound {
		t.Fatalf("GET after DELETE = %d", w.Code)
	}
	if ents, _ := os.ReadDir(desktopSoundDir()); len(ents) != 0 {
		t.Fatalf("leftover files: %v", ents)
	}
}

func TestUIStatePostCannotSetDesktopSound(t *testing.T) {
	openTestDB(t)
	rev := uiStateRevHub(t)

	// None stored: a body naming one stores nothing and bumps nothing.
	r0 := rev()
	got := postUIState(t, `{"desktop_sound":{"name":"evil","sha256":"ab","size":3,"content_type":"text/html","updated_at":1},"client_id":"A"}`)
	if got.DesktopSound != nil || rev() != r0 {
		t.Fatalf("POST set desktop_sound: %+v rev %d", got.DesktopSound, rev())
	}
	if raw, _ := getSetting("ui_state"); strings.Contains(raw, "evil") {
		t.Fatalf("stored: %s", raw)
	}

	// One stored: neither a replacement, a null nor an unrelated patch moves it.
	if w := soundRequest(t, http.MethodPut, "/api/desktop/sound?name=chime", soundSamples()["caf"], nil); w.Code != http.StatusOK {
		t.Fatalf("PUT = %d", w.Code)
	}
	want, _ := getUIState()
	for _, body := range []string{
		`{"desktop_sound":{"name":"evil","sha256":"ab","size":3},"client_id":"A"}`,
		`{"desktop_sound":null,"client_id":"A"}`,
		`{"files_click_navigates":false,"client_id":"A"}`,
	} {
		got = postUIState(t, body)
		if got.DesktopSound == nil || *got.DesktopSound != *want.DesktopSound {
			t.Fatalf("%s changed it: %+v", body, got.DesktopSound)
		}
	}
	stored, _ := getUIState()
	if stored.DesktopSound == nil || *stored.DesktopSound != *want.DesktopSound || stored.FilesClickNavigates {
		t.Fatalf("stored after patches: %+v", stored)
	}
}

func TestSanitizeDesktopSoundName(t *testing.T) {
	for in, want := range map[string]string{
		"":                desktopSoundDefaultName,
		" \t\n":           desktopSoundDefaultName,
		"Glass":           "Glass",
		"a\x00b\x1bc":     "abc",
		"  two   words  ": "two words",
		"line\nbreak":     "line break",
		"zero​width":      "zerowidth",
		"rtl‮override":    "rtloverride",
	} {
		if got := sanitizeDesktopSoundName(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("é", 100)
	if got := []rune(sanitizeDesktopSoundName(long)); len(got) != desktopSoundNameMax {
		t.Errorf("long name kept %d runes", len(got))
	}
}
