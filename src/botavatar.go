package main

// A bot's picture: one image in its folder (.lasso/avatar.<ext>), set from
// the Bots view's settings or by the bot itself (set_bot_avatar), served at
// /api/bots/<name>/avatar. Raster only, told by its bytes rather than its
// name: an SVG is a document that can carry script, and lasso serves this from
// its own origin.

import (
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

const botAvatarMax = 2 << 20

var botAvatarTypes = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/webp": "webp",
	"image/gif":  "gif",
}

func botAvatarDir(b Backend, r *botRecord) string {
	return filepath.Join(expandTildeOn(b, r.Dir), ".lasso")
}

// storeBotAvatar validates data and makes it the bot's picture.
func storeBotAvatar(b Backend, r *botRecord, data []byte) error {
	if len(data) == 0 || len(data) > botAvatarMax {
		return fmt.Errorf("the image must be between 1 byte and %d MB", botAvatarMax>>20)
	}
	ext, ok := botAvatarTypes[http.DetectContentType(data)]
	if !ok {
		return fmt.Errorf("the image must be a PNG, JPEG, WebP or GIF")
	}
	dir := botAvatarDir(b, r)
	if err := b.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	name := "avatar." + ext
	if err := b.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		return err
	}
	for _, other := range botAvatarTypes {
		if other != ext {
			_ = b.RemoveAll(filepath.Join(dir, "avatar."+other))
		}
	}
	return setBotAvatarImage(r.Name, fmt.Sprintf("%s?v=%d", name, time.Now().Unix()))
}

func clearBotAvatar(b Backend, r *botRecord) error {
	for _, ext := range botAvatarTypes {
		_ = b.RemoveAll(filepath.Join(botAvatarDir(b, r), "avatar."+ext))
	}
	return setBotAvatarImage(r.Name, "")
}

func setBotAvatarImage(name, image string) error {
	_, err := db.Exec(`UPDATE bots SET avatar_image = ? WHERE name = ?`, image, name)
	return err
}

// serveBotAvatar: GET the picture, PUT a new one (the raw image as the body),
// DELETE it.
func serveBotAvatar(w http.ResponseWriter, r *http.Request, b Backend, rec *botRecord) {
	switch r.Method {
	case http.MethodGet:
		file, _, _ := strings.Cut(rec.AvatarImage, "?")
		if file == "" || strings.ContainsAny(file, "/\\") {
			http.NotFound(w, r)
			return
		}
		data, err := b.ReadFile(filepath.Join(botAvatarDir(b, rec), file))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ct := http.DetectContentType(data)
		if _, ok := botAvatarTypes[ct]; !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		// The URL carries the revision, so it can be kept.
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		_, _ = w.Write(data)
	case http.MethodPut:
		data, err := readLimited(r, botAvatarMax)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := storeBotAvatar(b, rec, data); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case http.MethodDelete:
		if err := clearBotAvatar(b, rec); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "GET, PUT or DELETE", http.StatusMethodNotAllowed)
	}
}

func readLimited(r *http.Request, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("the image is larger than %d MB", max>>20)
	}
	return data, nil
}
