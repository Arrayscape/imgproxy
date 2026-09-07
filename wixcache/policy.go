package wixcache

import (
	"image"
	"math"
	"sort"

	"github.com/imgproxy/imgproxy/v4/imagetype"
	"github.com/imgproxy/imgproxy/v4/wix"
)

// DefaultMaxDepth derives only from a master render, never from a derived one.
// Each generation resamples an already-resampled image, so depth costs quality.
const DefaultMaxDepth = 1

// Usable reports whether e can serve as the input for plan t against a master
// of masterW x masterH.
//
// OP-SPEC §10 suggests "smallest cached rendition at least as large as the
// target", but that alone is not sound: two `fill`s of different aspect ratios
// can both be "larger" while covering DISJOINT parts of the master, and
// deriving one from the other would silently return the wrong region. The
// containment check below is the missing predicate.
func Usable(e *Entry, t wix.Plan, maxDepth int) bool {
	// Effects are baked into the pixels; resampling them again is not the same
	// operation as applying them to a fresh resample.
	if !e.Effects.IsZero() {
		return false
	}

	// Only derive from a lossless intermediate. Deriving from a lossy re-encode
	// compounds artefacts, and the CDN's own pHYs evidence is a PNG round trip.
	if e.Format != imagetype.PNG {
		return false
	}

	if e.Depth >= maxDepth {
		return false
	}

	// The ancestor must cover the region the new request reads.
	want := image.Rect(t.NX, t.NY, t.NX+t.HW, t.NY+t.HH)
	if !want.In(e.SrcRect) {
		return false
	}

	// Never upsample from a cached ancestor: that would be strictly worse than
	// going back to the master, which still has the detail.
	return e.Width >= t.W && e.Height >= t.H
}

// SelectAncestor picks the input for a derived render, or nil to use the master.
//
// Among usable entries it takes the smallest by area -- the least work that
// still has enough detail -- and breaks ties on the source rectangle so the
// result is a pure function of the request and the cache contents, independent
// of insertion order or map iteration.
func SelectAncestor(entries []*Entry, t wix.Plan, maxDepth int) *Entry {
	usable := make([]*Entry, 0, len(entries))
	for _, e := range entries {
		if Usable(e, t, maxDepth) {
			usable = append(usable, e)
		}
	}
	if len(usable) == 0 {
		return nil
	}

	sort.Slice(usable, func(i, j int) bool {
		a, b := usable[i], usable[j]
		if ai, bi := a.Width*a.Height, b.Width*b.Height; ai != bi {
			return ai < bi
		}
		if a.SrcRect.Min.X != b.SrcRect.Min.X {
			return a.SrcRect.Min.X < b.SrcRect.Min.X
		}
		if a.SrcRect.Min.Y != b.SrcRect.Min.Y {
			return a.SrcRect.Min.Y < b.SrcRect.Min.Y
		}
		return a.SrcRect.Dx() < b.SrcRect.Dx()
	})
	return usable[0]
}

// Rebase re-expresses a plan written against the master as one against an
// ancestor, so the SAME pipeline renders it. Only the input differs.
//
// The ancestor is the master rectangle e.SrcRect rendered at e.Width x e.Height,
// so master coordinates map into it by the ratio between the two.
func Rebase(t wix.Plan, e *Entry) (wix.Plan, bool) {
	sw, sh := e.SrcRect.Dx(), e.SrcRect.Dy()
	if sw <= 0 || sh <= 0 {
		return wix.Plan{}, false
	}
	kx := float64(e.Width) / float64(sw)
	ky := float64(e.Height) / float64(sh)

	nx := int(math.Floor(float64(t.NX-e.SrcRect.Min.X)*kx + 0.5))
	ny := int(math.Floor(float64(t.NY-e.SrcRect.Min.Y)*ky + 0.5))
	hw := int(math.Floor(float64(t.HW)*kx + 0.5))
	hh := int(math.Floor(float64(t.HH)*ky + 0.5))

	// Clamp into the ancestor. Rounding can push the rectangle a pixel over.
	if nx < 0 {
		nx = 0
	}
	if ny < 0 {
		ny = 0
	}
	if nx+hw > e.Width {
		hw = e.Width - nx
	}
	if ny+hh > e.Height {
		hh = e.Height - ny
	}
	if hw < 1 || hh < 1 {
		return wix.Plan{}, false
	}

	p := wix.Plan{
		NX: nx, NY: ny, HW: hw, HH: hh,
		W: t.W, H: t.H,
		S: float64(t.W) / float64(hw),
	}

	switch {
	case p.W == p.HW && p.H == p.HH:
		p.Identity = true
	case p.S > 1.0:
		// Deriving should never enlarge -- Usable rejects an ancestor smaller
		// than the target -- but rounding can land a hair over. Fall back to
		// the master rather than upsampling a rendition.
		return wix.Plan{}, false
	default:
		p.DW = wix.Drop(p.S, p.HW, p.W)
		p.DH = wix.Drop(p.S, p.HH, p.H)
	}
	return p, true
}
