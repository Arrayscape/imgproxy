package integration_test

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/imgproxy/imgproxy/v4/testutil/servertest"
)

// Media ids for the generated masters. The extension is deliberately part of
// the id and deliberately not trusted: WIX-URL-SPEC §1.1.
const (
	wixRGB   = "0a7ba9_dddddddddddddddddddddddddddddddd~mv2.png"
	wixRGBA  = "0a7ba9_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb~mv2.png"
	wixJPEG  = "0a7ba9_cccccccccccccccccccccccccccccccc~mv2.jpg"
	wixLeaky = "0a7ba9_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee~mv2.png"
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

	masters string
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

func (s *WixHandlerTestSuite) TestVaryAccept() {
	res := s.GET("/media/" + wixRGB + "/v1/fit/w_100,h_100/x.png")
	defer res.Body.Close()
	s.Contains(res.Header.Get("Vary"), "Accept",
		"output depends on Accept, so caches must vary on it")
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
