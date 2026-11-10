package main

// Pixel dimensions of an image attachment (docs/design/client-api.md,
// Attachments). They are read from the header only, with the standard library,
// from at most the first clientImageHeadMax bytes; a file whose size cannot be
// read this way simply has none. Nothing here can fail an upload.

import (
	"bytes"
	"encoding/binary"
	"image"
	_ "image/gif"  // registers the GIF decoder for image.DecodeConfig
	_ "image/jpeg" // registers the JPEG decoder
	_ "image/png"  // registers the PNG decoder
	"strings"
)

// clientImageHeadMax bounds the bytes looked at. A JPEG can carry a few
// hundred KB of EXIF and colour profile ahead of its frame header; this is
// generous for that and nothing like a full decode.
const clientImageHeadMax = 1 << 20

// clientImageSize answers the width and height in pixels of an image of the
// given sniffed content type, or 0, 0 when the type is not an image or the
// header cannot be read.
func clientImageSize(mime string, data []byte) (w, h int) {
	if !strings.HasPrefix(mime, "image/") {
		return 0, 0
	}
	defer func() {
		if recover() != nil {
			w, h = 0, 0
		}
	}()
	if len(data) > clientImageHeadMax {
		data = data[:clientImageHeadMax]
	}
	if mime == "image/webp" {
		return webpSize(data)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// webpSize reads the canvas size from a WebP's first chunk: lossy (VP8 ),
// lossless (VP8L) or extended (VP8X). The standard library has no WebP
// decoder, and the header is a few bytes.
func webpSize(b []byte) (int, int) {
	if len(b) < 21 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return 0, 0
	}
	var w, h int
	switch string(b[12:16]) {
	case "VP8 ":
		// 3-byte frame tag, start code 9d 01 2a, then 14-bit sizes.
		if len(b) < 30 || b[23] != 0x9d || b[24] != 0x01 || b[25] != 0x2a {
			return 0, 0
		}
		w = int(binary.LittleEndian.Uint16(b[26:28]) & 0x3fff)
		h = int(binary.LittleEndian.Uint16(b[28:30]) & 0x3fff)
	case "VP8L":
		// Signature 0x2f, then 14-bit width-1 and height-1.
		if len(b) < 25 || b[20] != 0x2f {
			return 0, 0
		}
		bits := binary.LittleEndian.Uint32(b[21:25])
		w = int(bits&0x3fff) + 1
		h = int((bits>>14)&0x3fff) + 1
	case "VP8X":
		// Flags, 3 reserved bytes, then 24-bit canvas width-1 and height-1.
		if len(b) < 30 {
			return 0, 0
		}
		w = int(uint32(b[24])|uint32(b[25])<<8|uint32(b[26])<<16) + 1
		h = int(uint32(b[27])|uint32(b[28])<<8|uint32(b[29])<<16) + 1
	default:
		return 0, 0
	}
	if w <= 0 || h <= 0 {
		return 0, 0
	}
	return w, h
}
