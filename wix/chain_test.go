package wix

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Chaining. WIX-URL-SPEC §1.2 / OP-SPEC §4.4: segments apply left to right and
// compose in any order.
//
// Only crop* -> (fit|fill) is in the corpus, plus crop -> crop and one
// scale -> crop fuse from verify-vips-crop. The longer forms below are
// specified but unmeasured -- these tests pin the composition we implement so
// it cannot drift silently, not that it matches the CDN.

func TestChainNeverSilentlyDropsASegment(t *testing.T) {
	// The bug this guards: a trailing segment after a fused crop used to be
	// discarded, returning 200 with a wrong-sized image.
	p := plan(t, "fill/w_200,h_200/crop/x_10,y_10,w_50,h_50/fit/w_25,h_25", 512, 392)
	require.Equal(t, 25, p.W, "the trailing fit must be applied, not dropped")
	require.Equal(t, 25, p.H)
}

func TestChainScaleThenScaleComposes(t *testing.T) {
	// The second op is computed against the FIRST op's output, not the master.
	// fit 200x200 of 512x392 -> 200x153; fill 100x100 of that takes a 153x153
	// window at x=23, which back-maps to a 392x392 master rect at x=59.
	p := plan(t, "fit/w_200,h_200/fill/w_100,h_100", 512, 392)
	require.Equal(t, 100, p.W)
	require.Equal(t, 100, p.H)
	require.Equal(t, 59, p.NX)
	require.Equal(t, 392, p.HW)

	// A different first op must reach a different rectangle. Both of these used
	// to collapse to the same plan because the first scale was discarded.
	q := plan(t, "fill/w_400,h_400/fill/w_100,h_100", 512, 392)
	require.Equal(t, 60, q.NX)
	require.NotEqual(t, p.NX, q.NX, "the first scale must affect the result")
}

func TestChainStaysWithinTheMaster(t *testing.T) {
	// However long the chain, the final extract_area must be readable.
	for _, path := range []string{
		"fill/w_2000,h_2000/fit/w_100,h_100",
		"crop/x_400,y_300,w_200,h_200/fill/w_50,h_50/crop/x_5,y_5,w_20,h_20",
		"fit/w_50,h_50/fill/w_500,h_500/crop/x_1,y_1,w_10,h_10",
	} {
		t.Run(path, func(t *testing.T) {
			p := plan(t, path, 512, 392)
			require.GreaterOrEqual(t, p.NX, 0)
			require.GreaterOrEqual(t, p.NY, 0)
			require.LessOrEqual(t, p.NX+p.HW, 512, "source rect must fit the master")
			require.LessOrEqual(t, p.NY+p.HH, 392)
			require.Greater(t, p.W, 0)
			require.Greater(t, p.H, 0)
		})
	}
}

func TestChainCropAfterScaleIsFusedNotResampledTwice(t *testing.T) {
	// scale -> crop collapses into ONE crop+resize on the master. Rendering the
	// scale and cropping the result was measured at only 99.494% identical.
	p := plan(t, "fill/w_200,h_200/crop/x_10,y_10,w_50,h_50", 512, 392)
	require.Equal(t, 50, p.W)
	require.Equal(t, 50, p.H)
	// The crop origin back-maps through the scale rather than being applied in
	// output space: x_10 at s=0.51 lands ~20px into the master rect at x=60.
	require.Equal(t, 80, p.NX)
	require.Equal(t, 20, p.NY)
}

func TestChainMeasuredShapesAreUnchanged(t *testing.T) {
	// The corpus shape must produce exactly what it did before general
	// chaining was added: the scale is computed against the cropped rectangle,
	// with the crop origin carried through.
	p := plan(t, "crop/x_10,y_10,w_300,h_300/fill/w_100,h_100", 512, 392)
	require.Equal(t, 10, p.NX)
	require.Equal(t, 10, p.NY)
	require.Equal(t, 300, p.HW)
	require.Equal(t, 300, p.HH)
	require.Equal(t, 100, p.W)
	require.InDelta(t, 1.0/3.0, p.S, 1e-12)
}

func TestChainRejectsBadParamsRatherThanGuessing(t *testing.T) {
	for _, path := range []string{
		"crop/x_0,y_0,w_10",     // missing h
		"fill/w_100",            // missing h
		"fit/w_0,h_10",          // zero dimension
		"crop/x_0,y_0,w_0,h_10", // zero crop
	} {
		r, err := ParsePath("/m~mv2.png/v1/" + path + "/n.jpg")
		require.NoError(t, err, "%s should parse", path)
		_, err = Resolve(r.Segments, 512, 392)
		require.Error(t, err, "%s should not resolve", path)
	}
}
