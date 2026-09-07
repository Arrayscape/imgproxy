package integration_test

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
	"io"
	"strconv"
)

// Small helpers for the Wix suite: PNG/WebP inspection and fixture building.

type pngChunk struct {
	typ  string
	data []byte
}

func splitPNGChunks(b []byte) ([]pngChunk, bool) {
	if len(b) < 8 || !bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) {
		return nil, false
	}
	var out []pngChunk
	off := 8
	for off+8 <= len(b) {
		n := int(binary.BigEndian.Uint32(b[off : off+4]))
		typ := string(b[off+4 : off+8])
		if n < 0 || off+12+n > len(b) {
			return out, false
		}
		out = append(out, pngChunk{typ: typ, data: b[off+8 : off+8+n]})
		if typ == "IEND" {
			break
		}
		off += 12 + n
	}
	return out, true
}

// pngChunks returns the chunk type sequence and the eXIf payload, if any.
func pngChunks(b []byte) ([]string, []byte) {
	chunks, _ := splitPNGChunks(b)
	types := make([]string, 0, len(chunks))
	var exif []byte
	for _, c := range chunks {
		types = append(types, c.typ)
		if c.typ == "eXIf" {
			exif = c.data
		}
	}
	return types, exif
}

func countOf(ss []string, want string) int {
	n := 0
	for _, s := range ss {
		if s == want {
			n++
		}
	}
	return n
}

// webpCodec reports the image chunk fourcc inside a RIFF/WEBP container:
// "VP8 " for lossy, "VP8L" for lossless. A VP8X container is walked, since
// libvips wraps output carrying metadata or alpha.
func webpCodec(b []byte) string {
	if len(b) < 16 || !bytes.Equal(b[0:4], []byte("RIFF")) || !bytes.Equal(b[8:12], []byte("WEBP")) {
		return "not-webp"
	}
	off := 12
	for off+8 <= len(b) {
		typ := string(b[off : off+4])
		n := int(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		if typ == "VP8 " || typ == "VP8L" {
			return typ
		}
		off += 8 + n + (n & 1)
	}
	return "unknown"
}

func zlibDeflate(s string) []byte {
	var b bytes.Buffer
	w := zlib.NewWriter(&b)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return b.Bytes()
}

func itoa(i int) string { return strconv.Itoa(i) }

// iccpProfile returns the decompressed ICC profile from a PNG's iCCP chunk.
func iccpProfile(b []byte) ([]byte, bool) {
	chunks, _ := splitPNGChunks(b)
	for _, c := range chunks {
		if c.typ != "iCCP" {
			continue
		}
		// name \0 compression-method compressed-profile
		i := bytes.IndexByte(c.data, 0)
		if i < 0 || i+2 > len(c.data) {
			return nil, false
		}
		zr, err := zlib.NewReader(bytes.NewReader(c.data[i+2:]))
		if err != nil {
			return nil, false
		}
		defer zr.Close()
		out, err := io.ReadAll(zr)
		if err != nil {
			return nil, false
		}
		return out, true
	}
	return nil, false
}

// withICCProfile injects an iCCP chunk into a PNG, after IHDR.
func withICCProfile(pngBytes, profile []byte) []byte {
	var comp bytes.Buffer
	zw := zlib.NewWriter(&comp)
	_, _ = zw.Write(profile)
	_ = zw.Close()

	payload := append([]byte("test\x00\x00"), comp.Bytes()...)

	chunks, _ := splitPNGChunks(pngBytes)
	var out bytes.Buffer
	out.Write([]byte("\x89PNG\r\n\x1a\n"))
	write := func(typ string, data []byte) {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(data)))
		out.Write(n[:])
		out.WriteString(typ)
		out.Write(data)
		h := crc32.NewIEEE()
		h.Write([]byte(typ))
		h.Write(data)
		var s [4]byte
		binary.BigEndian.PutUint32(s[:], h.Sum32())
		out.Write(s[:])
	}
	for _, c := range chunks {
		write(c.typ, c.data)
		if c.typ == "IHDR" {
			write("iCCP", payload)
		}
	}
	return out.Bytes()
}
