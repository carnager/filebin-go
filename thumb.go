package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"os"
	"sync"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const thumbMax = 400

var thumbMu sync.Mutex

// thumbnail returns the path of a cached JPEG thumbnail for meta, generating it if needed.
func (s *Server) thumbnail(meta *FileMeta) (string, error) {
	if !canThumb(meta.Mimetype) {
		return "", errNotFound
	}
	out := s.store.ThumbPath(meta.Hash)
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	thumbMu.Lock()
	defer thumbMu.Unlock()
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	data, err := os.ReadFile(s.store.FilePath(meta.Hash))
	if err != nil {
		return "", err
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width*cfg.Height > 80_000_000 {
		return "", errNotFound
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", errNotFound
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > thumbMax || h > thumbMax {
		if w >= h {
			w, h = thumbMax, max(1, h*thumbMax/w)
		} else {
			w, h = max(1, w*thumbMax/h), thumbMax
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src) // flatten transparency for JPEG
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	var img image.Image = dst
	if baseMime(meta.Mimetype) == "image/jpeg" {
		img = orient(dst, exifOrientation(data))
	}
	tmp, err := os.CreateTemp(s.store.dir, ".thumb-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	err = jpeg.Encode(tmp, img, &jpeg.Options{Quality: 80})
	tmp.Close()
	if err != nil {
		return "", err
	}
	return out, os.Rename(tmp.Name(), out)
}

// exifOrientation extracts the EXIF orientation tag (1-8) from JPEG data; 1 if absent.
func exifOrientation(data []byte) int {
	r := bytes.NewReader(data)
	var marker [2]byte
	if _, err := io.ReadFull(r, marker[:]); err != nil || marker != [2]byte{0xFF, 0xD8} {
		return 1
	}
	for {
		if _, err := io.ReadFull(r, marker[:]); err != nil || marker[0] != 0xFF {
			return 1
		}
		var l uint16
		if binary.Read(r, binary.BigEndian, &l) != nil || l < 2 {
			return 1
		}
		seg := make([]byte, l-2)
		if _, err := io.ReadFull(r, seg); err != nil {
			return 1
		}
		if marker[1] == 0xDA {
			return 1
		}
		if marker[1] == 0xE1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
			o, err := tiffOrientation(seg[6:])
			if err != nil {
				return 1
			}
			return o
		}
	}
}

func tiffOrientation(t []byte) (int, error) {
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1, errors.New("bad tiff")
	}
	off := int(bo.Uint32(t[4:8]))
	if off+2 > len(t) {
		return 1, errors.New("bad ifd")
	}
	n := int(bo.Uint16(t[off:]))
	for i := 0; i < n; i++ {
		e := off + 2 + i*12
		if e+12 > len(t) {
			break
		}
		if bo.Uint16(t[e:]) == 0x0112 {
			if v := int(bo.Uint16(t[e+8:])); v >= 1 && v <= 8 {
				return v, nil
			}
		}
	}
	return 1, nil
}

// orient applies an EXIF orientation transform.
func orient(src *image.RGBA, o int) image.Image {
	if o <= 1 || o > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			dst.SetRGBA(dx, dy, src.RGBAAt(x, y))
		}
	}
	return dst
}
