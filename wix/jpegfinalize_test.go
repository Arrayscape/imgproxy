package wix

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

// jpegWith builds a JPEG header with the given APPn segments, plus a stub
// coded stream so the finalizer has something to preserve.
func jpegWith(segs ...jpegSegment) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xFF, jpegSOI})
	for _, s := range segs {
		b.Write([]byte{0xFF, s.Marker})
		var n [2]byte
		binary.BigEndian.PutUint16(n[:], uint16(len(s.Data)+2))
		b.Write(n[:])
		b.Write(s.Data)
	}
	b.Write([]byte{0xFF, 0xDB, 0x00, 0x03, 0x00}) // a stub DQT
	b.Write([]byte{0xFF, jpegEOI})
	return b.Bytes()
}

func jpegMarkers(t *testing.T, b []byte) []byte {
	t.Helper()
	segs, _, err := splitJPEGHeader(b)
	require.NoError(t, err)
	out := make([]byte, len(segs))
	for i, s := range segs {
		out[i] = s.Marker
	}
	return out
}

func TestFinalizeJPEGEmitsTheCDNHeader(t *testing.T) {
	// SOI APP1(Exif) [APP2(ICC_PROFILE)] DQT ... EOI, with no APP0 JFIF.
	in := jpegWith(
		jpegSegment{jpegAPP0, []byte("JFIF\x00\x01\x02\x01\x00\x48\x00\x48\x00\x00")},
		jpegSegment{jpegAPP1, append([]byte("Exif\x00\x00"), []byte("MM\x00\x2a...")...)},
		jpegSegment{jpegAPP2, append([]byte("ICC_PROFILE\x00\x01\x01"), bytes.Repeat([]byte{7}, 100)...)},
	)
	out, err := FinalizeJPEG(in, nil, 100, 80)
	require.NoError(t, err)

	require.Equal(t, []byte{jpegAPP1, jpegAPP2}, jpegMarkers(t, out),
		"exactly APP1 then APP2; the APP0 JFIF is dropped")
}

func TestFinalizeJPEGDropsXMPAndForeignAPPn(t *testing.T) {
	// libvips appends the master's XMP -- 41720 bytes on one measured master.
	in := jpegWith(
		jpegSegment{jpegAPP1, append([]byte("Exif\x00\x00"), []byte("MM\x00\x2a")...)},
		jpegSegment{jpegAPP1, []byte("http://ns.adobe.com/xap/1.0/\x00CANARY-XMP")},
		jpegSegment{0xED, []byte("CANARY-PHOTOSHOP-IRB")},
	)
	out, err := FinalizeJPEG(in, nil, 10, 10)
	require.NoError(t, err)

	require.Equal(t, []byte{jpegAPP1}, jpegMarkers(t, out))
	require.False(t, bytes.Contains(out, []byte("CANARY-XMP")))
	require.False(t, bytes.Contains(out, []byte("CANARY-PHOTOSHOP-IRB")))
}

func TestFinalizeJPEGReplacesEXIFWithTheCanonicalBlock(t *testing.T) {
	// The biggest metadata leak of any output format if skipped: a full camera
	// EXIF block with an embedded thumbnail.
	camera := append([]byte("Exif\x00\x00"),
		[]byte("MM\x00\x2a\x00\x00\x00\x08CANARY-CAMERA-MAKE")...)
	out, err := FinalizeJPEG(jpegWith(jpegSegment{jpegAPP1, camera}), nil, 100, 80)
	require.NoError(t, err)

	segs, _, err := splitJPEGHeader(out)
	require.NoError(t, err)
	require.Len(t, segs[0].Data, 186, `"Exif\0\0" + the canonical 180 bytes`)
	require.Equal(t, "II", string(segs[0].Data[6:8]), "rebuilt little-endian")
	require.False(t, bytes.Contains(out, []byte("CANARY-CAMERA-MAKE")))
}

func TestFinalizeJPEGKeepsICCVerbatim(t *testing.T) {
	// Wix's sRGB profile is 456 bytes in one chunk; the multi-segment form
	// never arises.
	icc := append([]byte("ICC_PROFILE\x00\x01\x01"), bytes.Repeat([]byte{0xAB}, 456)...)
	out, err := FinalizeJPEG(jpegWith(jpegSegment{jpegAPP2, icc}), nil, 10, 10)
	require.NoError(t, err)

	segs, _, err := splitJPEGHeader(out)
	require.NoError(t, err)
	require.Len(t, segs, 2)
	require.Equal(t, icc, segs[1].Data, "APP2 must be byte-identical")
}

func TestFinalizeJPEGPreservesTheCodedStream(t *testing.T) {
	// From the first DQT to EOI is already exact and must not be touched.
	in := jpegWith(jpegSegment{jpegAPP1, []byte("Exif\x00\x00MM\x00\x2a")})
	out, err := FinalizeJPEG(in, nil, 10, 10)
	require.NoError(t, err)

	_, restIn, _ := splitJPEGHeader(in)
	_, restOut, _ := splitJPEGHeader(out)
	require.Equal(t, restIn, restOut)
}

func TestFinalizeJPEGResolutionFromMasterJFIF(t *testing.T) {
	// §8.5: a JPEG has no pHYs, so step 3 reads the MASTER's JFIF density.
	// Unit 2, density 28 -> 2800 px/m -> 71119/1000. The 2834 default would
	// give 71983 and miss by four bytes.
	master := append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10},
		[]byte("JFIF\x00\x01\x02\x02\x00\x1c\x00\x1c\x00\x00")...)

	px, ok := jfifDensity(master)
	require.True(t, ok)
	require.Equal(t, uint32(2800), px)

	out, err := FinalizeJPEG(jpegWith(jpegSegment{jpegAPP1, []byte("Exif\x00\x00MM\x00\x2a")}),
		master, 10, 10)
	require.NoError(t, err)
	segs, _, _ := splitJPEGHeader(out)
	exif := segs[0].Data[6:]
	require.Equal(t, uint32(71119), binary.LittleEndian.Uint32(exif[86:90]))
}

func TestJPEGQualityRule(t *testing.T) {
	// §7.4, which is NOT the §7.2 WebP rule.
	q := func(path string) int {
		r, err := ParsePath("/m~mv2.jpg/v1/" + path + "/n.jpg")
		require.NoError(t, err)
		_, enc := r.Params()
		return enc.JPEGQuality()
	}

	// enc_ on EVERY segment -> 80, q_N ignored.
	require.Equal(t, 80, q("fill/w_10,h_10,enc_avif"))
	require.Equal(t, 80, q("fill/w_10,h_10,q_85,enc_avif"))
	require.Equal(t, 80, q("crop/x_0,y_0,w_9,h_9,enc_auto/fill/w_5,h_5,enc_avif"))

	// enc_ on SOME segments -> 90. This is production's shape: the crop
	// segment carries no enc, so q_85 encodes at 90 -- not 85, and not 80.
	require.Equal(t, 90, q("crop/x_0,y_0,w_9,h_9/fill/w_5,h_5,q_85,enc_avif,quality_auto"))

	// no enc_ anywhere -> the LAST segment's q_N, default 90.
	require.Equal(t, 90, q("fill/w_10,h_10"))
	require.Equal(t, 70, q("fill/w_10,h_10,q_70"))
	require.Equal(t, 60, q("crop/x_0,y_0,w_9,h_9,q_95/fill/w_5,h_5,q_60"),
		"the LAST segment's q_N wins")

	// quality_N is inert for JPEG.
	require.Equal(t, 90, q("fill/w_10,h_10,quality_20"))
}

func TestSplitJPEGRejectsNonJPEG(t *testing.T) {
	_, _, err := splitJPEGHeader([]byte("\x89PNG\r\n\x1a\n"))
	require.ErrorIs(t, err, ErrNotJPEG)
}
