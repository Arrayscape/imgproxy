package wix

import (
	"testing"

	"github.com/imgproxy/imgproxy/v4/imagetype"
	"github.com/stretchr/testify/require"
)

func TestNegotiateMatchesTheSpecTable(t *testing.T) {
	// WIX-URL-SPEC §4, verbatim.
	for _, c := range []struct {
		name   string
		enc    bool
		accept string
		master imagetype.Type
		want   imagetype.Type
	}{
		{"enc + */*", true, "*/*", imagetype.PNG, imagetype.PNG},
		{"enc + avif,webp,*/*", true, "image/avif,image/webp,*/*", imagetype.PNG, imagetype.AVIF},
		{"enc + webp,*/*", true, "image/webp,*/*", imagetype.PNG, imagetype.WEBP},
		{"enc + image/png", true, "image/png", imagetype.PNG, imagetype.PNG},
		{"no enc + avif,webp", false, "image/avif,image/webp,*/*", imagetype.PNG, imagetype.PNG},
		// "master's format" means the STORED format, so a JPEG master falls
		// back to JPEG rather than to any global default.
		{"enc + */* on a jpeg master", true, "*/*", imagetype.JPEG, imagetype.JPEG},
		{"no enc on a jpeg master", false, "image/webp", imagetype.JPEG, imagetype.JPEG},
	} {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, Negotiate(c.enc, c.accept, c.master, true))
		})
	}
}

func TestNegotiateAVIFCanBeDisabled(t *testing.T) {
	// The AVIF encoder settings were never compared against the CDN, so an
	// operator can fall back to webp without losing negotiation entirely.
	got := Negotiate(true, "image/avif,image/webp,*/*", imagetype.PNG, false)
	require.Equal(t, imagetype.WEBP, got)
}

func TestEncOptInParsing(t *testing.T) {
	for _, c := range []struct {
		enc  string
		want bool
	}{{"avif", true}, {"auto", true}, {"webp", false}, {"bogus", false}, {"", false}} {
		r, err := ParsePath("/m~mv2.png/v1/fit/w_10,h_10,enc_" + c.enc + "/n.jpg")
		require.NoError(t, err)
		_, e := r.Params()
		require.Equal(t, c.want, e.Enc, "enc_%s", c.enc)
	}
}

func TestStoredLossyFollowsTheMasterNotTheExtension(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n" + "................")
	jpg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0, 0, 0, 0, 0, 0, 0}
	riff := func(codec string) []byte {
		b := []byte("RIFF____WEBP")
		return append(b, []byte(codec)...)
	}

	require.False(t, StoredLossy(png), "a PNG master is lossless -> VP8L")
	require.True(t, StoredLossy(jpg), "a JPEG master is lossy -> VP8")
	require.False(t, StoredLossy(riff("VP8L")), "VP8L master stays lossless")
	require.True(t, StoredLossy(riff("VP8 ")), "VP8 master is lossy")
	// A VP8X container counts as lossy even when its inner stream is not.
	// Imprecise, but it is what was measured.
	require.True(t, StoredLossy(riff("VP8X")))

	require.Equal(t, imagetype.PNG, StoredFormat(png))
	require.Equal(t, imagetype.WEBP, StoredFormat(riff("VP8L")))
}

func TestParamsDefaults(t *testing.T) {
	r, err := ParsePath("/m~mv2.png/v1/fit/w_10,h_10/n.jpg")
	require.NoError(t, err)
	fx, enc := r.Params()
	require.True(t, fx.IsZero())
	require.Equal(t, DefaultQuality, enc.Quality)

	r, _ = ParsePath("/m~mv2.png/v1/fit/w_10,h_10,q_60,usm_0.66_1.00_0.01,blur_3,lg_1/n.jpg")
	fx, enc = r.Params()
	require.Equal(t, 60, enc.Quality)
	require.Equal(t, 3.0, fx.Blur)
	require.NotNil(t, fx.USM)
	require.Equal(t, 0.66, fx.USM.Sigma)
	require.Equal(t, 1.0, fx.USM.Amount)
	require.Equal(t, 0.01, fx.USM.Threshold)
}

func TestUSMSigmaAbove10FallsBack(t *testing.T) {
	// Sigma above 10 is ignored by the CDN and falls back to 0.5.
	require.Equal(t, 0.5, (&USM{Sigma: 15}).EffectiveSigma())
	require.Equal(t, 9.0, (&USM{Sigma: 9}).EffectiveSigma())
}
