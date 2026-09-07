package wix

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// plan is a helper: parse a transform path against a source size.
func plan(t *testing.T, path string, sw, sh int) Plan {
	t.Helper()
	r, err := ParsePath("/mid~mv2.png/v1/" + path + "/n.jpg")
	require.NoError(t, err)
	p, err := Resolve(r.Segments, sw, sh)
	require.NoError(t, err)
	return p
}

func TestFitFloorsRatherThanRounds(t *testing.T) {
	// 877 of the corpus's 1798 fit renditions distinguish floor from round.
	// 100x100 into a 51x51 box: s = 0.51, out = floor(51.0) = 51.
	p := plan(t, "fit/w_51,h_51", 100, 100)
	require.Equal(t, 51, p.W)

	// 1725x1294 fit into 1120x840: s = min(1120/1725, 840/1294) = 0.6493...
	// floor gives 1119 on the width where round would give 1120.
	p = plan(t, "fit/w_1120,h_840", 1725, 1294)
	require.Equal(t, 1119, p.W, "fit must FLOOR the output dimension")
	require.Equal(t, 840, p.H)

	// fit never enlarges.
	p = plan(t, "fit/w_500,h_500", 100, 100)
	require.Equal(t, 1.0, p.S)
	require.Equal(t, 100, p.W)
}

func TestFillCentringDependsOnAlPresence(t *testing.T) {
	// Measured on one master, one geometry, two URLs fetched minutes apart:
	// on 512x392, these three all give the combined-halves form without `al`
	// and the separate-halves form with al_c, 3/3.
	for _, g := range []struct{ w, h int }{{123, 66}, {451, 339}, {166, 113}} {
		const sw, sh = 512, 392

		noAl := plan(t, fmt.Sprintf("fill/w_%d,h_%d", g.w, g.h), sw, sh)
		withAl := plan(t, fmt.Sprintf("fill/w_%d,h_%d,al_c", g.w, g.h), sw, sh)

		require.Equal(t, floorDiv(sh-noAl.HH, 2), noAl.NY, "no al: combined halves")
		require.Equal(t, floorDiv(sh, 2)-floorDiv(withAl.HH, 2), withAl.NY, "al_c: separate halves")

		// The forms differ by one exactly when the slack is odd.
		if (sh-noAl.HH)%2 != 0 {
			require.NotEqual(t, noAl.NY, withAl.NY,
				"odd slack must distinguish the two centring forms (h=%d)", g.h)
		}
	}
}

func TestFillClampsTheCropToTheSource(t *testing.T) {
	// On the driving axis W/s is exactly sw, but in floating point it lands a
	// hair above and ceil then asks for one pixel more than the master has,
	// which makes extract_area refuse outright.
	p := plan(t, "fill/w_919,h_400", 919, 500)
	require.LessOrEqual(t, p.HW, 919)
	require.LessOrEqual(t, p.HH, 500)
}

func TestDrop(t *testing.T) {
	// E = out/s - in;  d = floor(-E) for E <= -1, else 0.
	require.Equal(t, 0, Drop(0.5, 100, 50), "exact: no drop")

	// E in (-2,-1] drops one, (-3,-2] drops two.
	require.Equal(t, 1, Drop(1.0, 101, 100))
	require.Equal(t, 2, Drop(1.0, 102, 100))
	require.Equal(t, 0, Drop(1.0, 100, 100))

	// Only downscale plans carry a drop.
	p := plan(t, "fit/w_50,h_50", 100, 100)
	require.Equal(t, 0, p.DW)
}

func TestIdentityBeatsEnlargement(t *testing.T) {
	// A fill whose crop already equals its output takes the identity branch
	// even when its nominal scale exceeds 1 -- the premultiply round trip is
	// lossy on partial alpha, so it must be skipped.
	p := plan(t, "fill/w_100,h_100", 100, 100)
	require.True(t, p.Identity)
	require.Equal(t, 0, p.DW)
	require.Equal(t, 0, p.TX)
}

func TestEnlargementTrimsFromTheCentre(t *testing.T) {
	p := plan(t, "fill/w_301,h_301", 100, 100)
	require.False(t, p.Identity)
	require.Greater(t, p.S, 1.0)
	require.Equal(t, 301, p.W)
	// tx = floor((floor(hw*s + 0.5) - W) / 2)
	require.GreaterOrEqual(t, p.TX, 0)
}

func TestCropStandaloneIsAPlainExtract(t *testing.T) {
	p := plan(t, "crop/x_10,y_20,w_30,h_40", 100, 100)
	require.True(t, p.Identity, "an in-bounds standalone crop is exactly extract_area")
	require.Equal(t, 10, p.NX)
	require.Equal(t, 20, p.NY)
	require.Equal(t, 30, p.W)
	require.Equal(t, 40, p.H)
}

func TestCropChainAddsOffsets(t *testing.T) {
	// crop -> crop equals ONE extract_area with the offsets added.
	p := plan(t, "crop/x_10,y_10,w_50,h_50/crop/x_5,y_5,w_20,h_20", 100, 100)
	require.Equal(t, 15, p.NX)
	require.Equal(t, 15, p.NY)
	require.Equal(t, 20, p.W)
}

func TestCropThenFillUsesTheCroppedSource(t *testing.T) {
	// The later segment's centring is computed against the CROPPED dimensions,
	// with the crop origin added.
	p := plan(t, "crop/x_37,y_53,w_300,h_280/fill/w_150,h_140", 1000, 1000)
	require.GreaterOrEqual(t, p.NX, 37)
	require.GreaterOrEqual(t, p.NY, 53)
	require.Equal(t, 150, p.W)
	require.Equal(t, 140, p.H)
}

func TestOutOfBoundsCropIsClamped(t *testing.T) {
	// A rectangle leaving the source is clipped to the available pixels rather
	// than erroring, then cover-scaled back up.
	p := plan(t, "crop/x_80,y_80,w_50,h_50", 100, 100)
	require.Equal(t, 50, p.W)
	require.Equal(t, 50, p.H)
	require.LessOrEqual(t, p.NX+p.HW, 100, "source rect must stay inside the master")
	require.LessOrEqual(t, p.NY+p.HH, 100)
}

func TestScaleIsNeverStringified(t *testing.T) {
	// %.14f does not round-trip a double: 0.44755244755244755 prints as
	// 0.44755244755245, whose reciprocal is 2.234374999999988 rather than
	// 2.234375, shifting the sampling phase by +-1 across the frame.
	p := plan(t, "fit/w_512,h_512", 1144, 1144)
	require.Equal(t, 512.0/1144.0, p.S, "scale must be the exact quotient")
}
