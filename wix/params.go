package wix

import (
	"strconv"
	"strings"
)

// DefaultQuality is the lossy encode quality when the URL carries no q_N.
// OP-SPEC.md §7.2.
const DefaultQuality = 85

// JPEG quality constants. OP-SPEC §7.4 -- deliberately NOT the §7.2 WebP rule.
const (
	// JPEGQualityAllEnc applies when EVERY segment carries enc_.
	JPEGQualityAllEnc = 80

	// JPEGQualityDefault applies when only SOME segments carry enc_, and is
	// also the default when no segment carries q_N.
	JPEGQualityDefault = 90
)

// LosslessWebPQuality is fixed for the VP8L path. OP-SPEC §7.3 pins Q at 75
// with a near-lossless level of 80, and the URL's q_N does NOT apply here --
// only on the lossy path. Surprising, but explicit and measured.
const (
	LosslessWebPQuality         = 75
	LosslessWebPNearLosslessLvl = 80
)

// USM is an unsharp mask: usm_<sigma>_<amount>_<threshold>.
type USM struct {
	Sigma     float64
	Amount    float64
	Threshold float64
}

// Effects are the post-resample filters. They are applied AFTER unpremultiply
// and after the final extract_area, on the uchar image -- doing either in the
// premultiplied domain is wrong. OP-SPEC §6.
type Effects struct {
	USM  *USM
	Blur float64
}

// IsZero reports whether nothing would be applied.
func (e Effects) IsZero() bool { return e.USM == nil && e.Blur <= 0 }

// EffectiveSigma is the sharpen sigma actually handed to libvips.
//
// Sigma above 10 is ignored by the CDN and falls back to a default of 0.5.
// OP-SPEC §6 attributes that to libvips' own #3270 bound, but a direct
// vips_sharpen call with sigma=15 would more likely GObject-clamp to the
// property maximum -- a different result. So the fallback is implemented here,
// where the behaviour is ours rather than a guess about libvips'.
func (u *USM) EffectiveSigma() float64 {
	if u.Sigma > 10 {
		return 0.5
	}
	return u.Sigma
}

// Encoding holds the parameters that steer the encoder rather than the pixels.
type Encoding struct {
	// Enc is true for enc_avif and enc_auto, which OPT IN to modern-format
	// negotiation. enc does NOT name the output format, and enc_<anything else>
	// is unrecognised and ignored. WIX-URL-SPEC §3.7, §4.
	Enc bool

	// Quality is q_N, the numeric quality for the lossy encode.
	Quality int

	// QualityAuto records `quality_auto`. The parameter is BOOLEAN, not
	// numeric: quality_20, quality_50, quality_80 and quality_best all produce
	// identical bytes, and only auto vs not-auto matters. Recorded for
	// completeness; it does not steer our encoder, and §7.4 measures it inert
	// for JPEG specifically.
	QualityAuto bool

	// encAll is true when EVERY segment carries enc_, encAny when at least one
	// does. JPEG's quality rule turns on the conjunction, so the two cannot be
	// collapsed into a single flag.
	encAll bool
	encAny bool

	// lastQ is the LAST segment's q_N, which is what JPEG reads. The flat
	// Quality above is the last q_N seen anywhere, which is the same thing for
	// every production URL but not by construction.
	lastQ    int
	hasLastQ bool
}

// JPEGQuality implements the §7.4 rule, which is NOT §7.2's:
//
//	enc_ on ANY segment   q_N ignored; 80 if EVERY segment carries enc_,
//	                      otherwise 90
//	no enc_ anywhere      the LAST segment's q_N, default 90
//
// The conjunction is not academic. Production's
// crop/x,y,w,h/fill/...,q_85,enc_avif,quality_auto chains carry no enc on the
// crop segment, so they encode at 90 -- not the 85 they ask for, and not the
// 80 the flat form gives.
func (e Encoding) JPEGQuality() int {
	if e.encAny {
		if e.encAll {
			return JPEGQualityAllEnc
		}
		return JPEGQualityDefault
	}
	if e.hasLastQ {
		return e.lastQ
	}
	return JPEGQualityDefault
}

// Params returns the effects and encoding parameters carried anywhere in the
// chain. In production they sit on the scaling segment, but the CDN accepts
// them on any segment, so all are scanned and the last occurrence wins.
func (r *Request) Params() (Effects, Encoding) {
	fx := Effects{}
	enc := Encoding{Quality: DefaultQuality}

	enc.encAll = len(r.Segments) > 0

	for _, seg := range r.Segments {
		p := seg.Params

		// Track enc_ per segment: JPEG's quality rule needs both "any" and
		// "every", not just the flat OR below.
		segEnc := p["enc"] == "avif" || p["enc"] == "auto"
		enc.encAny = enc.encAny || segEnc
		enc.encAll = enc.encAll && segEnc

		// The LAST segment's q_N wins for JPEG, so this is overwritten rather
		// than kept only when present.
		if q, ok := p.Int("q"); ok && q > 0 && q <= 100 {
			enc.lastQ, enc.hasLastQ = q, true
		}

		if v, ok := p["usm"]; ok {
			if u, ok := parseUSM(v); ok {
				fx.USM = u
			}
		}
		if b, ok := p.Float("blur"); ok && b > 0 {
			fx.Blur = b
		}
		if v, ok := p["enc"]; ok {
			// enc_avif and enc_auto opt in; anything else is ignored.
			enc.Enc = enc.Enc || v == "avif" || v == "auto"
		}
		if q, ok := p.Int("q"); ok && q > 0 && q <= 100 {
			enc.Quality = q
		}
		if v, ok := p["quality"]; ok {
			enc.QualityAuto = v == "auto"
		}
		// lg_N is deliberately not read. lg_0, lg_1, lg_2 and the parameter's
		// absence all return byte-identical output; production emits lg_1 on
		// some URLs. No case has been found where it changes anything, so it is
		// accepted and ignored. WIX-URL-SPEC §3.6.
	}
	return fx, enc
}

// parseUSM reads "<sigma>_<amount>_<threshold>", the value left after the
// key/value split has taken the first underscore off usm_0.66_1.00_0.01.
func parseUSM(v string) (*USM, bool) {
	parts := strings.Split(v, "_")
	if len(parts) != 3 {
		return nil, false
	}
	f := make([]float64, 3)
	for i, s := range parts {
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, false
		}
		f[i] = n
	}
	return &USM{Sigma: f[0], Amount: f[1], Threshold: f[2]}, true
}
