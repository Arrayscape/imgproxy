package wixcache

import (
	"image"
	"sort"

	"github.com/imgproxy/imgproxy/v4/imagetype"
	"github.com/imgproxy/imgproxy/v4/wix"
)

// DefaultMaxDepth derives only from a master render, never from a derived one.
// Each generation resamples an already-resampled image, so depth costs quality.
const DefaultMaxDepth = 1

// Usable reports whether e can serve as the input for plan t.
//
// OP-SPEC §10 suggests "smallest cached rendition at least as large as the
// target", but that alone is not sound: two `fill`s of different aspect ratios
// can both be "larger" while covering DISJOINT parts of the master, and
// deriving one from the other would silently return the wrong region. The
// containment check below is the missing predicate.
//
// Because Replan treats the ancestor AS the source, a cropped ancestor would
// re-frame every subsequent transform against the crop rather than the master.
// Whether the CDN does that is unmeasured, so only ancestors covering the whole
// master are accepted -- the case where "the ancestor is the source" is
// unambiguous. masterW/masterH are the master's dimensions.
func Usable(e *Entry, t wix.Plan, maxDepth int, masterW, masterH int) bool {
	if e.SrcRect != image.Rect(0, 0, masterW, masterH) {
		return false
	}

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
func SelectAncestor(entries []*Entry, t wix.Plan, maxDepth, masterW, masterH int) *Entry {
	usable := make([]*Entry, 0, len(entries))
	for _, e := range entries {
		if Usable(e, t, maxDepth, masterW, masterH) {
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

// Replan re-runs the URL against a cached ancestor.
//
// OP-SPEC §10 and WIX-URL-SPEC §6 both say the same thing: "the pipeline is
// identical; only the input differs", and "run through the same pipeline". So
// the ancestor IS the source -- the segments are resolved against its
// dimensions exactly as if it were the master.
//
// This is NOT the same as mapping the master-relative plan into ancestor
// coordinates by ratio. That was the first implementation here, and it differed
// from this on 38% of sampled cases -- different scale, different drop, a crop
// a pixel wider -- all of which change pixels.
func Replan(segs []wix.Segment, e *Entry) (wix.Plan, bool) {
	p, err := wix.Resolve(segs, e.Width, e.Height)
	if err != nil {
		return wix.Plan{}, false
	}
	// Deriving must never enlarge: falling back to the master is always
	// correct, and the master still has the detail.
	if p.S > 1.0 {
		return wix.Plan{}, false
	}
	return p, true
}
