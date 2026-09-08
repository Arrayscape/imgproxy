package integration_test

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/imgproxy/imgproxy/v4/testutil/servertest"
)

// Master formats beyond the three one site's corpus happened to contain.
//
// WIX-URL-SPEC §1.1 measured 366 masters as png/jpeg/webp only, but that is a
// property of that corpus, not of the CDN. A general emulator must handle every
// format it can decode, so these render real TIFF, JXL, GIF and BMP masters
// through the Wix path.
//
// Uses the test-images submodule; skipped when it is not checked out.

type WixFormatsTestSuite struct {
	servertest.Suite
	masters string
}

// masterFormats maps a media id to a source file in the test-images submodule.
var masterFormats = map[string]string{
	"0a7ba9_10000000000000000000000000000000~mv2.tiff": "tiff/8-bpp.tiff",
	"0a7ba9_20000000000000000000000000000000~mv2.jxl":  "jxl/8-bpp.jxl",
	"0a7ba9_30000000000000000000000000000000~mv2.gif":  "gif/gif.gif",
	"0a7ba9_40000000000000000000000000000000~mv2.bmp":  "bmp/24-bpp.bmp",
	// The extension deliberately lies here: a TIFF wearing a .png media id,
	// which is the §1.1 trap generalised beyond WebP.
	"0a7ba9_50000000000000000000000000000000~mv2.png": "tiff/8-bpp.tiff",
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

	for id, rel := range masterFormats {
		b, err := os.ReadFile(filepath.Join(src, rel))
		if err != nil {
			continue // a format this checkout does not carry
		}
		s.Require().NoError(os.WriteFile(filepath.Join(dir, id), b, 0o644))
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

func TestWixFormats(t *testing.T) {
	suite.Run(t, new(WixFormatsTestSuite))
}
