package wix

import (
	"fmt"
	"io"

	"github.com/imgproxy/imgproxy/v4/imagedata"
	"github.com/imgproxy/imgproxy/v4/imagetype"
	"github.com/imgproxy/imgproxy/v4/vips"
	wixspec "github.com/imgproxy/imgproxy/v4/wix"
)

// Encode writes the rendered image in the negotiated format. OP-SPEC §7.
//
// format comes from wix.Negotiate; src decides the WebP codec, because that
// follows the STORED master's own nature rather than the request.
func Encode(
	img *vips.Image,
	src *Source,
	format imagetype.Type,
	enc wixspec.Encoding,
	avifEffort int,
) (imagedata.ImageData, error) {
	switch format {
	case imagetype.PNG:
		return encodePNG(img, src)

	case imagetype.WEBP:
		// The codec follows the stored master, not alpha presence and not
		// encode-both-and-keep-the-smaller. WIX-URL-SPEC §5.
		if src.Lossy {
			// §7.2 -- stock defaults otherwise: effort 4, no preset, no smart
			// subsample.
			if err := stripForLossy(img); err != nil {
				return nil, err
			}
			return img.WixSaveWebP(enc.Quality, false, false, -1)
		}
		// §7.3 -- Q is fixed at 75 with a near-lossless level of 80. The URL's
		// q_N does NOT apply on this path. Output is near-lossless rather than
		// strictly lossless: pixels shift by at most 1.
		if err := stripForLossy(img); err != nil {
			return nil, err
		}
		return img.WixSaveWebP(
			wixspec.LosslessWebPQuality, true, true, wixspec.LosslessWebPNearLosslessLvl)

	case imagetype.AVIF:
		// UNVERIFIED encoder settings; the §4 negotiation that selects it is
		// exact. See WIXEMU.md.
		if err := stripForLossy(img); err != nil {
			return nil, err
		}
		return img.WixSaveAVIF(enc.Quality, avifEffort)

	case imagetype.JPEG:
		// Never scored against the CDN.
		if err := stripForLossy(img); err != nil {
			return nil, err
		}
		return img.WixSaveJPEG(enc.Quality)
	}

	return nil, fmt.Errorf("wix: cannot encode to %s", format)
}

// encodePNG saves with libvips' own defaults, then applies the container
// fix-ups. The fix-ups need the MASTER's bytes, not just the encoded output:
// the resolution precedence reads the master's EXIF and detects a metric JFIF
// block, neither of which survives into the rendition.
func encodePNG(img *vips.Image, src *Source) (imagedata.ImageData, error) {
	// Deliberately NOT img.Strip(): vips_strip rewrites xres/yres to 72/25.4,
	// which forces pHYs 2835 on every output and breaks the resolution
	// derivation. FinalizePNG owns PNG metadata, and it strips far more
	// aggressively than Strip does.
	d, err := img.WixSavePNG()
	if err != nil {
		return nil, fmt.Errorf("wix: pngsave: %w", err)
	}
	defer d.Close()

	raw, err := io.ReadAll(d.Reader())
	if err != nil {
		return nil, fmt.Errorf("wix: reading encoded png: %w", err)
	}

	fixed, err := wixspec.FinalizePNG(raw, src.Head)
	if err != nil {
		return nil, fmt.Errorf("wix: png finalize: %w", err)
	}

	return imagedata.NewFromBytesWithFormat(imagetype.PNG, fixed), nil
}

// stripForLossy drops EXIF, XMP and IPTC before a non-PNG encode, keeping the
// ICC profile. Transformed renditions carry no camera metadata (§7.1), and PNG
// is exempt only because FinalizePNG handles it more precisely.
func stripForLossy(img *vips.Image) error {
	if err := img.Strip(false); err != nil {
		return fmt.Errorf("wix: strip: %w", err)
	}
	return nil
}
