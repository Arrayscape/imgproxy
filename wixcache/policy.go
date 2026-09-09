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
// framing check below is the missing predicate.
//
// Only ancestors covering the whole master are accepted. The CDN's own pyramid
// levels are whole-master and aspect-preserved, and deriving is a plain resize
// with no crop (§10.1), so a cropped ancestor has nothing it could correctly
// produce. masterW/masterH are the master's dimensions.
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

	// The ancestor must frame EXACTLY the region the new request reads -- not
	// merely contain it.
	//
	// Deriving is a plain resize with no crop (OP-SPEC §10.1), so a target that
	// reads a sub-region of the ancestor cannot be produced from it at all: the
	// resize would squash the whole ancestor into the target's box instead of
	// cropping to the region and scaling that. Containment was the right
	// predicate while derivation re-ran the pipeline; under a plain resize it
	// silently produces a differently-framed image.
	//
	// In practice this means only whole-master targets derive -- `fit`, which
	// uses the entire source. A `fill` that crops, or a `crop` op, falls back
	// to the master. That is the conservative half of the trade: the CDN's own
	// pyramid levels are whole-master and aspect-preserved (925x21 from a
	// 1032x24 master), so a cropped target has no level it could have come
	// from either.
	want := image.Rect(t.NX, t.NY, t.NX+t.HW, t.NY+t.HH)
	if want != e.SrcRect {
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

// DeriveDims returns the dimensions a derived rendition must be resized to.
//
// They come from the plan resolved against the MASTER, not from re-resolving
// the url against the ancestor. A url's output size is a property of the url
// and the master alone: the same request must produce the same dimensions
// whether it was served from the master or from a cached rendition, and only
// the pixels may differ. Re-resolving against the ancestor would change the
// size too, which no CDN behaviour supports.
//
// This replaces an earlier Replan that did exactly that -- resolved the
// segments against the ancestor's dimensions "because the pipeline is
// identical, only the input differs". OP-SPEC §10.1 measured the truth: the
// derivation path is not the pipeline at all, it is a plain resize to the
// target's own dimensions.
func DeriveDims(t wix.Plan) (w, h int) { return t.W, t.H }
