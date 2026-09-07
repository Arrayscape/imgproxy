package wix

import (
	"strconv"
	"strings"
)

// DefaultQuality is the lossy encode quality when the URL carries no q_N.
// OP-SPEC.md §7.2.
const DefaultQuality = 85

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
	// completeness; it does not currently steer our encoder.
	QualityAuto bool
}

// Params returns the effects and encoding parameters carried anywhere in the
// chain. In production they sit on the scaling segment, but the CDN accepts
// them on any segment, so all are scanned and the last occurrence wins.
func (r *Request) Params() (Effects, Encoding) {
	fx := Effects{}
	enc := Encoding{Quality: DefaultQuality}

	for _, seg := range r.Segments {
		p := seg.Params

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
