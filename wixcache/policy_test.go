package wixcache

import (
	"image"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/imgproxy/imgproxy/v4/imagetype"
	"github.com/imgproxy/imgproxy/v4/wix"
)

func entry(rect image.Rectangle, w, h, depth int) *Entry {
	return &Entry{
		Data: []byte("x"), Format: imagetype.PNG,
		SrcRect: rect, Width: w, Height: h, Depth: depth,
	}
}

// target reads the master rectangle (nx,ny)-(nx+hw,ny+hh) down to w x h.
func target(nx, ny, hw, hh, w, h int) wix.Plan {
	return wix.Plan{NX: nx, NY: ny, HW: hw, HH: hh, W: w, H: h,
		S: float64(w) / float64(hw)}
}

func TestUsableRequiresContainment(t *testing.T) {
	// OP-SPEC §10's "smallest rendition at least as large as the target" is not
	// sufficient on its own: two fills of different aspect ratios can both be
	// larger while covering DISJOINT parts of the master, and deriving one from
	// the other would silently return the wrong region.
	big := entry(image.Rect(0, 0, 1000, 1000), 800, 800, 0)

	inside := target(100, 100, 200, 200, 100, 100)
	require.True(t, Usable(big, inside, DefaultMaxDepth, 1000, 1000))

	// Same output size, but reads a region the ancestor never covered.
	outside := target(900, 900, 200, 200, 100, 100)
	require.False(t, Usable(big, outside, DefaultMaxDepth, 1000, 1000),
		"an ancestor that does not contain the target region must be rejected")

	// Partially overlapping is still not containment.
	partial := target(900, 0, 200, 200, 100, 100)
	require.False(t, Usable(big, partial, DefaultMaxDepth, 1000, 1000))
}

func TestUsableRejectsUnsuitableAncestors(t *testing.T) {
	rect := image.Rect(0, 0, 1000, 1000)
	tgt := target(0, 0, 1000, 1000, 100, 100)

	require.True(t, Usable(entry(rect, 500, 500, 0), tgt, DefaultMaxDepth, 1000, 1000))

	// Effects are baked into the pixels.
	sharp := entry(rect, 500, 500, 0)
	sharp.Effects = wix.Effects{USM: &wix.USM{Sigma: 0.66, Amount: 1, Threshold: 0.01}}
	require.False(t, Usable(sharp, tgt, DefaultMaxDepth, 1000, 1000), "a sharpened ancestor is not resamplable")

	blurred := entry(rect, 500, 500, 0)
	blurred.Effects = wix.Effects{Blur: 3}
	require.False(t, Usable(blurred, tgt, DefaultMaxDepth, 1000, 1000))

	// Deriving from a lossy re-encode compounds artefacts.
	lossy := entry(rect, 500, 500, 0)
	lossy.Format = imagetype.WEBP
	require.False(t, Usable(lossy, tgt, DefaultMaxDepth, 1000, 1000))

	// Never upsample from a rendition; the master still has the detail.
	tooSmall := entry(rect, 50, 50, 0)
	require.False(t, Usable(tooSmall, tgt, DefaultMaxDepth, 1000, 1000))

	// Depth: quality degrades with every generation.
	require.False(t, Usable(entry(rect, 500, 500, 1), tgt, 1, 1000, 1000),
		"default depth 1 derives only from a master render")
	require.True(t, Usable(entry(rect, 500, 500, 1), tgt, 2, 1000, 1000))
}

func TestSelectAncestorPicksSmallestSufficient(t *testing.T) {
	rect := image.Rect(0, 0, 1000, 1000)
	tgt := target(0, 0, 1000, 1000, 100, 100)

	got := SelectAncestor([]*Entry{
		entry(rect, 900, 900, 0),
		entry(rect, 200, 200, 0), // smallest that still has enough detail
		entry(rect, 600, 600, 0),
		entry(rect, 50, 50, 0), // too small, rejected
	}, tgt, DefaultMaxDepth, 1000, 1000)

	require.NotNil(t, got)
	require.Equal(t, 200, got.Width, "least work that still has the detail")
}

func TestSelectAncestorIsOrderIndependent(t *testing.T) {
	// The choice must be a pure function of the request and the cache contents
	// (OP-SPEC §10), not of insertion order or map iteration.
	rect := image.Rect(0, 0, 1000, 1000)
	tgt := target(0, 0, 1000, 1000, 100, 100)

	a := entry(image.Rect(0, 0, 1000, 1000), 400, 400, 0)
	b := entry(image.Rect(0, 0, 1000, 1000), 400, 400, 0)
	b.SrcRect = rect

	first := SelectAncestor([]*Entry{a, b}, tgt, DefaultMaxDepth, 1000, 1000)
	second := SelectAncestor([]*Entry{b, a}, tgt, DefaultMaxDepth, 1000, 1000)
	require.Equal(t, first.Width, second.Width)
	require.Equal(t, first.SrcRect, second.SrcRect)
}

func TestReplanTreatsTheAncestorAsTheSource(t *testing.T) {
	// "The pipeline is identical; only the input differs" -- so the segments
	// resolve against the ancestor's dimensions, not the master's.
	anc := entry(image.Rect(0, 0, 1000, 1000), 400, 400, 0)
	segs := []wix.Segment{{Op: wix.OpFit, Params: wix.Params{"w": "100", "h": "100"}}}

	p, ok := Replan(segs, anc)
	require.True(t, ok)
	require.Equal(t, 400, p.HW, "the whole ancestor is the source")
	require.Equal(t, 400, p.HH)
	require.Equal(t, 100, p.W)
	require.InDelta(t, 0.25, p.S, 1e-12, "scale is relative to the ANCESTOR")
}

func TestReplanRefusesToEnlarge(t *testing.T) {
	anc := entry(image.Rect(0, 0, 1000, 1000), 100, 100, 0)
	segs := []wix.Segment{{Op: wix.OpFill, Params: wix.Params{"w": "400", "h": "400"}}}
	_, ok := Replan(segs, anc)
	require.False(t, ok, "falling back to the master is always correct")
}

func TestUsableRequiresAnUncroppedAncestor(t *testing.T) {
	// Replan re-frames against the ancestor, so a cropped ancestor would apply
	// every later transform to the crop rather than the master. Unmeasured, so
	// refused.
	tgt := target(0, 0, 1000, 1000, 100, 100)
	cropped := entry(image.Rect(100, 100, 900, 900), 500, 500, 0)
	require.False(t, Usable(cropped, tgt, DefaultMaxDepth, 1000, 1000))

	full := entry(image.Rect(0, 0, 1000, 1000), 500, 500, 0)
	require.True(t, Usable(full, tgt, DefaultMaxDepth, 1000, 1000))
}

func TestKeyIsStableAndDiscriminating(t *testing.T) {
	base := Key{
		MediaID: "m~mv2.png",
		Plan:    target(0, 0, 100, 100, 50, 50),
		Format:  imagetype.PNG,
		Quality: 85,
	}
	require.Equal(t, base.String(), base.String(), "stable across calls")

	other := base
	other.Plan.NX = 1
	require.NotEqual(t, base.String(), other.String(), "geometry must be in the key")

	other = base
	other.Codec = "vp8l"
	require.NotEqual(t, base.String(), other.String(), "codec must be in the key")

	other = base
	other.Effects = wix.Effects{Blur: 3}
	require.NotEqual(t, base.String(), other.String(), "effects must be in the key")
}

func TestMemoryEvictsByLRU(t *testing.T) {
	m := NewMemory(300)
	mk := func(n int) *Entry {
		return &Entry{Data: make([]byte, 100), Format: imagetype.PNG,
			SrcRect: image.Rect(0, 0, 10, 10), Width: n, Height: n}
	}
	m.Put("mid", "a", mk(1))
	m.Put("mid", "b", mk(2))
	m.Put("mid", "c", mk(3))
	require.Equal(t, 3, m.Len())

	_, ok := m.Get("a")
	require.True(t, ok)

	m.Put("mid", "d", mk(4)) // over budget: evicts the least recently used
	require.Equal(t, 3, m.Len())
	_, ok = m.Get("b")
	require.False(t, ok, "b was least recently used")
	_, ok = m.Get("a")
	require.True(t, ok, "a was touched and must survive")
}

func TestNopStoreNeverCaches(t *testing.T) {
	var s Store = Nop{}
	s.Put("m", "k", &Entry{Data: []byte("x")})
	_, ok := s.Get("k")
	require.False(t, ok)
	require.Empty(t, s.Ancestors("m"))
	_, _, ok = s.Dims("m")
	require.False(t, ok)
}
