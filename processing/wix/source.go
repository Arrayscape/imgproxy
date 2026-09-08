// Package wix renders images the way the Wix CDN does.
//
// It is a separate pipeline from processing.Processor, not a variation on it.
// The two disagree on arithmetic that cannot be reconciled by configuration:
// processing/prepare.go rounds half-away-from-zero where Wix floors, its
// scale-on-load step shrinks JPEG and WebP before the resample, and
// vips_resize_go casts premultiplied data back to uchar before resizing. Each
// of those changes the output. See OP-SPEC.md §5.
package wix

import (
	"fmt"
	"io"

	"github.com/imgproxy/imgproxy/v4/imagedata"
	"github.com/imgproxy/imgproxy/v4/imagetype"
	"github.com/imgproxy/imgproxy/v4/vips"
	wixspec "github.com/imgproxy/imgproxy/v4/wix"
)

// Source is a decoded master plus everything sniffed from its bytes.
type Source struct {
	// Head is the first HeadSize bytes, kept because the PNG resolution rules
	// read the master's own EXIF and JFIF blocks, neither of which survives
	// into the rendition.
	Head []byte

	// Format is SNIFFED from magic bytes. The media-id extension records the
	// original upload format and lies about what is stored.
	Format imagetype.Type

	// Lossy drives the WebP codec choice: a lossless master (PNG) encodes to
	// VP8L, a lossy one (JPEG, WebP) to VP8.
	Lossy bool

	Width, Height int

	// HasAlpha is read once from the decoded image. libvips expands a palette
	// PNG carrying tRNS to RGBA, so this is already true for exactly the case
	// OP-SPEC §5.3 calls out -- and it does NOT depend on whether any pixel is
	// actually transparent.
	HasAlpha bool

	HasICC bool
}

// NewSource reads the head of the master and decodes it into img.
//
// Two load rules, both load-bearing:
//
// Shrink is always 1. imgproxy's scaleOnLoad step would shrink JPEG and WebP
// through the decoder before the resample, changing the pixels the resampler
// sees.
//
// Access is RANDOM, not imgproxy's usual SEQUENTIAL. Under sequential access
// reducev sits behind a line cache whose strip height decides which output rows
// land on a phase tie, making the result depend both on that height and on what
// consumes the resize -- a sharpen after it moves the boundaries and changes
// the output. Random access removes the cache and is byte-exact on both the
// plain and the sharpened path. OP-SPEC.md §2.
func NewSource(img *vips.Image, data imagedata.ImageData) (*Source, error) {
	r := data.Reader()
	head := make([]byte, wixspec.HeadSize)
	n, err := io.ReadFull(r, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, fmt.Errorf("wix: cannot read master head: %w", err)
	}
	head = head[:n]

	if err := vips.WithRandomAccess(func() error {
		return img.Load(data, 1.0, 0, 1)
	}); err != nil {
		return nil, fmt.Errorf("wix: cannot decode master: %w", err)
	}

	return &Source{
		Head:     head,
		Format:   wixspec.StoredFormat(head),
		Lossy:    wixspec.StoredLossy(head),
		Width:    img.Width(),
		Height:   img.Height(),
		HasAlpha: img.HasAlpha(),
		HasICC:   img.HasEmbeddedICC(),
	}, nil
}
