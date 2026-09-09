package integration_test

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/imgproxy/imgproxy/v4/testutil/servertest"
	wixspec "github.com/imgproxy/imgproxy/v4/wix"
)

// Media ids for the generated masters. The extension is deliberately part of
// the id and deliberately not trusted: WIX-URL-SPEC §1.1.
const (
	wixRGB   = "0a7ba9_dddddddddddddddddddddddddddddddd~mv2.png"
	wixRGBA  = "0a7ba9_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb~mv2.png"
	wixJPEG  = "0a7ba9_cccccccccccccccccccccccccccccccc~mv2.jpg"
	wixLeaky = "0a7ba9_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee~mv2.png"
	wixICC   = "0a7ba9_ffffffffffffffffffffffffffffffff~mv2.png"
	wixRot   = "0a7ba9_60000000000000000000000000000000~mv2.jpg"
	wixGIF   = "0a7ba9_70000000000000000000000000000000~mv2.gif"
)

// The masters are 512x392 -- an EVEN source dimension on both axes, which is
// required to exercise the `al` centring rule at all: the combined and separate
// halves differ only when the source is even and the crop window odd.
const (
	masterW = 512
	masterH = 392
)

type WixHandlerTestSuite struct {
	servertest.Suite

	masters       string
	masterProfile []byte
}

func (s *WixHandlerTestSuite) SetupSuite() {
	s.Suite.SetupSuite()

	dir, err := os.MkdirTemp("", "wix-masters-")
	s.Require().NoError(err)
	s.masters = dir

	s.Require().NoError(os.WriteFile(filepath.Join(dir, wixRGB), gradientPNG(false), 0o644))
	s.Require().NoError(os.WriteFile(filepath.Join(dir, wixRGBA), gradientPNG(true), 0o644))
	s.Require().NoError(os.WriteFile(filepath.Join(dir, wixJPEG), gradientJPEG(), 0o644))
	s.Require().NoError(os.WriteFile(filepath.Join(dir, wixLeaky), leakyPNG(), 0o644))

	// A master carrying an ICC profile that is NOT Wix's, so a missing
	// icc_transform is detectable in the output's iCCP chunk.
	prof, err := os.ReadFile("testdata/adobergb-test.icc")
	s.Require().NoError(err)
	s.masterProfile = prof
	s.Require().NoError(os.WriteFile(filepath.Join(dir, wixICC),
		withICCProfile(gradientPNG(false), prof), 0o644))

	// A landscape master tagged EXIF Orientation 6 (rotate 90° CW), so the
	// rotated dimensions differ from the stored ones. See §4.0.
	s.Require().NoError(os.WriteFile(filepath.Join(dir, wixRot), orientedJPEG(6), 0o644))

	// A GIF master, which the CDN passes through untransformed.
	s.Require().NoError(os.WriteFile(filepath.Join(dir, wixGIF), gradientGIF(), 0o644))
}

// gradientGIF builds a small paletted GIF. It is deliberately NOT a format the
// pipeline can round-trip: the point is that nothing decodes or re-encodes it.
func gradientGIF() []byte {
	pal := make(color.Palette, 256)
	for i := range pal {
		pal[i] = color.RGBA{uint8(i), uint8(255 - i), uint8(i / 2), 255}
	}
	img := image.NewPaletted(image.Rect(0, 0, 64, 48), pal)
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			img.SetColorIndex(x, y, uint8((x*4+y)%256))
		}
	}
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func (s *WixHandlerTestSuite) TearDownSuite() {
	if s.masters != "" {
		os.RemoveAll(s.masters)
	}
	s.Suite.TearDownSuite()
}

func (s *WixHandlerTestSuite) SetupTest()    { s.configure() }
func (s *WixHandlerTestSuite) SetupSubTest() { s.ResetLazyObjects(); s.configure() }

func (s *WixHandlerTestSuite) configure() {
	c := s.Config()
	c.Fetcher.Transport.Local.Root = s.masters
	c.Handlers.Wix.Enabled = true
	c.Handlers.Wix.SourceURLTemplate = "local:///%s"

	// The shared server suite turns development errors on, which replaces the
	// public message with a stack trace. WIX-URL-SPEC §9.3 specifies the
	// public bodies exactly -- "Forbidden", and the manipulator's diagnostic
	// for a bad parameter -- so this suite has to see what production sends.
	c.Server.DevelopmentErrorsMode = false
}

// get fetches a Wix URL and returns the response body plus content type.
func (s *WixHandlerTestSuite) get(path string, accept ...string) (int, string, []byte) {
	h := http.Header{}
	if len(accept) > 0 {
		h.Set("Accept", accept[0])
	}
	res := s.GET("/media/"+path, h)
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	s.Require().NoError(err)
	return res.StatusCode, res.Header.Get("Content-Type"), body
}

// dims reads a PNG's IHDR.
func (s *WixHandlerTestSuite) dims(b []byte) (int, int) {
	s.Require().True(bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")), "expected a PNG")
	return int(binary.BigEndian.Uint32(b[16:20])), int(binary.BigEndian.Uint32(b[20:24]))
}

// ---------------------------------------------------------------- geometry

func (s *WixHandlerTestSuite) TestFitFloorsTheOutputDimension() {
	// `fit` scales to sit inside the box and FLOORS, so the result can be a
	// pixel under what was asked for. Rounding instead changes roughly half of
	// all fit renditions.
	//
	// The geometry matters: at w_200 the height works out to 153.125, where
	// floor and round agree and the test would prove nothing. At w_202 the
	// scale is 202/512 and the height is 392 * 0.39453125 = 154.65625 --
	// floor 154, round 155 -- so this fails if the rule is ever loosened.
	code, ct, body := s.get(wixRGB + "/v1/fit/w_202,h_1000/x.png")
	s.Require().Equal(http.StatusOK, code)
	s.Equal("image/png", ct)

	w, h := s.dims(body)
	s.Equal(202, w)
	s.Equal(154, h, "154.65625 must FLOOR to 154, not round to 155")
}

func (s *WixHandlerTestSuite) TestFitNeverEnlarges() {
	code, _, body := s.get(wixRGB + "/v1/fit/w_2000,h_2000/x.png")
	s.Require().Equal(http.StatusOK, code)
	w, h := s.dims(body)
	s.Equal(masterW, w, "fit must not enlarge")
	s.Equal(masterH, h)
}

func (s *WixHandlerTestSuite) TestFillIsExactlyTheRequestedSize() {
	for _, g := range []struct{ w, h int }{{137, 89}, {123, 66}, {451, 339}} {
		code, _, body := s.get(wixRGB +
			"/v1/fill/w_" + itoa(g.w) + ",h_" + itoa(g.h) + "/x.png")
		s.Require().Equal(http.StatusOK, code)
		w, h := s.dims(body)
		s.Equal(g.w, w)
		s.Equal(g.h, h)
	}
}

func (s *WixHandlerTestSuite) TestFillEnlarges() {
	code, _, body := s.get(wixRGB + "/v1/fill/w_1027,h_783/x.png")
	s.Require().Equal(http.StatusOK, code)
	w, h := s.dims(body)
	s.Equal(1027, w)
	s.Equal(783, h)
}

func (s *WixHandlerTestSuite) TestAlPresenceChangesTheCrop() {
	// WIX-URL-SPEC §3.2: `al`'s PRESENCE changes the centring rounding,
	// independently of its value -- it applies even to al_c, which is otherwise
	// a no-op. Measured 3/3 on exactly this master geometry.
	for _, g := range []string{"w_123,h_66", "w_451,h_339", "w_166,h_113"} {
		_, _, without := s.get(wixRGB + "/v1/fill/" + g + "/x.png")
		_, _, with := s.get(wixRGB + "/v1/fill/" + g + ",al_c/x.png")

		s.Require().NotEmpty(without)
		s.NotEqual(without, with,
			"al_c must change the crop for %s: presence selects separate halves", g)
	}
}

func (s *WixHandlerTestSuite) TestAlAnchorsActOnIndependentAxes() {
	// Only the axis with slack can matter. These crops are full-width, so the
	// horizontal anchors collapse onto the centre while the vertical ones do not.
	get := func(al string) []byte {
		_, _, b := s.get(wixRGB + "/v1/fill/w_123,h_66,al_" + al + "/x.png")
		return b
	}
	c, l, r := get("c"), get("l"), get("r")
	top, bottom := get("t"), get("b")

	s.Equal(c, l, "no horizontal slack: al_l collapses onto centre")
	s.Equal(c, r, "no horizontal slack: al_r collapses onto centre")
	s.NotEqual(c, top, "vertical slack: al_t must differ from centre")
	s.NotEqual(c, bottom, "vertical slack: al_b must differ from centre")
	s.NotEqual(top, bottom)
	s.Equal(top, get("tl"), "tl and t agree when there is no horizontal slack")
}

func (s *WixHandlerTestSuite) TestCropAndChaining() {
	s.Run("standalone crop is a plain extract", func() {
		code, _, body := s.get(wixRGB + "/v1/crop/x_10,y_20,w_30,h_40/x.png")
		s.Require().Equal(http.StatusOK, code)
		w, h := s.dims(body)
		s.Equal(30, w)
		s.Equal(40, h)
	})

	s.Run("out-of-bounds crop is clipped, not rejected", func() {
		code, _, body := s.get(wixRGB + "/v1/crop/x_500,y_380,w_100,h_100/x.png")
		s.Require().Equal(http.StatusOK, code, "must clip rather than error")
		w, h := s.dims(body)
		s.Equal(100, w)
		s.Equal(100, h)
	})

	s.Run("crop chained into fill", func() {
		code, _, body := s.get(wixRGB + "/v1/crop/x_10,y_10,w_300,h_300/fill/w_100,h_100/x.png")
		s.Require().Equal(http.StatusOK, code)
		w, h := s.dims(body)
		s.Equal(100, w)
		s.Equal(100, h)
	})

	s.Run("scale then scale composes rather than discarding the first", func() {
		_, _, a := s.get(wixRGB + "/v1/fit/w_200,h_200/fill/w_100,h_100/x.png")
		_, _, b := s.get(wixRGB + "/v1/fill/w_400,h_400/fill/w_100,h_100/x.png")
		s.Require().NotEmpty(a)
		s.NotEqual(a, b, "the first scale must affect the result")
	})

	s.Run("a trailing segment is never silently dropped", func() {
		code, _, body := s.get(wixRGB +
			"/v1/fill/w_200,h_200/crop/x_10,y_10,w_50,h_50/fit/w_25,h_25/x.png")
		s.Require().Equal(http.StatusOK, code)
		w, h := s.dims(body)
		s.Equal(25, w, "the trailing fit must be applied")
		s.Equal(25, h)
	})
}

func (s *WixHandlerTestSuite) TestEffectsChangeTheOutput() {
	_, _, plain := s.get(wixRGB + "/v1/fit/w_200,h_200/x.png")
	_, _, sharp := s.get(wixRGB + "/v1/fit/w_200,h_200,usm_0.66_1.00_0.01/x.png")
	_, _, blurry := s.get(wixRGB + "/v1/fit/w_200,h_200,blur_3/x.png")
	_, _, ignored := s.get(wixRGB + "/v1/fit/w_200,h_200,lg_1/x.png")

	s.Require().NotEmpty(plain)
	s.NotEqual(plain, sharp, "usm must be applied")
	s.NotEqual(plain, blurry, "blur must be applied")
	s.Equal(plain, ignored, "lg_N is accepted and ignored")
}

// ------------------------------------------------------------ negotiation

func (s *WixHandlerTestSuite) TestFormatNegotiation() {
	// WIX-URL-SPEC §4, verbatim. `enc` is an opt-in FLAG, not a format name:
	// the format comes from Accept, preferring avif > webp > the STORED
	// master's own format.
	for _, tc := range []struct {
		name, params, accept, want string
	}{
		{"enc + */*", "enc_avif", "*/*", "image/png"},
		{"enc + avif,webp,*/*", "enc_avif", "image/avif,image/webp,*/*", "image/avif"},
		{"enc + webp,*/*", "enc_avif", "image/webp,*/*", "image/webp"},
		{"enc + image/png", "enc_avif", "image/png", "image/png"},
		{"no enc", "", "image/avif,image/webp,*/*", "image/png"},
		{"enc_auto", "enc_auto", "image/avif,image/webp", "image/avif"},
		{"enc unrecognised", "enc_bogus", "image/avif,image/webp", "image/png"},
	} {
		s.Run(tc.name, func() {
			p := "w_100,h_100"
			if tc.params != "" {
				p += "," + tc.params
			}
			code, ct, _ := s.get(wixRGB+"/v1/fit/"+p+"/x.png", tc.accept)
			s.Require().Equal(http.StatusOK, code)
			s.Equal(tc.want, ct)
		})
	}
}

func (s *WixHandlerTestSuite) TestJPEGMasterFallsBackToJPEG() {
	// "Master's format" means the STORED format, sniffed from magic bytes.
	code, ct, _ := s.get(wixJPEG+"/v1/fit/w_100,h_100,enc_avif/x.jpg", "*/*")
	s.Require().Equal(http.StatusOK, code)
	s.Equal("image/jpeg", ct)
}

// WIX-URL-SPEC §9.1. A transform's headers are decided by the route, not by
// any TTL config, and they are constant across op, format, quality and
// geometry -- so the only thing that varies is Vary itself.
func (s *WixHandlerTestSuite) TestTransformResponseHeaders() {
	// Without enc_, Accept is ignored entirely (§4.1) and NO Vary is sent.
	// This is not merely "unset": advertising one would split caches on a
	// header that cannot change the answer.
	res := s.GET("/media/" + wixRGB + "/v1/fit/w_100,h_100/x.png")
	defer res.Body.Close()

	s.Equal("public, max-age=2592000, immutable", res.Header.Get("Cache-Control"))
	s.Empty(res.Header.Get("Vary"), "no enc_, so Accept cannot change the output")

	// A transform carries no validator and no freshness date of its own.
	for _, h := range []string{"ETag", "Last-Modified", "Expires", "Accept-Ranges"} {
		s.Empty(res.Header.Get(h), "transforms carry no %s", h)
	}

	// With enc_, and only then, the output does depend on Accept.
	for _, enc := range []string{"enc_auto", "enc_avif"} {
		res := s.GET("/media/" + wixRGB + "/v1/fit/w_100,h_100," + enc + "/x.png")
		s.Equal("Accept", res.Header.Get("Vary"), "%s opts into negotiation", enc)
		s.Equal("public, max-age=2592000, immutable", res.Header.Get("Cache-Control"))
		res.Body.Close()
	}
}

// Ranges are served -- 206 with a Content-Range -- even though the route never
// advertises accept-ranges on the full response. Both halves are measured CDN
// behaviour, and they look contradictory, so both are asserted.
func (s *WixHandlerTestSuite) TestTransformServesRangesWithoutAdvertising() {
	h := http.Header{"Range": []string{"bytes=0-9"}}
	res := s.GET("/media/"+wixRGB+"/v1/fit/w_100,h_100/x.png", h)
	defer res.Body.Close()

	s.Equal(http.StatusPartialContent, res.StatusCode)
	s.Regexp(`^bytes 0-9/\d+$`, res.Header.Get("Content-Range"))
	body, _ := io.ReadAll(res.Body)
	s.Len(body, 10)
}

// §9.2. The bare original is answered by a different service than transforms
// are, and the header set says so: six times the TTL, a validator, a date, and
// range support it actually advertises.
func (s *WixHandlerTestSuite) TestOriginalResponseHeaders() {
	res := s.GET("/media/" + wixRGB)
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	s.Require().NoError(err)

	s.Equal("public, max-age=15552000, immutable", res.Header.Get("Cache-Control"))
	s.Equal("bytes", res.Header.Get("Accept-Ranges"))
	s.NotEmpty(res.Header.Get("Expires"))
	s.Empty(res.Header.Get("Vary"), "no negotiation happens on this path")

	// The etag is md5 of the body -- verified by hashing, not assumed, because
	// a client may legitimately compute it itself to check integrity.
	s.Equal(fmt.Sprintf("%q", fmt.Sprintf("%x", md5.Sum(body))), res.Header.Get("ETag"))
}

// A conditional GET is honoured, and the 304 drops exactly three headers
// relative to the 200. Asserting what the 304 KEEPS matters as much: a cache
// that loses cache-control or etag on revalidation re-downloads every time.
func (s *WixHandlerTestSuite) TestOriginalConditionalGET() {
	res := s.GET("/media/" + wixRGB)
	etag := res.Header.Get("ETag")
	lastMod := res.Header.Get("Last-Modified")
	res.Body.Close()
	s.Require().NotEmpty(etag)

	for name, h := range map[string]http.Header{
		"If-None-Match":     {"If-None-Match": []string{etag}},
		"If-Modified-Since": {"If-Modified-Since": []string{lastMod}},
	} {
		res := s.GET("/media/"+wixRGB, h)
		s.Equal(http.StatusNotModified, res.StatusCode, "%s should revalidate", name)

		for _, dropped := range []string{"Content-Type", "Content-Length", "Last-Modified"} {
			s.Empty(res.Header.Get(dropped), "%s: 304 must omit %s", name, dropped)
		}
		s.Equal(etag, res.Header.Get("ETag"), "%s: 304 keeps the validator", name)
		s.Equal("public, max-age=15552000, immutable", res.Header.Get("Cache-Control"))
		res.Body.Close()
	}
}

// §9.3. The two error shapes are produced by different tiers and differ in
// status, body and even the ordering of the cache-control directives. Getting
// them backwards would make a routing failure look like a bad parameter.
func (s *WixHandlerTestSuite) TestErrorResponses() {
	const (
		forbiddenCC = "no-cache, private, must-revalidate, proxy-revalidate, no-store"
		badReqCC    = "private, no-cache, no-store, must-revalidate"
	)

	// Rejected upstream of the manipulator: unknown op, or no such media.
	for _, path := range []string{
		wixRGB + "/v1/bogus/w_1,h_1/x.png",
		wixRGB + "/v2/fit/w_1,h_1/x.png",
		"no_such_media~mv2.png/v1/fit/w_1,h_1/x.png",
		"no_such_media~mv2.png",
	} {
		res := s.GET("/media/" + path)
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()

		s.Equal(http.StatusForbidden, res.StatusCode, "path %q", path)
		s.Equal("Forbidden", string(body), "path %q", path)
		s.Equal(forbiddenCC, res.Header.Get("Cache-Control"), "path %q", path)
	}

	// Reached the manipulator, which reports which value it could not use.
	for path, want := range map[string]string{
		wixRGB + "/v1/fill/w_abc,h_100/x.png":     "(fil) (dimensions) invalid width abc",
		wixRGB + "/v1/fit/w_0,h_100/x.png":        "(fit) (dimensions) invalid width 0",
		wixRGB + "/v1/crop/x_q,y_0,w_5,h_5/x.png": "(crop) (coordinates) invalid x q",
	} {
		res := s.GET("/media/" + path)
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()

		s.Equal(http.StatusBadRequest, res.StatusCode, "path %q", path)
		s.Equal(want, string(body), "path %q", path)
		s.Equal(badReqCC, res.Header.Get("Cache-Control"), "path %q", path)
	}
}

// An unrecognised output extension is NOT an error: it falls back to the
// master's stored format and must be byte-identical to naming that format
// outright. imagetype knows both jxl and tiff, so without an explicit
// allowlist this silently answers .jxl with JPEG XL -- a format the CDN
// cannot produce.
func (s *WixHandlerTestSuite) TestUnknownExtensionFallsBackToMasterFormat() {
	want := s.GET("/media/" + wixRGB + "/v1/fit/w_100,h_100/x.png")
	wantBody, err := io.ReadAll(want.Body)
	want.Body.Close()
	s.Require().NoError(err)

	for _, ext := range []string{"jxl", "tiff", "tif", "bmp", "gif", "heic"} {
		res := s.GET("/media/" + wixRGB + "/v1/fit/w_100,h_100/x." + ext)
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()

		s.Equal(http.StatusOK, res.StatusCode, ".%s is not an error", ext)
		s.Equal("image/png", res.Header.Get("Content-Type"), ".%s -> master format", ext)
		s.Equal(wantBody, body, ".%s must be byte-identical to .png", ext)
	}
}

// §7.5 / §9.5. A GIF master is answered by the media router even on a /v1/
// transform url: the bytes come back untransformed AND under the original
// header set, not the transform one. The §9.2 shape here is the spec's
// prediction rather than a measurement, so this test pins our choice, not the
// CDN's observed behaviour.
func (s *WixHandlerTestSuite) TestGIFPassThroughUsesOriginalHeaders() {
	master, err := os.ReadFile(filepath.Join(s.masters, wixGIF))
	s.Require().NoError(err)

	// A geometry that would visibly change the image if it were applied.
	res := s.GET("/media/" + wixGIF + "/v1/fit/w_16,h_16/x.gif")
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	s.Require().NoError(err)

	s.Equal(http.StatusOK, res.StatusCode)
	s.Equal(master, body, "a GIF master is passed through untransformed")
	s.Equal("image/gif", res.Header.Get("Content-Type"))

	// The media router's header set, not the manipulator's.
	s.Equal("public, max-age=15552000, immutable", res.Header.Get("Cache-Control"))
	s.Equal("bytes", res.Header.Get("Accept-Ranges"))
	s.Equal(fmt.Sprintf("%q", fmt.Sprintf("%x", md5.Sum(master))), res.Header.Get("ETag"))
	s.NotEmpty(res.Header.Get("Expires"))
	s.Empty(res.Header.Get("Vary"))
}

// §9.4. No compression on any path, even when the client asks for it.
func (s *WixHandlerTestSuite) TestNeverContentEncoded() {
	h := http.Header{"Accept-Encoding": []string{"gzip, br, deflate"}}
	for _, path := range []string{wixRGB + "/v1/fit/w_100,h_100/x.png", wixRGB} {
		res := s.GET("/media/"+path, h)
		s.Empty(res.Header.Get("Content-Encoding"), "path %q", path)
		res.Body.Close()
	}
}

func (s *WixHandlerTestSuite) TestWebPCodecFollowsTheStoredMaster() {
	// WIX-URL-SPEC §5: a lossless master (PNG) encodes to VP8L, a lossy one
	// (JPEG) to VP8. Not alpha presence, and not whichever is smaller.
	for _, tc := range []struct {
		name, id, want string
	}{
		{"png master is lossless", wixRGB, "VP8L"},
		{"rgba png master is still lossless", wixRGBA, "VP8L"},
		{"jpeg master is lossy", wixJPEG, "VP8 "},
	} {
		s.Run(tc.name, func() {
			code, ct, body := s.get(tc.id+"/v1/fit/w_100,h_100,enc_auto/x", "image/webp")
			s.Require().Equal(http.StatusOK, code)
			s.Require().Equal("image/webp", ct)
			s.Equal(tc.want, webpCodec(body))
		})
	}
}

// --------------------------------------------------------------- metadata

func (s *WixHandlerTestSuite) TestWebPCarriesTheCDNContainer() {
	// OP-SPEC §8.4: the CDN always emits exactly
	//   RIFF WEBP  VP8X  [ICCP]  [ALPH]  VP8|VP8L  EXIF
	// The coded payload was already exact before these fix-ups existed, which
	// is precisely why a payload-only check called WebP finished while the
	// files still differed.
	for _, tc := range []struct {
		name, id   string
		wantChunks []string
		wantFlags  byte
	}{
		{"png master", wixRGB, []string{"VP8X", "VP8L", "EXIF"}, 0x08},
		{"rgba master", wixRGBA, []string{"VP8X", "VP8L", "EXIF"}, 0x18},
		{"jpeg master", wixJPEG, []string{"VP8X", "VP8 ", "EXIF"}, 0x08},
		{"profiled master", wixICC, []string{"VP8X", "ICCP", "VP8L", "EXIF"}, 0x28},
	} {
		s.Run(tc.name, func() {
			code, ct, body := s.get(tc.id+"/v1/fit/w_100,h_100,enc_auto/x", "image/webp")
			s.Require().Equal(http.StatusOK, code)
			s.Require().Equal("image/webp", ct)

			cs, err := wixspec.SplitWebP(body)
			s.Require().NoError(err)

			got := make([]string, len(cs))
			for i, c := range cs {
				got[i] = c.FourCC
			}
			s.Equal(tc.wantChunks, got)
			s.NotContains(got, "XMP ", "libvips writes XMP; the CDN never does")

			s.Equal(tc.wantFlags, cs[0].Data[0], "VP8X flags")
			s.Len(cs[len(cs)-1].Data, 186, `EXIF is "Exif\0\0" + the canonical 180 bytes`)
		})
	}
}

func (s *WixHandlerTestSuite) TestJPEGCarriesTheCDNContainer() {
	// OP-SPEC §8.5: SOI APP1(Exif) [APP2(ICC_PROFILE)] DQT ... EOI, with no
	// APP0 JFIF and no trailing bytes.
	for _, tc := range []struct {
		name, id string
		wantAPP2 bool
	}{
		{"jpeg master", wixJPEG, false},
		{"profiled master", wixICC, true},
	} {
		s.Run(tc.name, func() {
			// No enc_, and a .jpg name: §4.1 makes the extension decide.
			code, ct, body := s.get(tc.id + "/v1/fit/w_100,h_100/x.jpg")
			s.Require().Equal(http.StatusOK, code)
			s.Require().Equal("image/jpeg", ct)

			markers, exif, icc := jpegHeader(s.T(), body)

			s.Equal(byte(0xE1), markers[0], "APP1 comes first")
			s.NotContains(markers, byte(0xE0), "the APP0 JFIF is dropped")
			s.Len(exif, 186, `APP1 is "Exif\0\0" + the canonical 180 bytes`)
			s.Equal("II", string(exif[6:8]), "rebuilt little-endian")

			if tc.wantAPP2 {
				s.Require().NotNil(icc, "a profiled master keeps its ICC APP2")
				s.True(bytes.HasPrefix(icc, []byte("ICC_PROFILE\x00")))
				s.Len(markers, 2, "exactly APP1 then APP2")
			} else {
				s.Nil(icc, "an unprofiled master yields no APP2")
				s.Len(markers, 1, "APP1 only")
			}
		})
	}
}

func (s *WixHandlerTestSuite) TestJPEGDoesNotLeakMasterMetadata() {
	// The largest metadata leak of any output format if the fix-ups are
	// skipped: a full camera EXIF with an embedded thumbnail, plus XMP.
	code, ct, body := s.get(wixRot + "/v1/fit/w_100,h_100/x.jpg")
	s.Require().Equal(http.StatusOK, code)
	s.Require().Equal("image/jpeg", ct)

	_, exif, _ := jpegHeader(s.T(), body)
	s.Len(exif, 186, "only the canonical block survives")
}

func (s *WixHandlerTestSuite) TestAVIFIsEncodedByLibavif() {
	// OP-SPEC §7.5. The CDN's AVIFs carry `hdlr` name "libavif" and libavif's
	// own box layout, so libheif output would be the wrong encoder however it
	// was tuned. This is the discriminator.
	code, ct, body := s.get(wixRGB+"/v1/fit/w_100,h_100,enc_avif/x",
		"image/avif,image/webp,*/*")
	s.Require().Equal(http.StatusOK, code)
	s.Require().Equal("image/avif", ct)

	s.Equal([]string{"ftyp", "meta", "mdat"}, isobmffBoxes(body))
	s.True(bytes.Contains(body, []byte("libavif")),
		"the hdlr name identifies the encoder; libheif output would not carry it")
	s.False(bytes.Contains(body, []byte("libheif")))
}

func (s *WixHandlerTestSuite) TestAVIFAlwaysCarriesAnAlphaItem() {
	// A consequence of avifEncoderAddImage's flags being 0 rather than
	// AVIF_ADD_IMAGE_FLAG_SINGLE: the flag would DROP a fully opaque alpha
	// plane, and prod emits an alpha item on every rendition including ones
	// whose master had no alpha at all.
	for _, tc := range []struct{ name, id string }{
		{"opaque jpeg master", wixJPEG},
		{"opaque png master", wixRGB},
		{"rgba master", wixRGBA},
	} {
		s.Run(tc.name, func() {
			_, ct, body := s.get(tc.id+"/v1/fit/w_100,h_100,enc_avif/x",
				"image/avif,image/webp,*/*")
			s.Require().Equal("image/avif", ct)
			s.Greater(bytes.Count(body, []byte("av01")), 1,
				"a second av01 item is the alpha aux item")
		})
	}
}

func (s *WixHandlerTestSuite) TestAVIFQualitySelectsTheQuantizer() {
	// q_N selects minQuantizer from the measured table, so a lower q must
	// produce a visibly smaller file. A url with no q_ behaves as q_90.
	size := func(params string) int {
		_, ct, body := s.get(wixRGB+"/v1/fit/w_200,h_200,"+params+"/x",
			"image/avif,image/webp,*/*")
		s.Require().Equal("image/avif", ct)
		return len(body)
	}
	q90, q50, q10 := size("enc_avif"), size("q_50,enc_avif"), size("q_10,enc_avif")

	s.Greater(q90, q50, "q_50 quantises harder than the q_90 default")
	s.Greater(q50, q10)
}

func (s *WixHandlerTestSuite) TestRenditionsDoNotLeakSourceMetadata() {
	// OP-SPEC §8.2 calls this a security control: libvips forwards source
	// metadata by default, so without the container fix-ups every rendition
	// carries whatever the uploader's camera recorded.
	for _, tc := range []struct{ name, params, accept string }{
		{"png", "", ""},
		{"webp", ",enc_auto", "image/webp"},
		{"avif", ",enc_avif", "image/avif,image/webp,*/*"},
	} {
		s.Run(tc.name, func() {
			code, _, body := s.get(wixLeaky+"/v1/fit/w_100,h_100"+tc.params+"/x", tc.accept)
			s.Require().Equal(http.StatusOK, code)

			for _, canary := range []string{
				"CANARY-CAMERA-MAKE", "SECRET-MODEL-XYZ", "2019:07:04", "leaked-by-ztxt",
			} {
				s.NotContains(string(body), canary,
					"%s rendition leaked %q from the master", tc.name, canary)
			}
		})
	}
}

func (s *WixHandlerTestSuite) TestPNGRenditionCarriesTheCanonicalEXIF() {
	code, _, body := s.get(wixLeaky + "/v1/fit/w_100,h_100/x.png")
	s.Require().Equal(http.StatusOK, code)

	types, exif := pngChunks(body)
	s.NotContains(types, "zTXt", "libvips writes zTXt; the CDN does not")
	s.Equal(1, countOf(types, "eXIf"),
		"exactly one eXIf: libvips 8.15.5 emits two for a source that carries one")
	s.Require().NotNil(exif)
	s.Len(exif, 180, "the canonical 11-tag block is a fixed 180 bytes")
	s.Equal("II", string(exif[:2]), "little-endian")
}

func (s *WixHandlerTestSuite) TestBareMediaPathServesTheOriginalUntouched() {
	// WIX-URL-SPEC §7.2 -- this is a LEAK, reproduced deliberately for
	// compatibility. The bare path must not travel the transform path, because
	// the §7.1 whitelist would strip it and the behaviour would no longer match.
	original, err := os.ReadFile(filepath.Join(s.masters, wixLeaky))
	s.Require().NoError(err)

	code, ct, body := s.get(wixLeaky)
	s.Require().Equal(http.StatusOK, code)
	s.Equal("image/png", ct)
	s.Equal(original, body, "the bare path must return the stored bytes exactly")
	s.Contains(string(body), "CANARY-CAMERA-MAKE",
		"the leak is intentional; if this fails the behaviour changed on purpose")
}

// ----------------------------------------------------------------- errors

func (s *WixHandlerTestSuite) TestRejectsMalformedPaths() {
	for _, p := range []string{
		wixRGB + "/fill/w_10,h_10/x.png",     // no /v1/
		wixRGB + "/v1/",                      // no segment
		wixRGB + "/v1/bogus/w_10,h_10/x.png", // unknown op
		wixRGB + "/v1/fill/w_10/x.png",       // missing h
		"does-not-exist~mv2.png/v1/fit/w_10,h_10/x.png",
	} {
		code, _, _ := s.get(p)
		s.NotEqual(http.StatusOK, code, "%s should not succeed", p)
	}
}

func (s *WixHandlerTestSuite) TestNativeGrammarStillWorks() {
	// The Wix route is an addition; imgproxy's own grammar must be unaffected.
	res := s.GET("/unsafe/rs:fill:50:50/plain/local:///" + wixRGB)
	defer res.Body.Close()
	s.Equal(http.StatusOK, res.StatusCode)
}

func (s *WixHandlerTestSuite) TestRouteIsOffByDefault() {
	s.Config().Handlers.Wix.Enabled = false
	s.Config().Handlers.Wix.SourceURLTemplate = ""

	res := s.GET("/media/" + wixRGB + "/v1/fit/w_10,h_10/x.png")
	defer res.Body.Close()
	// Falls through to the native grammar, which cannot parse this path.
	s.NotEqual(http.StatusOK, res.StatusCode)
}

func (s *WixHandlerTestSuite) TestProfiledMasterIsConvertedToWixSRGB() {
	// OP-SPEC §3: a profiled master is converted to Wix's own sRGB, which is
	// NOT libvips' built-in. Skipping it leaves the master's profile in iCCP --
	// pixels match and bytes do not.
	wixProfile, err := wixspec.SRGBProfile()
	s.Require().NoError(err)

	code, _, body := s.get(wixICC + "/v1/fit/w_200,h_200/x.png")
	s.Require().Equal(http.StatusOK, code)

	got, ok := iccpProfile(body)
	s.Require().True(ok, "a profiled master must produce a profiled rendition")
	s.Equal(wixProfile, got, "output must carry Wix's sRGB, not the master's profile")
	s.NotEqual(s.masterProfile, got)
}

func (s *WixHandlerTestSuite) TestBareCropIsAlsoColourConverted() {
	// "This applies to EVERY operation, including a bare crop that does no
	// resampling." The identity branch skips the resample entirely, so it is
	// exactly where the colour transform is easiest to omit by accident.
	wixProfile, err := wixspec.SRGBProfile()
	s.Require().NoError(err)

	code, _, body := s.get(wixICC + "/v1/crop/x_10,y_10,w_50,h_50/x.png")
	s.Require().Equal(http.StatusOK, code)

	got, ok := iccpProfile(body)
	s.Require().True(ok)
	s.Equal(wixProfile, got,
		"a bare crop must still be converted; otherwise pixels match and bytes do not")
}

func (s *WixHandlerTestSuite) TestUnprofiledMasterIsLeftAlone() {
	// "Skip entirely when the master has no embedded profile."
	code, _, body := s.get(wixRGB + "/v1/fit/w_200,h_200/x.png")
	s.Require().Equal(http.StatusOK, code)
	_, ok := iccpProfile(body)
	s.False(ok, "an unprofiled master must not gain a profile")
}

func (s *WixHandlerTestSuite) TestOrientationIsAppliedBeforeGeometry() {
	// OP-SPEC §4.0. Getting this wrong does not merely rotate the output: sw x
	// sh feed the scale and the output box, so a `fit` box that the stored
	// (landscape) size would fill exactly is instead limited by the rotated
	// (portrait) height, giving a different size entirely.
	//
	// Master is 512x392 landscape, tagged Orientation 6, so it is 392x512
	// portrait once rotated. fit w_512,h_512:
	//   stored size  -> scale 1.0,     512x392   WRONG
	//   rotated size -> scale 1.0,     392x512
	code, _, body := s.get(wixRot + "/v1/fit/w_512,h_512/x.png")
	s.Require().Equal(http.StatusOK, code)

	w, h := s.dims(body)
	s.Equal(392, w, "width must come from the ROTATED master")
	s.Equal(512, h)
	s.Greater(h, w, "an Orientation 6 landscape master renders portrait")
}

func (s *WixHandlerTestSuite) TestOrientationChangesTheComputedScale() {
	// The sharper version of the same rule: with a non-square box, the stored
	// and rotated sizes select different scales, so the output box differs --
	// not just its orientation.
	code, _, body := s.get(wixRot + "/v1/fit/w_200,h_100/x.png")
	s.Require().Equal(http.StatusOK, code)

	w, h := s.dims(body)
	// rotated 392x512 into 200x100: scale = min(200/392, 100/512) = 0.195312
	// -> floor(392*0.195312)=76, floor(512*0.195312)=100
	s.Equal(76, w)
	s.Equal(100, h)
}

func TestWixHandler(t *testing.T) {
	suite.Run(t, new(WixHandlerTestSuite))
}

// ------------------------------------------------------------- generators

// gradientPNG builds a master that varies in BOTH axes. A probe symmetric in an
// axis cannot test that axis -- which is how a vertical resampling difference
// hid behind horizontal-only probes for a long time.
func gradientPNG(alpha bool) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, masterW, masterH))
	for y := range masterH {
		for x := range masterW {
			a := uint8(255)
			if alpha {
				a = 128
			}
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8((x*7 + y*3) % 256),
				G: uint8((x*x/13 + y*11) % 256),
				B: uint8(((x ^ y) * 5) % 256),
				A: a,
			})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

// orientedJPEG builds a landscape JPEG carrying an EXIF Orientation tag.
func orientedJPEG(orientation uint16) []byte {
	base := gradientJPEG()

	var e bytes.Buffer
	le := binary.LittleEndian
	e.WriteString("II")
	_ = binary.Write(&e, le, uint16(42))
	_ = binary.Write(&e, le, uint32(8))
	_ = binary.Write(&e, le, uint16(1))
	_ = binary.Write(&e, le, uint16(0x0112)) // Orientation
	_ = binary.Write(&e, le, uint16(3))      // SHORT
	_ = binary.Write(&e, le, uint32(1))
	_ = binary.Write(&e, le, orientation)
	_ = binary.Write(&e, le, uint16(0))
	_ = binary.Write(&e, le, uint32(0))

	app1 := append([]byte("Exif\x00\x00"), e.Bytes()...)

	var out bytes.Buffer
	out.Write(base[:2]) // SOI
	out.Write([]byte{0xFF, 0xE1})
	var n [2]byte
	binary.BigEndian.PutUint16(n[:], uint16(len(app1)+2))
	out.Write(n[:])
	out.Write(app1)
	out.Write(base[2:])
	return out.Bytes()
}

func gradientJPEG() []byte {
	img, _ := png.Decode(bytes.NewReader(gradientPNG(false)))
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, &jpeg.Options{Quality: 90})
	return b.Bytes()
}

// leakyPNG carries camera and timestamp EXIF plus a zTXt chunk, so a rendition
// that forwards either can be detected.
func leakyPNG() []byte {
	base := gradientPNG(false)
	chunks, _ := splitPNGChunks(base)

	out := [][2]any{}
	for _, c := range chunks {
		out = append(out, [2]any{c.typ, c.data})
		if c.typ == "IHDR" {
			out = append(out, [2]any{"eXIf", cameraEXIF()})
			out = append(out, [2]any{"zTXt", append([]byte("Software\x00\x00"), zlibDeflate("leaked-by-ztxt")...)})
		}
	}

	var buf bytes.Buffer
	buf.Write([]byte("\x89PNG\r\n\x1a\n"))
	for _, c := range out {
		typ := c[0].(string)
		data := c[1].([]byte)
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(data)))
		buf.Write(n[:])
		buf.WriteString(typ)
		buf.Write(data)
		h := crc32.NewIEEE()
		h.Write([]byte(typ))
		h.Write(data)
		var s [4]byte
		binary.BigEndian.PutUint32(s[:], h.Sum32())
		buf.Write(s[:])
	}
	return buf.Bytes()
}

// cameraEXIF is a minimal little-endian TIFF with Make, Model and DateTime.
func cameraEXIF() []byte {
	entries := []struct {
		tag uint16
		val string
	}{
		{0x010F, "CANARY-CAMERA-MAKE\x00"},
		{0x0110, "SECRET-MODEL-XYZ\x00"},
		{0x0132, "2019:07:04 11:22:33\x00"},
	}

	var b bytes.Buffer
	le := binary.LittleEndian
	b.WriteString("II")
	_ = binary.Write(&b, le, uint16(42))
	_ = binary.Write(&b, le, uint32(8))
	_ = binary.Write(&b, le, uint16(len(entries)))

	dataAt := 8 + 2 + len(entries)*12 + 4
	var extra []byte
	for _, e := range entries {
		_ = binary.Write(&b, le, e.tag)
		_ = binary.Write(&b, le, uint16(2)) // ASCII
		_ = binary.Write(&b, le, uint32(len(e.val)))
		_ = binary.Write(&b, le, uint32(dataAt+len(extra)))
		extra = append(extra, e.val...)
	}
	_ = binary.Write(&b, le, uint32(0))
	b.Write(extra)
	return b.Bytes()
}
