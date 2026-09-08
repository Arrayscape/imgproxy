package wix

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

// vp8lChunk builds a minimal VP8L bitstream header of the given size.
func vp8lChunk(w, h int, alpha bool) RIFFChunk {
	d := make([]byte, 5)
	d[0] = 0x2f
	bits := uint32(w-1) | uint32(h-1)<<14
	if alpha {
		bits |= 1 << 28
	}
	binary.LittleEndian.PutUint32(d[1:5], bits)
	return RIFFChunk{FourCC: "VP8L", Data: append(d, 0xff, 0xff)}
}

// vp8Chunk builds a minimal lossy keyframe header.
func vp8Chunk(w, h int) RIFFChunk {
	d := make([]byte, 10)
	d[3], d[4], d[5] = 0x9d, 0x01, 0x2a
	binary.LittleEndian.PutUint16(d[6:8], uint16(w))
	binary.LittleEndian.PutUint16(d[8:10], uint16(h))
	return RIFFChunk{FourCC: "VP8 ", Data: append(d, 0xff)}
}

func fourccs(t *testing.T, b []byte) []string {
	t.Helper()
	cs, err := SplitWebP(b)
	require.NoError(t, err)
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.FourCC
	}
	return out
}

func chunkNamed(t *testing.T, b []byte, fourcc string) []byte {
	t.Helper()
	cs, err := SplitWebP(b)
	require.NoError(t, err)
	for _, c := range cs {
		if c.FourCC == fourcc {
			return c.Data
		}
	}
	return nil
}

func TestFinalizeWebPEmitsTheCDNChunkSequence(t *testing.T) {
	// OP-SPEC §8.4: RIFF WEBP VP8X [ICCP] [ALPH] VP8|VP8L EXIF
	in := JoinWebP([]RIFFChunk{
		{FourCC: "ICCP", Data: bytes.Repeat([]byte{1}, 456)},
		vp8lChunk(100, 80, false),
		{FourCC: "XMP ", Data: []byte("<x:xmpmeta>leaked</x:xmpmeta>")},
	})

	out, err := FinalizeWebP(in, nil, 2834)
	require.NoError(t, err)

	require.Equal(t, []string{"VP8X", "ICCP", "VP8L", "EXIF"}, fourccs(t, out))
}

func TestFinalizeWebPDropsXMP(t *testing.T) {
	// libvips sometimes appends one; the CDN never emits one. It is also a
	// metadata leak: XMP carries whatever the uploader's file held.
	in := JoinWebP([]RIFFChunk{
		vp8lChunk(10, 10, false),
		{FourCC: "XMP ", Data: []byte("CANARY-XMP-PAYLOAD")},
	})
	out, err := FinalizeWebP(in, nil, 2834)
	require.NoError(t, err)

	require.NotContains(t, fourccs(t, out), "XMP ")
	require.False(t, bytes.Contains(out, []byte("CANARY-XMP-PAYLOAD")))
}

func TestFinalizeWebPRebuildsEXIF(t *testing.T) {
	// libvips forwards the master's own EXIF, which is big-endian and drops
	// YCbCrPositioning when the master had none. The block is rebuilt, not
	// edited: "Exif\0\0" + the canonical 180 bytes = 186.
	in := JoinWebP([]RIFFChunk{
		vp8lChunk(100, 80, false),
		{FourCC: "EXIF", Data: append([]byte("Exif\x00\x00"),
			[]byte("MM\x00\x2a\x00\x00\x00\x08\x00\x00CANARY-CAMERA")...)},
	})
	out, err := FinalizeWebP(in, nil, 2834)
	require.NoError(t, err)

	exif := chunkNamed(t, out, "EXIF")
	require.Len(t, exif, 186)
	require.Equal(t, "Exif\x00\x00", string(exif[:6]))
	require.Equal(t, "II", string(exif[6:8]), "the rebuilt block is little-endian")
	require.False(t, bytes.Contains(out, []byte("CANARY-CAMERA")),
		"the master's EXIF must not survive")
}

func TestFinalizeWebPAlwaysWritesVP8X(t *testing.T) {
	// libvips omits VP8X when there is no metadata to carry; the CDN always
	// writes one.
	in := JoinWebP([]RIFFChunk{vp8lChunk(300, 200, false)})
	require.NotContains(t, fourccs(t, in), "VP8X")

	out, err := FinalizeWebP(in, nil, 2834)
	require.NoError(t, err)

	vp8x := chunkNamed(t, out, "VP8X")
	require.Len(t, vp8x, 10)

	// Canvas dimensions are stored minus one, 24-bit little-endian.
	w := int(vp8x[4]) | int(vp8x[5])<<8 | int(vp8x[6])<<16
	h := int(vp8x[7]) | int(vp8x[8])<<8 | int(vp8x[9])<<16
	require.Equal(t, 299, w)
	require.Equal(t, 199, h)
}

func TestFinalizeWebPVP8XFlags(t *testing.T) {
	// 0x08 EXIF, +0x20 when ICCP is present, +0x10 when the image has alpha.
	for _, tc := range []struct {
		name string
		in   []RIFFChunk
		want byte
	}{
		{"plain", []RIFFChunk{vp8lChunk(10, 10, false)}, 0x08},
		{"alpha inline in VP8L", []RIFFChunk{vp8lChunk(10, 10, true)}, 0x18},
		{"separate ALPH chunk", []RIFFChunk{
			{FourCC: "ALPH", Data: []byte{0}}, vp8Chunk(10, 10)}, 0x18},
		{"icc profile", []RIFFChunk{
			{FourCC: "ICCP", Data: []byte{1}}, vp8lChunk(10, 10, false)}, 0x28},
		{"icc and alpha", []RIFFChunk{
			{FourCC: "ICCP", Data: []byte{1}}, vp8lChunk(10, 10, true)}, 0x38},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := FinalizeWebP(JoinWebP(tc.in), nil, 2834)
			require.NoError(t, err)
			require.Equal(t, tc.want, chunkNamed(t, out, "VP8X")[0])
		})
	}
}

func TestFinalizeWebPReadsCodedSizeFromTheBitstream(t *testing.T) {
	// The canvas size VP8X advertises is the CODED size, so it comes from the
	// bitstream rather than from whatever was requested.
	for _, tc := range []struct {
		name string
		in   RIFFChunk
	}{
		{"VP8L", vp8lChunk(137, 89, false)},
		{"VP8", vp8Chunk(137, 89)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := FinalizeWebP(JoinWebP([]RIFFChunk{tc.in}), nil, 2834)
			require.NoError(t, err)
			vp8x := chunkNamed(t, out, "VP8X")
			require.Equal(t, 136, int(vp8x[4])|int(vp8x[5])<<8|int(vp8x[6])<<16)
			require.Equal(t, 88, int(vp8x[7])|int(vp8x[8])<<8|int(vp8x[9])<<16)
		})
	}
}

func TestFinalizeWebPResolutionFollowsThePrecedence(t *testing.T) {
	// §8.3 step 3 with no master resolution: derived from the rendition's own
	// resolution, TRUNCATED. 1000 px/m is what a cache-derived rendition
	// carries, and 25400/1000 is what the CDN emits for it -- 1307 of 1307.
	out, err := FinalizeWebP(JoinWebP([]RIFFChunk{vp8lChunk(10, 10, false)}), nil, 1000)
	require.NoError(t, err)
	exif := chunkNamed(t, out, "EXIF")[6:] // strip the "Exif\0\0" prefix
	require.Equal(t, uint32(25400), binary.LittleEndian.Uint32(exif[86:90]))
	require.Equal(t, uint32(1000), binary.LittleEndian.Uint32(exif[90:94]))
}

func TestSplitWebPRejectsNonWebP(t *testing.T) {
	_, err := SplitWebP([]byte("\x89PNG\r\n\x1a\n"))
	require.ErrorIs(t, err, ErrNotWebP)
}

func TestJoinWebPPadsOddChunks(t *testing.T) {
	// RIFF chunks are padded to an even length; the pad byte is not part of
	// the declared size.
	out := JoinWebP([]RIFFChunk{{FourCC: "TEST", Data: []byte{1, 2, 3}}})
	cs, err := SplitWebP(out)
	require.NoError(t, err)
	require.Len(t, cs[0].Data, 3)
	require.Equal(t, 0, len(out)%2)
}
