package integration_test

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/imgproxy/imgproxy/v4/testutil/servertest"
)

// Master formats beyond png/jpeg/webp.
//
// Two groups, and the distinction is the point:
//
// GIF, BMP, HEIC and AVIF are accepted uploads that this corpus merely does not
// contain. Untested is not unsupported, so they render -- format detection
// delegates to imgproxy's registry rather than a hand-written list, which is
// what stops "the corpus didn't happen to contain that" from becoming a bug.
//
// TIFF and JPEG XL are REFUSED. WIX-URL-SPEC §7.4 measured the real upload API:
// a TIFF upload is transcoded, so the canonical id is the `~mv2.png` derivative
// and renditions come from that (the original TIFF is preserved, but only at
// the `~mv2.tif` id, served untransformed); and JPEG XL is rejected at upload
// by content sniffing. Neither can reach the CDN's transform pipeline as a
// renderable master, so there is no observable behaviour to reproduce.
// Rendering them would be inventing a transform and calling it emulation.
//
// Uses the test-images submodule; skipped when it is not checked out.

type WixFormatsTestSuite struct {
	servertest.Suite
	masters string
}

// masterFormats maps a media id to a source file in the test-images submodule.
// masterFormats render through the normal transform path.
//
// AVIF is here because it is the one format ingest keeps verbatim that needs a
// real decoder, and the reference build has no libheif at all. These tests
// prove it DECODES AND RENDERS rather than erroring -- they do NOT prove
// byte-exactness. AVIF input is measured at 3/12 whole-file, with the residual
// in the decode's YCbCr->RGB matrix step. Zero AVIF masters in the corpus.
var masterFormats = map[string]string{
	"0a7ba9_70000000000000000000000000000000~mv2.avif": "avif/avif.avif",
}

// refusedFormats must NOT render. §9.1: ingest either transcodes these (TIFF,
// HEIC, BMP -- the canonical id becomes a ~mv2.png derivative) or rejects them
// (JPEG XL), so no renderable master of them can exist and the CDN's behaviour
// on the transform path is unobservable.
var refusedFormats = map[string]string{
	"0a7ba9_10000000000000000000000000000000~mv2.tiff": "tiff/8-bpp.tiff",
	"0a7ba9_20000000000000000000000000000000~mv2.jxl":  "jxl/8-bpp.jxl",
	"0a7ba9_40000000000000000000000000000000~mv2.bmp":  "bmp/24-bpp.bmp",
	"0a7ba9_80000000000000000000000000000000~mv2.heic": "heif/heif.heif",
	// The extension lies here -- a TIFF wearing a .png media id -- so this also
	// proves the refusal is decided by content, as Wix's own 406 is.
	"0a7ba9_50000000000000000000000000000000~mv2.png": "tiff/8-bpp.tiff",
}

// passThroughFormats are served untransformed, bytes unchanged.
var passThroughFormats = map[string]string{
	"0a7ba9_30000000000000000000000000000000~mv2.gif": "gif/gif.gif",
}

func (s *WixFormatsTestSuite) SetupSuite() {
	s.Suite.SetupSuite()

	src := filepath.Join(s.TestData.Root(), "test-images")
	if _, err := os.Stat(src); err != nil {
		s.T().Skip("test-images submodule not checked out")
	}

	dir, err := os.MkdirTemp("", "wix-formats-")
	s.Require().NoError(err)
	s.masters = dir

	for _, set := range []map[string]string{masterFormats, refusedFormats, passThroughFormats} {
		for id, rel := range set {
			b, err := os.ReadFile(filepath.Join(src, rel))
			if err != nil {
				continue // a format this checkout does not carry
			}
			s.Require().NoError(os.WriteFile(filepath.Join(dir, id), b, 0o644))
		}
	}
}

func (s *WixFormatsTestSuite) TearDownSuite() {
	if s.masters != "" {
		os.RemoveAll(s.masters)
	}
	s.Suite.TearDownSuite()
}

func (s *WixFormatsTestSuite) SetupTest()    { s.configure() }
func (s *WixFormatsTestSuite) SetupSubTest() { s.ResetLazyObjects(); s.configure() }

func (s *WixFormatsTestSuite) configure() {
	c := s.Config()
	c.Fetcher.Transport.Local.Root = s.masters
	c.Handlers.Wix.Enabled = true
	c.Handlers.Wix.SourceURLTemplate = "local:///%s"
}

func (s *WixFormatsTestSuite) TestEveryDecodableMasterFormatRenders() {
	for id, rel := range masterFormats {
		s.Run(rel+" as "+filepath.Ext(id), func() {
			if _, err := os.Stat(filepath.Join(s.masters, id)); err != nil {
				s.T().Skip("not present in this checkout")
			}
			res := s.GET("/media/" + id + "/v1/fit/w_100,h_100/x")
			defer res.Body.Close()
			s.Equal(http.StatusOK, res.StatusCode,
				"a %s master must render; the CDN is not limited to the formats "+
					"one corpus happened to contain", rel)
		})
	}
}

func (s *WixFormatsTestSuite) TestNegotiationFallsBackToTheStoredFormat() {
	// §4: with no `enc`, output is the STORED master's format -- whatever that
	// format is, including ones outside png/jpeg/webp.
	for id, rel := range masterFormats {
		s.Run(rel, func() {
			if _, err := os.Stat(filepath.Join(s.masters, id)); err != nil {
				s.T().Skip("not present in this checkout")
			}
			res := s.GET("/media/" + id + "/v1/fit/w_100,h_100/x")
			defer res.Body.Close()
			s.Require().Equal(http.StatusOK, res.StatusCode)
			s.NotEmpty(res.Header.Get("Content-Type"))
			s.NotEqual("text/plain", res.Header.Get("Content-Type"))
		})
	}
}

func (s *WixFormatsTestSuite) TestWebPNegotiationWorksForEveryMaster() {
	for id, rel := range masterFormats {
		s.Run(rel, func() {
			if _, err := os.Stat(filepath.Join(s.masters, id)); err != nil {
				s.T().Skip("not present in this checkout")
			}
			res := s.GET("/media/"+id+"/v1/fit/w_100,h_100,enc_auto/x",
				http.Header{"Accept": {"image/webp"}})
			defer res.Body.Close()
			s.Require().Equal(http.StatusOK, res.StatusCode)
			s.Equal("image/webp", res.Header.Get("Content-Type"))
		})
	}
}

func (s *WixFormatsTestSuite) TestGIFIsPassedThroughUntransformed() {
	// §9.1: ingest stores GIF verbatim and the media router answers rather than
	// the image manipulator, so the transform is NOT applied. A `fit` that
	// would shrink the image returns the FULL-SIZE original instead, byte for
	// byte -- which is also how animation survives.
	for id, rel := range passThroughFormats {
		s.Run(rel, func() {
			path := filepath.Join(s.masters, id)
			original, err := os.ReadFile(path)
			if err != nil {
				s.T().Skip("not present in this checkout")
			}

			res := s.GET("/media/" + id + "/v1/fit/w_180,h_135/x.gif")
			defer res.Body.Close()
			s.Require().Equal(http.StatusOK, res.StatusCode)

			body, err := io.ReadAll(res.Body)
			s.Require().NoError(err)

			s.Equal("image/gif", res.Header.Get("Content-Type"))
			s.Equal(original, body,
				"a GIF master must come back untransformed, byte for byte, "+
					"even though the url asks for w_180,h_135")
		})
	}
}

func (s *WixFormatsTestSuite) TestGIFPassThroughIgnoresEveryTransform() {
	id := "0a7ba9_30000000000000000000000000000000~mv2.gif"
	original, err := os.ReadFile(filepath.Join(s.masters, id))
	if err != nil {
		s.T().Skip("not present in this checkout")
	}
	// Not just fit: nothing on the transform path applies.
	for _, u := range []string{
		"fit/w_10,h_10",
		"fill/w_50,h_50,al_c",
		"crop/x_1,y_1,w_20,h_20/fill/w_5,h_5",
		"fit/w_60,h_60,usm_0.66_1.00_0.01,blur_2",
	} {
		s.Run(u, func() {
			res := s.GET("/media/" + id + "/v1/" + u + "/x.gif")
			defer res.Body.Close()
			s.Require().Equal(http.StatusOK, res.StatusCode)
			body, _ := io.ReadAll(res.Body)
			s.Equal(original, body)
		})
	}
}

func (s *WixFormatsTestSuite) TestRefusedFormatsAreNotRendered() {
	// Refusing is the honest answer while the behaviour is unobservable: the
	// CDN's upload path transcodes TIFF and rejects JXL, so it never renders
	// either and there is nothing to reproduce.
	for id, rel := range refusedFormats {
		s.Run(rel+" as "+filepath.Ext(id), func() {
			if _, err := os.Stat(filepath.Join(s.masters, id)); err != nil {
				s.T().Skip("not present in this checkout")
			}
			res := s.GET("/media/" + id + "/v1/fit/w_100,h_100/x")
			defer res.Body.Close()

			s.Equal(http.StatusUnprocessableEntity, res.StatusCode,
				"a %s master must be refused, not rendered (WIX-URL-SPEC §7.4)", rel)
		})
	}
}

func (s *WixFormatsTestSuite) TestRefusalIsDecidedByContentNotExtension() {
	// A TIFF wearing a .png media id is still refused -- the same way Wix's own
	// upload service returns 406 on JXL bytes declared as PNG.
	id := "0a7ba9_50000000000000000000000000000000~mv2.png"
	if _, err := os.Stat(filepath.Join(s.masters, id)); err != nil {
		s.T().Skip("not present in this checkout")
	}
	res := s.GET("/media/" + id + "/v1/fit/w_100,h_100/x")
	defer res.Body.Close()
	s.Equal(http.StatusUnprocessableEntity, res.StatusCode,
		"the media-id extension must not be able to smuggle a refused format through")
}

func TestWixFormats(t *testing.T) {
	suite.Run(t, new(WixFormatsTestSuite))
}
