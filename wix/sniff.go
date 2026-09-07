package wix

import (
	"bytes"

	"github.com/imgproxy/imgproxy/v4/imagetype"
)

// HeadSize is how much of the master is kept for sniffing and for the PNG
// resolution rules. The Python reference reads exactly this much.
const HeadSize = 262144

// StoredFormat identifies what the master actually IS, from magic bytes.
//
// NEVER branch on the media-id extension. It records the ORIGINAL UPLOAD
// format, not what is stored: the CDN re-encodes some masters and keeps the old
// extension. Measured across this site's 366 masters, four advertise ~mv2.png
// and hold lossy WebP, and the .webp masters split 9 lossy to 1 lossless.
// WIX-URL-SPEC §1.1.
func StoredFormat(head []byte) imagetype.Type {
	switch {
	case len(head) >= 8 && bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return imagetype.PNG
	case len(head) >= 3 && bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}):
		return imagetype.JPEG
	case isRIFFWebP(head):
		return imagetype.WEBP
	case len(head) >= 6 && (bytes.HasPrefix(head, []byte("GIF87a")) || bytes.HasPrefix(head, []byte("GIF89a"))):
		return imagetype.GIF
	case len(head) >= 12 && bytes.Equal(head[4:8], []byte("ftyp")) &&
		bytes.Contains(head[8:12], []byte("avif")):
		return imagetype.AVIF
	case len(head) >= 12 && bytes.Equal(head[4:8], []byte("ftyp")):
		return imagetype.HEIC
	}
	return imagetype.Unknown
}

// StoredLossy decides the WebP codec from the STORED master's own nature.
//
//	stored master lossless (PNG)      -> VP8L (lossless webp)
//	stored master lossy (JPEG, WebP)  -> VP8  (lossy webp)
//
// It is NOT alpha presence -- most VP8L masters here are fully opaque -- and it
// is NOT encode-both-and-keep-the-smaller: lossy is smaller in every measured
// case, including all the ones that came back lossless. WIX-URL-SPEC §5.
//
// A VP8X (extended) container counts as lossy even when its inner stream is
// lossless. That is imprecise, but it is what was measured, and the reference
// does the same.
func StoredLossy(head []byte) bool {
	if isRIFFWebP(head) {
		if len(head) >= 16 && bytes.Equal(head[12:16], []byte("VP8L")) {
			return false
		}
		return true
	}
	return StoredFormat(head) == imagetype.JPEG
}

func isRIFFWebP(head []byte) bool {
	return len(head) >= 12 &&
		bytes.Equal(head[0:4], []byte("RIFF")) &&
		bytes.Equal(head[8:12], []byte("WEBP"))
}
