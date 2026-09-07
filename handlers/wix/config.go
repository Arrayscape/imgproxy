package wix

import (
	"errors"
	"fmt"
	"strings"

	"github.com/imgproxy/imgproxy/v4/ensure"
	"github.com/imgproxy/imgproxy/v4/env"
)

var (
	IMGPROXY_WIX_ENABLED     = env.Bool("IMGPROXY_WIX_ENABLED")
	IMGPROXY_WIX_PATH_PREFIX = env.String("IMGPROXY_WIX_PATH_PREFIX")
	IMGPROXY_WIX_SOURCE_URL  = env.String("IMGPROXY_WIX_SOURCE_URL_TEMPLATE")
	IMGPROXY_WIX_SRGB        = env.String("IMGPROXY_WIX_SRGB_PROFILE")
	IMGPROXY_WIX_AVIF        = env.Bool("IMGPROXY_WIX_AVIF")
	IMGPROXY_WIX_AVIF_SPEED  = env.Int("IMGPROXY_WIX_AVIF_SPEED")
	IMGPROXY_WIX_SIG_MODE    = env.String("IMGPROXY_WIX_SIGNATURE_MODE")
	IMGPROXY_WIX_ALLOW_TH    = env.Bool("IMGPROXY_WIX_ALLOW_WRONG_TILE_HEIGHT")
	IMGPROXY_WIX_ALLOW_VIPS  = env.Bool("IMGPROXY_WIX_ALLOW_UNVERIFIED_LIBVIPS")
)

// Signature modes.
const (
	// SignatureOff serves unsigned URLs, which is what the CDN does.
	SignatureOff = "off"

	// SignatureQuery requires ?sig=<base64url> over the canonical path.
	//
	// A query parameter rather than a leading path segment, because a path
	// signature would break the URL shape -- and keeping the CDN's URL shape is
	// the entire point of this feature. Query params affect CDN caching only,
	// never output.
	SignatureQuery = "query"
)

type Config struct {
	// Enabled turns the /media route on. Off by default: this is an alternate
	// URL grammar, not a change to imgproxy's own.
	Enabled bool

	// PathPrefix is where the Wix grammar is mounted.
	PathPrefix string

	// SourceURLTemplate maps a media id to something imgproxy's fetcher
	// understands. Exactly one %s. How a media id becomes bytes is deployment
	// configuration and affects no transform rule (OP-SPEC §9).
	SourceURLTemplate string

	// SRGBProfile is where the Wix sRGB ICC profile lives. When absent or
	// unreadable the embedded copy is written to a temp file instead.
	SRGBProfile string

	// AVIF enables the AVIF branch of format negotiation. The negotiation is
	// exact; the AVIF encoder settings were never compared against the CDN.
	AVIF bool

	// AVIFSpeed maps to effort = 9 - speed, as imgproxy's own encoder does.
	AVIFSpeed int

	SignatureMode string

	// AllowWrongTileHeight permits a VIPS_TILE_HEIGHT other than 16. Running
	// with the wrong value silently produces a different transform, so this
	// must be set deliberately.
	AllowWrongTileHeight bool

	// AllowUnverifiedLibvips permits starting on a libvips that is not the
	// patched 8.15.5 the transform is defined against -- for instance an
	// unmodified upstream imgproxy base. Output will not reproduce the CDN.
	AllowUnverifiedLibvips bool
}

func NewDefaultConfig() Config {
	return Config{
		Enabled:           false,
		PathPrefix:        "/media",
		SourceURLTemplate: "",
		SRGBProfile:       "/opt/imgproxy/share/wix-srgb.icc",
		AVIF:              true,
		AVIFSpeed:         8,
		SignatureMode:     SignatureOff,
	}
}

func LoadConfigFromEnv(c *Config) (*Config, error) {
	c = ensure.Ensure(c, NewDefaultConfig)

	return c, errors.Join(
		IMGPROXY_WIX_ENABLED.Parse(&c.Enabled),
		IMGPROXY_WIX_PATH_PREFIX.Parse(&c.PathPrefix),
		IMGPROXY_WIX_SOURCE_URL.Parse(&c.SourceURLTemplate),
		IMGPROXY_WIX_SRGB.Parse(&c.SRGBProfile),
		IMGPROXY_WIX_AVIF.Parse(&c.AVIF),
		IMGPROXY_WIX_AVIF_SPEED.Parse(&c.AVIFSpeed),
		IMGPROXY_WIX_SIG_MODE.Parse(&c.SignatureMode),
		IMGPROXY_WIX_ALLOW_TH.Parse(&c.AllowWrongTileHeight),
		IMGPROXY_WIX_ALLOW_VIPS.Parse(&c.AllowUnverifiedLibvips),
	)
}

func (c *Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if !strings.HasPrefix(c.PathPrefix, "/") {
		return fmt.Errorf("IMGPROXY_WIX_PATH_PREFIX must start with a slash, got %q", c.PathPrefix)
	}
	if c.SourceURLTemplate == "" {
		return errors.New(
			"IMGPROXY_WIX_SOURCE_URL_TEMPLATE is required when the Wix route is enabled, " +
				"e.g. local:///%s or s3://bucket/masters/%s")
	}
	if strings.Count(c.SourceURLTemplate, "%s") != 1 {
		return fmt.Errorf(
			"IMGPROXY_WIX_SOURCE_URL_TEMPLATE must contain exactly one %%s, got %q",
			c.SourceURLTemplate)
	}
	switch c.SignatureMode {
	case SignatureOff, SignatureQuery:
	default:
		return fmt.Errorf(
			"IMGPROXY_WIX_SIGNATURE_MODE must be %q or %q, got %q",
			SignatureOff, SignatureQuery, c.SignatureMode)
	}
	if c.AVIFSpeed < 0 || c.AVIFSpeed > 9 {
		return fmt.Errorf("IMGPROXY_WIX_AVIF_SPEED must be 0-9, got %d", c.AVIFSpeed)
	}
	return nil
}
