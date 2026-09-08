package wix

import (
	"bytes"

	"github.com/imgproxy/imgproxy/v4/imagetype"
)

// HeadSize is how much of the master is kept for sniffing and for the PNG
// resolution rules. The Python reference reads exactly this much.
const HeadSize = 262144

// StoredFormat identifies what the master actually IS.
//
// NEVER branch on the media-id extension. It records the ORIGINAL UPLOAD
// format, not what is stored: the CDN re-encodes some masters and keeps the old
// extension. Measured across one site's 366 masters, four advertise ~mv2.png
// and hold lossy WebP, and the .webp masters split 9 lossy to 1 lossless.
// WIX-URL-SPEC §1.1.
//
// Detection is delegated to imgproxy's own registry rather than hand-rolled, so
// every format imgproxy can load is recognised here too -- including TIFF and
// JXL, which a hand-written sniffer covering only the formats one corpus
// happened to contain would silently reject.
func StoredFormat(head []byte) imagetype.Type {
	t, err := imagetype.Detect(bytes.NewReader(head), "", "")
	if err != nil {
		return imagetype.Unknown
	}
	return t
}

// StoredLossy decides the WebP codec from the STORED master's own nature:
//
//	stored master lossless  -> VP8L (lossless webp)
//	stored master lossy     -> VP8  (lossy webp)
//
// It is NOT alpha presence -- most VP8L masters are fully opaque -- and it is
// NOT encode-both-and-keep-the-smaller: lossy is smaller in every measured
// case, including all the ones that came back lossless. WIX-URL-SPEC §5.
//
// Only PNG (lossless) and JPEG/WebP (lossy) are measured. The rest follow from
// the format's own nature:
//
//	lossy     JPEG, HEIC, AVIF, WebP/VP8
//	lossless  PNG, TIFF, GIF, BMP, ICO, WebP/VP8L
//
// JXL is genuinely ambiguous -- it encodes both ways and telling them apart
// needs the codestream, not the header -- so it is treated as lossless, which
// costs size rather than fidelity. Unverified against the CDN, as is everything
// outside PNG/JPEG/WebP here.
func StoredLossy(head []byte) bool {
	switch StoredFormat(head) {
	case imagetype.JPEG, imagetype.HEIC, imagetype.AVIF:
		return true
	case imagetype.WEBP:
		return !isLosslessWebP(head)
	default:
		return false
	}
}

// isLosslessWebP reports a VP8L stream.
//
// A VP8X (extended) container counts as lossy even when its inner stream is
// not. That is imprecise, but it is what was measured and what the reference
// does.
func isLosslessWebP(head []byte) bool {
	return len(head) >= 16 &&
		bytes.Equal(head[0:4], []byte("RIFF")) &&
		bytes.Equal(head[8:12], []byte("WEBP")) &&
		bytes.Equal(head[12:16], []byte("VP8L"))
}
