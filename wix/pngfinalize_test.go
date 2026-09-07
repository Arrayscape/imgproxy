package wix

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

// minimalPNG builds a chunk stream for testing the container rules.
func minimalPNG(t *testing.T, extra ...Chunk) []byte {
	t.Helper()
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], 640)
	binary.BigEndian.PutUint32(ihdr[4:8], 480)

	phys := make([]byte, 9)
	binary.BigEndian.PutUint32(phys[0:4], 2834)
	binary.BigEndian.PutUint32(phys[4:8], 2834)
	phys[8] = 1

	cs := []Chunk{{Type: []byte("IHDR"), Data: ihdr}}
	cs = append(cs, extra...)
	cs = append(cs,
		Chunk{Type: []byte("pHYs"), Data: phys},
		Chunk{Type: []byte("IDAT"), Data: []byte{1, 2, 3}},
		Chunk{Type: []byte("IEND"), Data: nil},
	)
	return JoinPNG(cs)
}

func chunkTypes(t *testing.T, b []byte) []string {
	t.Helper()
	cs, err := SplitPNG(b)
	require.NoError(t, err)
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = string(c.Type)
	}
	return out
}

func TestCanonicalEXIFIsExactly180Bytes(t *testing.T) {
	b := CanonicalEXIF(640, 480, 2834, 0, 0, false)
	require.Len(t, b, CanonicalEXIFLen)
	require.Equal(t, "II", string(b[0:2]), "little-endian TIFF header")

	// PixelXDimension / PixelYDimension carry the OUTPUT size.
	require.Contains(t, string(b), "0210", "ExifVersion")
	require.Contains(t, string(b), "0100", "FlashpixVersion")
}

func TestCanonicalEXIFResolutionIsTruncatedNotRounded(t *testing.T) {
	// 2834 px/m -> 2834*0.0254*1000 = 71983.6 -> 71983, not 71984.
	b := CanonicalEXIF(10, 10, 2834, 0, 0, false)
	num := binary.LittleEndian.Uint32(b[86:90])
	den := binary.LittleEndian.Uint32(b[90:94])
	require.Equal(t, uint32(71983), num)
	require.Equal(t, uint32(1000), den)
}

func TestFinalizeDropsZTXtAndReplacesEXIF(t *testing.T) {
	src := minimalPNG(t,
		Chunk{Type: []byte("zTXt"), Data: []byte("leak me")},
		Chunk{Type: []byte("eXIf"), Data: []byte("MM\x00\x2a" + "\x00\x00\x00\x08" + "\x00\x00")},
	)
	out, err := FinalizePNG(src, nil)
	require.NoError(t, err)

	types := chunkTypes(t, out)
	require.NotContains(t, types, "zTXt", "libvips writes zTXt; the CDN does not")
	require.Contains(t, types, "eXIf")

	cs, _ := SplitPNG(out)
	for _, c := range cs {
		if string(c.Type) == "eXIf" {
			require.Len(t, c.Data, CanonicalEXIFLen)
			require.NotContains(t, string(c.Data), "leak")
		}
	}
}

func TestFinalizeInsertsEXIFWhenLibvipsOmittedIt(t *testing.T) {
	out, err := FinalizePNG(minimalPNG(t), nil)
	require.NoError(t, err)
	types := chunkTypes(t, out)

	// The CDN always emits eXIf. It sits after IHDR and before pHYs.
	require.Equal(t, []string{"IHDR", "eXIf", "pHYs", "IDAT", "IEND"}, types)
}

func TestFinalizeInsertsEXIFAfterICCPWhenProfiled(t *testing.T) {
	src := minimalPNG(t, Chunk{Type: []byte("iCCP"), Data: []byte("p\x00\x00x")})
	out, err := FinalizePNG(src, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"IHDR", "iCCP", "eXIf", "pHYs", "IDAT", "IEND"}, chunkTypes(t, out))
}

func TestFinalizeStripsSourceEXIFEvenWithoutAMaster(t *testing.T) {
	// This is the security property: a master's GPS/camera tags must never
	// survive into a rendition, whatever the source carried.
	gpsy := append([]byte("II\x2a\x00\x08\x00\x00\x00\x00\x00"), []byte("GPS 51.5074N 0.1278W Canon EOS")...)
	out, err := FinalizePNG(minimalPNG(t, Chunk{Type: []byte("eXIf"), Data: gpsy}), nil)
	require.NoError(t, err)
	require.False(t, bytes.Contains(out, []byte("GPS")), "GPS data must not survive")
	require.False(t, bytes.Contains(out, []byte("Canon")), "camera model must not survive")
}

func TestMasterResolutionNormalises(t *testing.T) {
	// A JPEG APP1 carrying XResolution=216/1, ResolutionUnit=inch.
	exif := buildTestEXIF(t, 216, 1, 2)
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0, 0}, append([]byte("Exif\x00\x00"), exif...)...)

	num, den, ok := MasterResolution(jpeg)
	require.True(t, ok)
	require.Equal(t, uint32(216000), num, "216/1 is normalised to 216000/1000")
	require.Equal(t, uint32(1000), den)
}

func TestMasterResolution72dpiKeepsTheDefaultRational(t *testing.T) {
	// 72 dpi is the CDN's default and it emits 72/1 whatever form the master
	// used -- both 720000/10000 and 72/1 come back as 72/1.
	for _, r := range [][2]uint32{{72, 1}, {720000, 10000}} {
		exif := buildTestEXIF(t, r[0], r[1], 2)
		jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0, 0}, append([]byte("Exif\x00\x00"), exif...)...)
		num, den, ok := MasterResolution(jpeg)
		require.True(t, ok)
		require.Equal(t, uint32(72), num)
		require.Equal(t, uint32(1), den)
	}
}

func TestJFIFIsMetric(t *testing.T) {
	mk := func(unit byte) []byte {
		b := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 16}
		b = append(b, []byte("JFIF\x00")...)
		b = append(b, 1, 2, unit, 0, 0, 0, 0)
		return b
	}
	require.True(t, JFIFIsMetric(mk(2)), "unit 2 is dots per cm")
	require.False(t, JFIFIsMetric(mk(1)), "unit 1 is dots per inch")
	require.False(t, JFIFIsMetric([]byte("\x89PNG\r\n\x1a\n")), "not a JPEG")
}

// buildTestEXIF makes a minimal little-endian TIFF with XResolution and
// ResolutionUnit in IFD0.
func buildTestEXIF(t *testing.T, num, den uint32, unit uint16) []byte {
	t.Helper()
	var b bytes.Buffer
	le := binary.LittleEndian
	b.WriteString("II")
	binary.Write(&b, le, uint16(42))
	binary.Write(&b, le, uint32(8))
	binary.Write(&b, le, uint16(2)) // 2 entries

	binary.Write(&b, le, uint16(282))
	binary.Write(&b, le, uint16(5))
	binary.Write(&b, le, uint32(1))
	binary.Write(&b, le, uint32(8+2+2*12+4)) // rational sits after the IFD

	binary.Write(&b, le, uint16(296))
	binary.Write(&b, le, uint16(3))
	binary.Write(&b, le, uint32(1))
	binary.Write(&b, le, unit)
	binary.Write(&b, le, uint16(0))

	binary.Write(&b, le, uint32(0)) // next IFD
	binary.Write(&b, le, num)
	binary.Write(&b, le, den)
	return b.Bytes()
}

func TestFinalizeEmitsExactlyOneEXIF(t *testing.T) {
	// libvips 8.15.5 writes eXIf twice for a source that carries one: after
	// IHDR and again after the IDATs. The CDN emits one.
	src := minimalPNG(t,
		Chunk{Type: []byte("eXIf"), Data: []byte("II\x2a\x00\x08\x00\x00\x00\x00\x00")},
	)
	cs, err := SplitPNG(src)
	require.NoError(t, err)
	// Splice a second eXIf in after the IDAT, as libvips does.
	withTwo := make([]Chunk, 0, len(cs)+1)
	for _, c := range cs {
		if string(c.Type) == "IEND" {
			withTwo = append(withTwo, Chunk{Type: []byte("eXIf"), Data: []byte("II\x2a\x00\x08\x00\x00\x00\x00\x00")})
		}
		withTwo = append(withTwo, c)
	}

	out, err := FinalizePNG(JoinPNG(withTwo), nil)
	require.NoError(t, err)

	var n int
	for _, tt := range chunkTypes(t, out) {
		if tt == "eXIf" {
			n++
		}
	}
	require.Equal(t, 1, n, "exactly one eXIf chunk must survive")
	require.Equal(t, []string{"IHDR", "eXIf", "pHYs", "IDAT", "IEND"}, chunkTypes(t, out))
}
