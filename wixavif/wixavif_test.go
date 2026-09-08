package wixavif

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

// gradient builds w*h RGBA pixels varying in both axes, so the encoder has
// real detail to work on rather than a flat field.
func gradient(w, h int, alpha byte) []byte {
	b := make([]byte, w*h*4)
	for y := range h {
		for x := range w {
			i := (y*w + x) * 4
			b[i], b[i+1], b[i+2], b[i+3] =
				byte((x*7+y*3)%256), byte((x*x/13+y*11)%256), byte(((x^y)*5)%256), alpha
		}
	}
	return b
}

func params(minQ int) Params {
	return Params{
		MinQ: minQ, MaxQ: minQ + 8,
		MinAlphaQ: 27, MaxAlphaQ: 35,
		Speed: 9, Threads: 4,
	}
}

func TestEncodeProducesLibavifAVIF(t *testing.T) {
	out, err := Encode(gradient(64, 48, 255), 64, 48, params(13))
	require.NoError(t, err)
	require.NotEmpty(t, out)

	// ISOBMFF: the ftyp brand, then libavif's own signature. The hdlr name is
	// the discriminator -- libheif output would not carry it, and OP-SPEC §7.5
	// is explicit that the CDN's AVIFs are libavif's.
	require.Equal(t, "ftyp", string(out[4:8]))
	require.True(t, bytes.Contains(out[:200], []byte("avif")), "avif brand")
	require.True(t, bytes.Contains(out, []byte("libavif")), "hdlr name")
}

func TestEncodeAlwaysEmitsAnAlphaItem(t *testing.T) {
	// A consequence of avifEncoderAddImage's flags being 0: the SINGLE flag
	// would drop a fully opaque alpha plane, and prod emits an alpha item on
	// every rendition including ones whose master had none.
	opaque, err := Encode(gradient(48, 48, 255), 48, 48, params(13))
	require.NoError(t, err)
	require.Greater(t, bytes.Count(opaque, []byte("av01")), 1,
		"a second av01 item is the alpha aux item, present even when opaque")
}

func TestEncodeQuantizerChangesSize(t *testing.T) {
	px := gradient(96, 96, 255)
	big, err := Encode(px, 96, 96, params(13)) // q_90
	require.NoError(t, err)
	small, err := Encode(px, 96, 96, params(32)) // q_50 quantises harder
	require.NoError(t, err)
	require.Greater(t, len(big), len(small))
}

func TestEncodeIsDeterministic(t *testing.T) {
	// Same pixels and settings must give the same bytes: thread COUNT is
	// irrelevant to the bitstream (only >1 vs 1 matters, for row-MT), so two
	// runs at different counts above 1 have to agree.
	px := gradient(64, 64, 200)
	a, err := Encode(px, 64, 64, Params{MinQ: 13, MaxQ: 21, MinAlphaQ: 27, MaxAlphaQ: 35, Speed: 9, Threads: 2})
	require.NoError(t, err)
	b, err := Encode(px, 64, 64, Params{MinQ: 13, MaxQ: 21, MinAlphaQ: 27, MaxAlphaQ: 35, Speed: 9, Threads: 8})
	require.NoError(t, err)
	require.Equal(t, a, b, "thread count must not change the bitstream")
}

func TestEncodeRejectsBadInput(t *testing.T) {
	_, err := Encode(gradient(8, 8, 255), 0, 8, params(13))
	require.Error(t, err)

	_, err = Encode(make([]byte, 10), 8, 8, params(13))
	require.ErrorContains(t, err, "want 256")
}
