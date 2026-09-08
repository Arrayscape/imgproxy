package wix

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAVIFQuantizers(t *testing.T) {
	// The 28 measured points, spot-checked at both ends and the middle.
	for _, c := range []struct{ q, min int }{
		{0, 56}, {5, 53}, {50, 32}, {85, 16}, {90, 13}, {95, 10}, {100, 9},
	} {
		minQ, maxQ, exact := AVIFQuantizers(c.q)
		require.True(t, exact, "q_%d is a measured point", c.q)
		require.Equal(t, c.min, minQ)
		require.Equal(t, c.min+8, maxQ, "the span is always 8")
	}
}

func TestAVIFQuantizersApproximateUnmeasuredQ(t *testing.T) {
	// The curve has no closed form, so an unmeasured q can only be guessed at.
	// It must be flagged rather than silently presented as exact.
	minQ, _, exact := AVIFQuantizers(77)
	require.False(t, exact)
	require.Equal(t, avifQMap[75], minQ, "nearest measured point")

	_, _, exact = AVIFQuantizers(90)
	require.True(t, exact)
}

func TestAVIFQualityDefaultsTo90(t *testing.T) {
	// "A url with no q_ at all behaves as q_90" -- note this is NOT the WebP
	// default of 85, nor JPEG's rule.
	r, err := ParsePath("/m~mv2.png/v1/fill/w_10,h_10,enc_avif/n.jpg")
	require.NoError(t, err)
	_, enc := r.Params()
	require.Equal(t, 90, enc.AVIFQuality())

	r, _ = ParsePath("/m~mv2.png/v1/fill/w_10,h_10,q_50,enc_avif/n.jpg")
	_, enc = r.Params()
	require.Equal(t, 50, enc.AVIFQuality())

	// The LAST segment's q_N wins, as for JPEG.
	r, _ = ParsePath("/m~mv2.png/v1/crop/x_0,y_0,w_9,h_9,q_20/fill/w_5,h_5,q_75,enc_avif/n.jpg")
	_, enc = r.Params()
	require.Equal(t, 75, enc.AVIFQuality())
}

func TestAVIFAlphaQuantizersAreFixed(t *testing.T) {
	// Fixed at 27/35 regardless of q.
	require.Equal(t, [2]int{27, 35}, AVIFAlphaQuantizers)
}
