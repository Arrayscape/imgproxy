package wix

import (
	"testing"

	"github.com/imgproxy/imgproxy/v4/imagetype"
	"github.com/stretchr/testify/require"
)

func TestNegotiateMatchesTheSpecTable(t *testing.T) {
	// WIX-URL-SPEC §4, verbatim.
	for _, c := range []struct {
		name     string
		enc      bool
		accept   string
		filename string
		master   imagetype.Type
		want     imagetype.Type
	}{
		{"enc + */*", true, "*/*", "z.png", imagetype.PNG, imagetype.PNG},
		{"enc + avif,webp,*/*", true, "image/avif,image/webp,*/*", "z.png", imagetype.PNG, imagetype.AVIF},
		{"enc + webp,*/*", true, "image/webp,*/*", "z.png", imagetype.PNG, imagetype.WEBP},
		{"enc + image/png", true, "image/png", "z.png", imagetype.PNG, imagetype.PNG},
		// "master's format" means the STORED format, so a JPEG master falls
		// back to JPEG rather than to any global default.
		{"enc + */* on a jpeg master", true, "*/*", "z.jpg", imagetype.JPEG, imagetype.JPEG},
		// With enc_, the filename is ignored: Accept decides.
		{"enc + jpg name, webp accepted", true, "image/webp,*/*", "z.jpg", imagetype.PNG, imagetype.WEBP},
	} {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, Negotiate(c.enc, c.accept, c.filename, c.master, true))
		})
	}
}

func TestNegotiateWithoutEncTheFilenameDecides(t *testing.T) {
	// §4.1, measured on a png master. Accept is ignored ENTIRELY, and a PNG
	// master really does serve JPEG when the url asks for .jpg and opts out.
	for _, c := range []struct {
		filename, accept string
		master, want     imagetype.Type
	}{
		{"z.png", "image/jpeg", imagetype.PNG, imagetype.PNG},
		{"z.jpg", "image/png", imagetype.PNG, imagetype.JPEG},
		{"z.jpg", "image/jpeg", imagetype.PNG, imagetype.JPEG},
		{"z.webp", "image/webp,*/*", imagetype.PNG, imagetype.WEBP},
		// An unrecognised or absent extension falls back to the master.
		{"z.bogus", "*/*", imagetype.PNG, imagetype.PNG},
		{"z", "*/*", imagetype.JPEG, imagetype.JPEG},
		{"", "*/*", imagetype.WEBP, imagetype.WEBP},
	} {
		t.Run(c.filename+" accept="+c.accept, func(t *testing.T) {
			require.Equal(t, c.want,
				Negotiate(false, c.accept, c.filename, c.master, true))
		})
	}
}

func TestNegotiateAVIFCanBeDisabled(t *testing.T) {
	// The AVIF encoder settings were never compared against the CDN, so an
	// operator can fall back to webp without losing negotiation entirely.
	got := Negotiate(true, "image/avif,image/webp,*/*", "z.png", imagetype.PNG, false)
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

func TestFormatDispositionCoversEveryIngestOutcome(t *testing.T) {
	// The scorecard's input-format table, every row measured against the live
	// upload API. An accepted-but-transcoded format is unreachable on the
	// transform path just as firmly as a rejected one.
	for _, c := range []struct {
		name string
		t    imagetype.Type
		want Disposition
	}{
		{"jpeg stored verbatim", imagetype.JPEG, DispositionTransform},
		{"png stored verbatim", imagetype.PNG, DispositionTransform},
		{"webp stored verbatim", imagetype.WEBP, DispositionTransform},
		{"avif kept verbatim, decoded", imagetype.AVIF, DispositionTransform},

		{"gif passed through", imagetype.GIF, DispositionPassThrough},

		{"tiff transcoded to png", imagetype.TIFF, DispositionUnreachable},
		{"heic transcoded to png", imagetype.HEIC, DispositionUnreachable},
		{"bmp transcoded to png", imagetype.BMP, DispositionUnreachable},
		{"jxl rejected", imagetype.JXL, DispositionUnreachable},
		{"svg routed out of /media", imagetype.SVG, DispositionUnreachable},
		{"ico not an accepted upload", imagetype.ICO, DispositionUnreachable},

		// JPEG 2000 and RAW have no imgproxy type and sniff as Unknown. Both
		// are transcoded at ingest, so unreachable is the right answer anyway
		// -- and refusing up front beats a confusing decoder error later.
		{"unrecognised", imagetype.Unknown, DispositionUnreachable},
	} {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, FormatDisposition(c.t))
			require.Equal(t, c.want != DispositionUnreachable, MasterFormatSupported(c.t))
		})
	}
}

// An extension outside the CDN's output set is not a format request and not an
// error: it falls through to the master's format (§9.3). Answering .jxl with
// JPEG XL, or .tiff with TIFF, would be a capability the CDN does not have --
// and imagetype.GetTypeByName knows both, so the allowlist is what stops it.
func TestFilenameFormatAllowlist(t *testing.T) {
	for _, name := range []string{"x.jxl", "x.tiff", "x.tif", "x.bmp", "x.gif", "x.heic", "x.svg"} {
		if got, ok := FilenameFormat(name); ok {
			t.Errorf("FilenameFormat(%q) = %v, true; want unrecognised", name, got)
		}
	}
	for name, want := range map[string]imagetype.Type{
		"x.png": imagetype.PNG, "x.jpg": imagetype.JPEG, "x.JPEG": imagetype.JPEG,
		"x.webp": imagetype.WEBP, "x.avif": imagetype.AVIF,
	} {
		got, ok := FilenameFormat(name)
		if !ok || got != want {
			t.Errorf("FilenameFormat(%q) = %v, %v; want %v, true", name, got, ok, want)
		}
	}
}

// The fallback has to reach Negotiate, not just FilenameFormat: a .jxl name on
// a png master must serve png, byte-identical to asking for .png.
func TestNegotiateUnknownExtensionFallsBackToMaster(t *testing.T) {
	for _, name := range []string{"x.jxl", "x.tiff", "x", ""} {
		if got := Negotiate(false, "", name, imagetype.PNG, true); got != imagetype.PNG {
			t.Errorf("Negotiate(filename=%q, master=PNG) = %v; want PNG", name, got)
		}
	}
	// ...and an explicitly recognised one still overrides the master.
	if got := Negotiate(false, "", "x.jpg", imagetype.PNG, true); got != imagetype.JPEG {
		t.Errorf("Negotiate(x.jpg, master=PNG) = %v; want JPEG", got)
	}
	// With AVIF disabled, .avif is unrecognised rather than an error.
	if got := Negotiate(false, "", "x.avif", imagetype.PNG, false); got != imagetype.PNG {
		t.Errorf("Negotiate(x.avif, allowAVIF=false) = %v; want PNG", got)
	}
}
