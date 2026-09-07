package wix

import "math"

// The crop forms that the main geometry rules do not cover.
//
// The corpus only ever shows `crop` chained into `fill`, which makes crop look
// like a prefix to a scale. It is not: it runs standalone, before or after a
// scale, and chains with itself.

// fusedCrop collapses a <scale> -> crop chain into ONE crop+resize on the
// master. Wix does not materialise the scaled image and crop it -- that was
// measured and comes out only 99.494% identical.
//
//	x_m = round(x/s)              y_m = round(y/s)
//	m   = floor((ext-1)/s) + 1    for s > 1
//	    = floor(ext/s)            for s <= 1
//	out = min(ext, round(m*s))
//	any excess is trimmed CENTRED
//
// prev.NX/NY is the preceding op's own crop origin: `fill` crops before
// scaling, so the fused rectangle starts from there. `fit` never crops, so its
// origin is whatever a crop before IT established.
func fusedCrop(prev Plan, x, y, w, h int) Plan {
	s := prev.S

	// The extent in source pixels that renders to `e` output pixels.
	ext := func(e int) int {
		if s > 1 {
			return floorF(float64(e-1)/s) + 1
		}
		return floorF(float64(e) / s)
	}

	mw, mh := ext(w), ext(h)
	if mw < 1 {
		mw = 1
	}
	if mh < 1 {
		mh = 1
	}

	rw := roundHalfUp(float64(mw) * s)
	rh := roundHalfUp(float64(mh) * s)
	ow, oh := min(w, rw), min(h, rh)

	return Plan{
		NX: prev.NX + roundHalfUp(float64(x)/s),
		NY: prev.NY + roundHalfUp(float64(y)/s),
		HW: mw, HH: mh,
		S: s,
		// Excess comes off the CENTRE. Below about s = 2.4 the excess is 0 or 1
		// and 1//2 == 0, which makes a top-left trim look correct -- so this
		// only shows on heavy enlargements.
		TX: floorDiv(rw-ow, 2),
		TY: floorDiv(rh-oh, 2),
		W:  ow, H: oh,
		Identity: ow == mw && oh == mh && s == 1.0,
	}
}

// oobCrop handles a crop rectangle that leaves the source. It is CLAMPED to the
// available pixels and cover-scaled back up -- not padded, not shifted, not
// stretched (WIX-URL-SPEC §2):
//
//	s      = max(w/cw, h/ch)
//	source = (cx, cy, cw, min(ceil(h/s), ch))
//	resize by s, then CENTRED trim to (w, h)
//
// The asymmetry is real and deliberate: the full clamp WIDTH is kept and
// cropped in output space, because its centre can land on a fractional source
// column, while only the ROWS the output needs are read.
//
// nx/ny carry a preceding in-bounds crop's origin. Negative origins are clamped
// to zero, which the reference never exercises -- treat that case as unverified.
func oobCrop(nx, ny, sw, sh, x, y, w, h int) (Plan, error) {
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}

	cx, cy := min(x, sw-1), min(y, sh-1)
	cw, ch := min(w, sw-cx), min(h, sh-cy)
	if cw < 1 || ch < 1 {
		return Plan{}, ErrBadParams
	}

	s := math.Max(float64(w)/float64(cw), float64(h)/float64(ch))
	mh := min(ceilF(float64(h)/s), ch)

	tx := floorDiv(roundHalfUp(float64(cw)*s)-w, 2)
	ty := floorDiv(roundHalfUp(float64(mh)*s)-h, 2)

	return Plan{
		NX: nx + cx, NY: ny + cy,
		HW: cw, HH: mh,
		S:  s,
		TX: max(0, tx), TY: max(0, ty),
		W: w, H: h,
		Identity: cw == w && mh == h && s == 1.0,
	}, nil
}
