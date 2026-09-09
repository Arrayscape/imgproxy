package integration_test

import (
	"encoding/binary"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/imgproxy/imgproxy/v4/testutil/servertest"
)

// The derivation cache. OP-SPEC §10 / WIX-URL-SPEC §6.
//
// The behaviour under test is not "the cache is fast" -- it is that deriving
// deliberately changes output bytes, and that the default leaves the
// byte-exact master path untouched.

type WixCacheTestSuite struct {
	servertest.Suite

	masters string
	derive  bool
}

func (s *WixCacheTestSuite) SetupSuite() {
	s.Suite.SetupSuite()
	dir, err := os.MkdirTemp("", "wix-cache-masters-")
	s.Require().NoError(err)
	s.masters = dir
	s.Require().NoError(os.WriteFile(filepath.Join(dir, wixRGB), gradientPNG(false), 0o644))
}

func (s *WixCacheTestSuite) TearDownSuite() {
	if s.masters != "" {
		os.RemoveAll(s.masters)
	}
	s.Suite.TearDownSuite()
}

func (s *WixCacheTestSuite) SetupTest()    { s.configure() }
func (s *WixCacheTestSuite) SetupSubTest() { s.ResetLazyObjects(); s.configure() }

func (s *WixCacheTestSuite) configure() {
	c := s.Config()
	c.Fetcher.Transport.Local.Root = s.masters
	c.Handlers.Wix.Enabled = true
	c.Handlers.Wix.SourceURLTemplate = "local:///%s"
	c.Handlers.Wix.DerivationCache = s.derive

	// These tests are about which input produced a rendition, which production
	// deliberately does not disclose -- the CDN's derived and from-master
	// responses are identical at the header level (OP-SPEC §12). The header is
	// opt-in for exactly this reason.
	c.Handlers.Wix.DebugSourceHeader = true
}

// fetch returns the body and the X-Wix-Source header, which reports whether the
// response came from the master, a cached rendition, or an exact cache hit.
func (s *WixCacheTestSuite) fetch(path string) ([]byte, string) {
	res := s.GET("/media/" + path)
	defer res.Body.Close()
	s.Require().Equal(http.StatusOK, res.StatusCode)
	b, err := io.ReadAll(res.Body)
	s.Require().NoError(err)
	return b, res.Header.Get("X-Wix-Source")
}

// pngPhys reads the pixels-per-metre from a PNG's pHYs chunk.
//
// This is the marker that distinguishes the two inputs (WIX-URL-SPEC §6.1):
// a master render carries the master's own resolution through, while a derived
// one reads 1000 -- libvips' default -- because the resolution was lost in the
// intermediate's write/re-read cycle.
func pngPhys(b []byte) (uint32, bool) {
	chunks, _ := splitPNGChunks(b)
	for _, c := range chunks {
		if c.typ == "pHYs" && len(c.data) >= 4 {
			return binary.BigEndian.Uint32(c.data[:4]), true
		}
	}
	return 0, false
}

func (s *WixCacheTestSuite) TestDisabledByDefault() {
	s.derive = false
	s.ResetLazyObjects()
	s.configure()

	// Warm the cache the way a derived render would need, then ask for a
	// smaller size that a derivation would have been able to serve.
	_, first := s.fetch(wixRGB + "/v1/fit/w_400,h_400/x.png")
	body, second := s.fetch(wixRGB + "/v1/fit/w_100,h_100/x.png")

	s.Equal("master", first)
	s.Equal("master", second,
		"with the cache off, nothing may ever be served from a rendition")

	phys, ok := pngPhys(body)
	if ok {
		s.NotEqual(uint32(1000), phys,
			"a master render must not carry the derived-rendition pHYs marker")
	}
}

func (s *WixCacheTestSuite) TestEnabledDerivesAndChangesBytes() {
	s.derive = true
	s.ResetLazyObjects()
	s.configure()

	// A large rendition first, so there is something to derive from.
	_, first := s.fetch(wixRGB + "/v1/fit/w_400,h_400/x.png")
	s.Equal("master", first, "the first request has nothing to derive from")

	// A smaller one the ancestor fully contains.
	derived, origin := s.fetch(wixRGB + "/v1/fit/w_100,h_100/x.png")
	s.Equal("derived", origin, "must be served from the cached rendition")

	// The same request again is an exact hit.
	again, origin2 := s.fetch(wixRGB + "/v1/fit/w_100,h_100/x.png")
	s.Equal("cache", origin2)
	s.Equal(derived, again, "an exact hit must be byte-identical")

	// And deriving genuinely changes the bytes: that is the point, not a
	// side effect. Render the same URL from the master and compare.
	s.derive = false
	s.ResetLazyObjects()
	s.configure()
	fromMaster, origin3 := s.fetch(wixRGB + "/v1/fit/w_100,h_100/x.png")
	s.Equal("master", origin3)

	s.NotEqual(fromMaster, derived,
		"deriving from a rendition must produce different bytes than rendering "+
			"from the master -- if these match, derivation is not actually happening")
}

func (s *WixCacheTestSuite) TestDerivedRenditionCarriesThePhysMarker() {
	s.derive = true
	s.ResetLazyObjects()
	s.configure()

	_, _ = s.fetch(wixRGB + "/v1/fit/w_400,h_400/x.png")
	derived, origin := s.fetch(wixRGB + "/v1/fit/w_100,h_100/x.png")
	s.Require().Equal("derived", origin)

	phys, ok := pngPhys(derived)
	s.Require().True(ok, "PNG output should carry pHYs")
	s.Equal(uint32(1000), phys,
		"a derived rendition reads pHYs 1000: the intermediate lost its "+
			"resolution in the write/re-read cycle (WIX-URL-SPEC §6.1)")
}

func (s *WixCacheTestSuite) TestNeverDerivesFromASmallerRendition() {
	s.derive = true
	s.ResetLazyObjects()
	s.configure()

	// Cache a small rendition, then ask for a larger one. Upsampling a
	// rendition is strictly worse than going back to the master.
	_, _ = s.fetch(wixRGB + "/v1/fit/w_80,h_80/x.png")
	_, origin := s.fetch(wixRGB + "/v1/fit/w_400,h_400/x.png")
	s.Equal("master", origin, "must fall back to the master rather than upsample")
}

func (s *WixCacheTestSuite) TestNeverDerivesFromASharpenedRendition() {
	s.derive = true
	s.ResetLazyObjects()
	s.configure()

	// A sharpened rendition has the effect baked into its pixels.
	_, _ = s.fetch(wixRGB + "/v1/fit/w_400,h_400,usm_0.66_1.00_0.01/x.png")
	_, origin := s.fetch(wixRGB + "/v1/fit/w_100,h_100/x.png")
	s.Equal("master", origin, "a sharpened ancestor is not a resamplable source")
}

func (s *WixCacheTestSuite) TestNeverDerivesAcrossDisjointCrops() {
	s.derive = true
	s.ResetLazyObjects()
	s.configure()

	// Cache a rendition of the left edge, then request the right edge at a
	// size the left-edge rendition is nominally "large enough" for. Without the
	// containment predicate this would silently return the wrong region.
	_, _ = s.fetch(wixRGB + "/v1/crop/x_0,y_0,w_200,h_200/x.png")
	_, origin := s.fetch(wixRGB + "/v1/crop/x_300,y_150,w_200,h_200/fill/w_50,h_50/x.png")
	s.Equal("master", origin,
		"an ancestor covering a disjoint region must never be used")
}

// exifXResolution reads XResolution out of a rendition's canonical eXIf block.
// IFD0 entry index 1 is XResolution, a RATIONAL whose value field is an offset.
func exifXResolution(b []byte) (num, den uint32, ok bool) {
	_, exif := pngChunks(b)
	if len(exif) < 100 {
		return 0, 0, false
	}
	off := binary.LittleEndian.Uint32(exif[10+1*12+8 : 10+1*12+12])
	if int(off)+8 > len(exif) {
		return 0, 0, false
	}
	return binary.LittleEndian.Uint32(exif[off : off+4]),
		binary.LittleEndian.Uint32(exif[off+4 : off+8]), true
}

func (s *WixCacheTestSuite) TestDerivedRenditionCarriesNoAncestorMetadata() {
	// The CDN's cached intermediates carry no metadata and no resolution. Ours
	// are complete PNGs, so without explicitly stripping them the ancestor's
	// metadata travels into the derived rendition -- and it does so invisibly:
	// pHYs keeps the master's value AND the canonical EXIF's XResolution gets
	// derived from the ancestor's eXIf instead of from this rendition's own
	// pHYs. Both are divergences from the CDN, not cosmetic differences.
	s.derive = true
	s.ResetLazyObjects()
	s.configure()

	master, o1 := s.fetch(wixRGB + "/v1/fit/w_400,h_400/x.png")
	derived, o2 := s.fetch(wixRGB + "/v1/fit/w_100,h_100/x.png")
	s.Require().Equal("master", o1)
	s.Require().Equal("derived", o2)

	mNum, mDen, ok := exifXResolution(master)
	s.Require().True(ok)
	dNum, dDen, ok := exifXResolution(derived)
	s.Require().True(ok)

	s.NotEqual(mNum, dNum,
		"the derived rendition must not inherit the ancestor's EXIF resolution")

	// With no master resolution to carry, §8.3 falls through to deriving it
	// from the rendition's own pHYs, TRUNCATED: int(1000 * 0.0254 * 1000).
	s.Equal(uint32(25400), dNum)
	s.Equal(uint32(1000), dDen)

	// And the master path is unaffected: 2834 px/m -> int(2834*0.0254*1000).
	s.Equal(uint32(71983), mNum)
	s.Equal(uint32(1000), mDen)
}

func TestWixCache(t *testing.T) {
	suite.Run(t, new(WixCacheTestSuite))
}
