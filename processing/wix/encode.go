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
		return encodeWebP(img, src, enc)

	case imagetype.AVIF:
		// UNVERIFIED encoder settings; the §4 negotiation that selects it is
		// exact. See WIXEMU.md.
		if err := stripForLossy(img); err != nil {
			return nil, err
		}
		return img.WixSaveAVIF(enc.Quality, avifEffort)

	case imagetype.JPEG:
		return encodeJPEG(img, src, enc)
	}

	// Everything else -- TIFF, GIF, BMP, ICO, JXL. §4 says output falls back to
	// the STORED master's format when `enc` is absent, and a master can be any
	// format the decoder handles: WIX-URL-SPEC §1.1's png/jpeg/webp split
	// describes one site's 366 masters, not a limit on the CDN.
	//
	// There is no measured CDN behaviour for these, so rather than invent
	// encoder settings they go through imgproxy's own saver with its defaults.
	// Documented as pass-through in WIXEMU.md.
	if !vips.SupportsSave(format) {
		// A vector or otherwise unsaveable master still has to produce
		// something; PNG is the lossless default the rest of the pipeline
		// already targets.
		format = imagetype.PNG
		return encodePNG(img, src)
	}
	if err := stripForLossy(img); err != nil {
		return nil, err
	}
	return img.Save(format, enc.Quality, nil)
}

// encodeWebP saves and then applies the §8.4 container fix-ups.
//
// The codec follows the STORED master, not alpha presence and not
// encode-both-and-keep-the-smaller (WIX-URL-SPEC §5). The fix-ups afterwards
// are not cosmetic: the coded payload already matches, and they are what makes
// the FILE match -- a payload-only comparison called WebP finished while 22 of
// 54 files still differed.
func encodeWebP(img *vips.Image, src *Source, enc wixspec.Encoding) (imagedata.ImageData, error) {
	// Read the resolution before Strip rewrites it: WebP has no pHYs chunk, so
	// §8.3 step 3 has nothing to read it back from.
	pxPerM := img.WixPixelsPerMetre()

	if err := stripForLossy(img); err != nil {
		return nil, err
	}

	var (
		d   imagedata.ImageData
		err error
	)
	if src.Lossy {
		// §7.2 -- stock defaults otherwise: effort 4, no preset, no smart
		// subsample.
		d, err = img.WixSaveWebP(enc.Quality, false, false, -1)
	} else {
		// §7.3 -- Q is fixed at 75 with a near-lossless level of 80. The URL's
		// q_N does NOT apply here. Output is near-lossless rather than strictly
		// lossless: pixels shift by at most 1.
		d, err = img.WixSaveWebP(
			wixspec.LosslessWebPQuality, true, true, wixspec.LosslessWebPNearLosslessLvl)
	}
	if err != nil {
		return nil, fmt.Errorf("wix: webpsave: %w", err)
	}
	defer d.Close()

	raw, err := io.ReadAll(d.Reader())
	if err != nil {
		return nil, fmt.Errorf("wix: reading encoded webp: %w", err)
	}

	fixed, err := wixspec.FinalizeWebP(raw, src.Head, pxPerM)
	if err != nil {
		return nil, fmt.Errorf("wix: webp finalize: %w", err)
	}

	return imagedata.NewFromBytesWithFormat(imagetype.WEBP, fixed), nil
}

// encodeJPEG saves and then applies the §8.5 container fix-ups.
//
// Quality follows §7.4's rule, which is deliberately NOT §7.2's WebP rule.
func encodeJPEG(img *vips.Image, src *Source, enc wixspec.Encoding) (imagedata.ImageData, error) {
	w, h := img.Width(), img.Height()

	if err := stripForLossy(img); err != nil {
		return nil, err
	}

	d, err := img.WixSaveJPEG(enc.JPEGQuality())
	if err != nil {
		return nil, fmt.Errorf("wix: jpegsave: %w", err)
	}
	defer d.Close()

	raw, err := io.ReadAll(d.Reader())
	if err != nil {
		return nil, fmt.Errorf("wix: reading encoded jpeg: %w", err)
	}

	fixed, err := wixspec.FinalizeJPEG(raw, src.Head, w, h)
	if err != nil {
		return nil, fmt.Errorf("wix: jpeg finalize: %w", err)
	}

	return imagedata.NewFromBytesWithFormat(imagetype.JPEG, fixed), nil
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
