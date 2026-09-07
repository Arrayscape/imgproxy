package wix

import (
	"fmt"
	"math"
)

// Plan is the fully resolved geometry for one request against one source.
//
// All coordinates are in the SOURCE's own space -- the master, or a cached
// ancestor when deriving (OP-SPEC.md §10). A Plan completely determines the
// pipeline, so the executor in processing/wix is a pure interpreter of it and
// every geometry rule can be tested without libvips.
type Plan struct {
	// NX, NY, HW, HH are the source rectangle handed to extract_area. The
	// reference emits this extract unconditionally, even for `fit` where it is
	// the whole image.
	NX, NY int
	HW, HH int

	// S is the resample scale at FULL double precision. It is never formatted
	// to a string anywhere on this path: %.14f does not round-trip a double,
	// and the resulting phase shift costs ±1 across the frame (OP-SPEC §4.6).
	S float64

	// DW, DH are the pixels dropped from the source rectangle BEFORE the
	// resize, to reconcile libvips' ROUND_UINT sizing with the CDN's floor.
	// Downscale only. See Drop.
	DW, DH int

	// TX, TY are the origin of the trim applied AFTER the resize. Zero on the
	// ordinary downscale path; non-zero when the rendered size overshoots the
	// target and the excess comes off the centre (enlargement, fused crop,
	// out-of-bounds crop).
	TX, TY int

	// W, H are the output dimensions.
	W, H int

	// Identity means source rectangle and output size already agree, so there
	// is no resample at all. The premultiply/unpremultiply round trip is lossy
	// wherever alpha is partial, so this branch skips it entirely -- it is NOT
	// the enlargement path at scale 1. See OP-SPEC §5.1.
	Identity bool
}

// Resolve walks a segment chain against a source of sw x sh and produces the
// Plan that renders it.
//
// The measured forms, in the order they are handled:
//
//	crop* -> fit|fill    the corpus: a crop replaces the source rectangle and
//	                     the scale is computed against it (OP-SPEC §4.4)
//	crop                 standalone, exactly extract_area (70/70)
//	crop -> crop         one extract_area with the offsets added
//	fit|fill -> crop     FUSED into one crop+resize on the master
//	crop out of bounds   clamped and cover-scaled back up
func Resolve(segs []Segment, sw, sh int) (Plan, error) {
	if sw <= 0 || sh <= 0 {
		return Plan{}, fmt.Errorf("wix: source has no area (%dx%d)", sw, sh)
	}

	nx, ny := 0, 0
	var plan *Plan
	i := 0

	// Phase 1 -- leading crops, accumulated into one source rectangle. A crop
	// replaces the source rect (§4.3) and offsets add, so crop -> crop is a
	// single extract_area.
	for ; i < len(segs) && segs[i].Op == OpCrop; i++ {
		x, y, w, h, err := cropRect(segs[i])
		if err != nil {
			return Plan{}, err
		}
		if x < 0 || y < 0 || x+w > sw || y+h > sh {
			// Clipped to the available pixels rather than erroring.
			p, err := oobCrop(nx, ny, sw, sh, x, y, w, h)
			if err != nil {
				return Plan{}, err
			}
			plan = &p
			i++
			break
		}
		nx, ny = nx+x, ny+y
		sw, sh = w, h
	}

	// Phase 2 -- the first scale, computed against that rectangle. This is the
	// shape the corpus contains and the one that is byte-exact.
	if plan == nil {
		if i < len(segs) && (segs[i].Op == OpFit || segs[i].Op == OpFill) {
			p, err := scalePlan(segs[i], nx, ny, sw, sh)
			if err != nil {
				return Plan{}, err
			}
			plan = &p
			i++
		} else {
			// Crops only: a standalone crop is exactly extract_area (70/70).
			p := finish(Plan{NX: nx, NY: ny, HW: sw, HH: sh, S: 1.0, W: sw, H: sh})
			plan = &p
		}
	}

	// Phase 3 -- fold whatever remains, left to right. Nothing is ever dropped:
	// an unfoldable segment is an error, not a silently different image.
	for ; i < len(segs); i++ {
		p, err := fold(*plan, segs[i])
		if err != nil {
			return Plan{}, err
		}
		plan = &p
	}

	return *plan, nil
}

// cropRect reads and validates a crop segment's rectangle.
func cropRect(seg Segment) (x, y, w, h int, err error) {
	x, ok1 := seg.Params.Int("x")
	y, ok2 := seg.Params.Int("y")
	w, ok3 := seg.Params.Int("w")
	h, ok4 := seg.Params.Int("h")
	if !(ok1 && ok2 && ok3 && ok4) || w <= 0 || h <= 0 {
		return 0, 0, 0, 0, ErrBadParams
	}
	return x, y, w, h, nil
}

// fold applies one segment to an already-resolved plan.
//
// UNVERIFIED beyond a single trailing crop. The corpus contains only
// crop* -> (fit|fill), and verify-vips-crop adds crop -> crop and one
// scale -> crop fuse. Longer chains and scale -> scale compose the way the
// grammar implies, but nothing measured confirms the arithmetic. See WIXEMU.md.
func fold(prev Plan, seg Segment) (Plan, error) {
	switch seg.Op {
	case OpCrop:
		x, y, w, h, err := cropRect(seg)
		if err != nil {
			return Plan{}, err
		}
		return fusedCrop(prev, x, y, w, h), nil

	case OpFit, OpFill:
		return foldScale(prev, seg)
	}
	return Plan{}, fmt.Errorf("wix: unknown op %q", seg.Op)
}

// foldScale applies a fit/fill to the image a previous segment produced.
//
// The new op is computed against the CURRENT virtual size -- which is what
// "segments apply left to right" means -- and its crop window is then mapped
// back into master coordinates, so the whole chain still renders as one
// extract_area plus one resize. Materialising the intermediate and rescaling it
// would resample twice and is measurably not what the CDN does.
func foldScale(prev Plan, seg Segment) (Plan, error) {
	// Geometry of the new op against the virtual image prev produced.
	v, err := scalePlan(seg, 0, 0, prev.W, prev.H)
	if err != nil {
		return Plan{}, err
	}

	// Back-map the virtual crop window into master coordinates.
	mx := prev.NX + roundHalfUp(float64(prev.TX+v.NX)/prev.S)
	my := prev.NY + roundHalfUp(float64(prev.TY+v.NY)/prev.S)
	mw := max(1, roundHalfUp(float64(v.HW)/prev.S))
	mh := max(1, roundHalfUp(float64(v.HH)/prev.S))

	// Never read outside the rectangle the chain had already established.
	mx = clamp(mx, prev.NX, prev.NX+prev.HW-1)
	my = clamp(my, prev.NY, prev.NY+prev.HH-1)
	mw = min(mw, prev.NX+prev.HW-mx)
	mh = min(mh, prev.NY+prev.HH-my)
	if mw < 1 || mh < 1 {
		return Plan{}, ErrBadParams
	}

	// Derive the scale from the rectangle actually being read, so the output
	// lands exactly on the size the op asked for.
	return finish(Plan{
		NX: mx, NY: my, HW: mw, HH: mh,
		S: float64(v.W) / float64(mw),
		W: v.W, H: v.H,
	}), nil
}

// scalePlan implements `fit` and `fill` for one segment against a source
// rectangle already positioned at (nx, ny).
func scalePlan(seg Segment, nx, ny, sw, sh int) (Plan, error) {
	W, ok1 := seg.Params.Int("w")
	H, ok2 := seg.Params.Int("h")
	if !ok1 || !ok2 || W <= 0 || H <= 0 {
		return Plan{}, ErrBadParams
	}
	fw, fh := float64(sw), float64(sh)

	switch seg.Op {
	case OpFit:
		// Never enlarges. Output dimensions are FLOORED, not rounded: 877 of
		// the corpus's 1798 fit renditions distinguish the two.
		s := math.Min(math.Min(float64(W)/fw, float64(H)/fh), 1.0)
		// fit does no centring of its own, so a preceding crop's origin carries
		// through unchanged and the whole (possibly cropped) source is used.
		return finish(Plan{
			S: s, NX: nx, NY: ny, HW: sw, HH: sh,
			W: max(1, floorF(fw*s)),
			H: max(1, floorF(fh*s)),
		}), nil

	case OpFill:
		// Covers the box and crops the overflow. May enlarge.
		s := math.Max(float64(W)/fw, float64(H)/fh)
		// On the axis that drives s, W/s is exactly sw -- but in floating point
		// it lands a hair above (919.0000000000001) and ceil then asks for one
		// pixel more than the master has, which makes extract_area refuse and
		// the rendition produce no output at all. A crop can never exceed its
		// source, so clamp.
		hw := min(ceilF(float64(W)/s), sw)
		hh := min(ceilF(float64(H)/s), sh)
		cx, cy := centring(seg.Params, sw, sh, hw, hh)
		return finish(Plan{
			S: s, NX: nx + cx, NY: ny + cy, HW: hw, HH: hh, W: W, H: H,
		}), nil
	}
	return Plan{}, fmt.Errorf("wix: %q is not a scaling op", seg.Op)
}

// finish fills in the branch-dependent fields: identity, the enlargement centre
// trim, and the downscale drop. Everything above it sets only the source
// rectangle, the scale and the output size.
func finish(p Plan) Plan {
	switch {
	case p.W == p.HW && p.H == p.HH:
		// Tested BEFORE the enlargement case on purpose: a fill whose crop
		// already equals its output takes this branch even when its nominal
		// scale exceeds 1.
		p.Identity = true

	case p.S > 1.0:
		// Enlargement gets no grid correction, so where the rendered size
		// rounds up past the target the excess comes off the CENTRE.
		p.TX = floorF((float64(floorF(float64(p.HW)*p.S+0.5)) - float64(p.W)) / 2)
		p.TY = floorF((float64(floorF(float64(p.HH)*p.S+0.5)) - float64(p.H)) / 2)

	default:
		p.DW = Drop(p.S, p.HW, p.W)
		p.DH = Drop(p.S, p.HH, p.H)
	}
	return p
}

// Drop is how many pixels come off an axis before the resize.
//
// libvips sizes a reduce as ROUND_UINT(in/shrink); the CDN floors. Where the
// two disagree the shortfall is made up by handing the resampler fewer pixels,
// and the count is how far apart they are:
//
//	E = out/s - in
//	d = floor(-E) for E <= -1, else 0
//
// so E in (-2,-1] drops one and (-3,-2] drops two.
//
// Applied to the ORIGINAL crop, never to a hand-shrunk intermediate. Two things
// that look equivalent and are not, both measured:
//
//   - Solving ROUND_UINT((in-d)/shrink) == out for the smallest d returns 0 for
//     cases that genuinely need a drop, and cost 150 renditions.
//   - Measuring against `in // n` for the integer shrink vips_resize does
//     internally rounds the count down to 1 on heavy downscales and leaves them
//     38-81% wrong.
func Drop(s float64, in, out int) int {
	E := float64(out)/s - float64(in)
	if E <= -1 {
		return floorF(-E)
	}
	return 0
}

// centring places the fill crop window on its axes.
//
// The rounding depends on whether `al` is PRESENT, independently of its value:
//
//	al absent       (sw - hw) // 2      combined halves
//	al present      sw//2 - hw//2       separate halves
//
// The two differ by one exactly when the slack is odd. This is not an anchor
// choice -- it applies even to al_c, which is otherwise a no-op -- and getting
// it wrong costs roughly 6% of fill renditions and nothing else.
func centring(p Params, sw, sh, hw, hh int) (int, int) {
	switch {
	case p.Has("fp"):
		return focalOffsets(p, sw, sh, hw, hh)
	case p.Has("al"):
		return anchorOffsets(p["al"], sw, sh, hw, hh)
	default:
		return floorDiv(sw-hw, 2), floorDiv(sh-hh, 2)
	}
}

// anchorOffsets applies an `al` anchor. The two axes are independent, so only
// the axis with slack can matter: on a horizontally-cropped image al_c, al_t
// and al_b are identical.
//
// UNVERIFIED for anything but "c". Production emits only al_c, and the
// non-centre anchors were observed but never scored (WIX-URL-SPEC §8).
func anchorOffsets(v string, sw, sh, hw, hh int) (int, int) {
	cx := floorDiv(sw, 2) - floorDiv(hw, 2)
	cy := floorDiv(sh, 2) - floorDiv(hh, 2)
	for _, r := range v {
		switch r {
		case 'l':
			cx = 0
		case 'r':
			cx = sw - hw
		case 't':
			cy = 0
		case 'b':
			cy = sh - hh
		case 'c': // keeps the separate-halves centre
		}
	}
	return cx, cy
}

// focalOffsets positions the crop window on a focal point in [0,1] and clamps
// it to the image, which is why it looks like a three-way switch when the slack
// is small.
//
// UNVERIFIED except at 0.50_0.50. Production emits only fp_0.50_0.50, which is
// measured to equal al_c -- and the §11 formula does NOT reproduce al_c on odd
// source dimensions, since roundHalfUp(0.5*sw) - hw/2 and sw/2 - hw/2 differ by
// one when sw is odd. The measured case therefore wins outright and the formula
// covers the rest. Whether fp's PRESENCE also selects the separate-halves rule
// the way al's does is untested.
func focalOffsets(p Params, sw, sh, hw, hh int) (int, int) {
	fx, fy, ok := p.Float2("fp")
	if !ok {
		return floorDiv(sw-hw, 2), floorDiv(sh-hh, 2)
	}
	if fx == 0.5 && fy == 0.5 {
		return anchorOffsets("c", sw, sh, hw, hh)
	}
	return clamp(roundHalfUp(fx*float64(sw))-floorDiv(hw, 2), 0, sw-hw),
		clamp(roundHalfUp(fy*float64(sh))-floorDiv(hh, 2), 0, sh-hh)
}
