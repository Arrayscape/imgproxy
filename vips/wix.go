package vips

/*
#include <stdlib.h>
#include "vips.h"
#include "wix.h"
*/
import "C"
import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unsafe"

	"github.com/imgproxy/imgproxy/v4/imagedata"
	"github.com/imgproxy/imgproxy/v4/imagetype"
)

// Primitives for the Wix CDN reproduction pipeline. See vips/wix.h for why
// these sit alongside the _go entry points rather than replacing them.

// WixICCTransform converts to the profile at the given path, which must be
// Wix's own sRGB rather than libvips' built-in. A master with no embedded
// profile passes through unchanged.
func (img *Image) WixICCTransform(profilePath string) error {
	cpath := C.CString(profilePath)
	defer C.free(unsafe.Pointer(cpath))

	var tmp *C.VipsImage
	if C.vips_icc_transform_wix(img.VipsImage, &tmp, cpath) != 0 {
		return Error()
	}
	img.swapAndUnref(tmp)
	return nil
}

func (img *Image) WixPremultiply() error {
	var tmp *C.VipsImage
	if C.vips_premultiply_wix(img.VipsImage, &tmp) != 0 {
		return Error()
	}
	img.swapAndUnref(tmp)
	return nil
}

func (img *Image) WixUnpremultiply() error {
	var tmp *C.VipsImage
	if C.vips_unpremultiply_wix(img.VipsImage, &tmp) != 0 {
		return Error()
	}
	img.swapAndUnref(tmp)
	return nil
}

func (img *Image) WixCopy() error {
	var tmp *C.VipsImage
	if C.vips_copy_wix(img.VipsImage, &tmp) != 0 {
		return Error()
	}
	img.swapAndUnref(tmp)
	return nil
}

// WixResize resamples by a single uniform scale with the lanczos3 kernel.
//
// The scale is passed at full double precision and must never have been
// round-tripped through a string: %.14f does not round-trip a double, and the
// resulting phase shift costs ±1 across the frame (OP-SPEC §4.6).
func (img *Image) WixResize(scale float64) error {
	var tmp *C.VipsImage
	if C.vips_resize_wix(img.VipsImage, &tmp, C.double(scale)) != 0 {
		return Error()
	}
	img.swapAndUnref(tmp)
	return nil
}

// WixResizeXY resizes with independent horizontal and vertical scales. Used
// only on the derivation path (OP-SPEC §10.1), where the target's two axes
// scale by different ratios because the cached ancestor rounded each one
// separately when it was itself produced.
func (img *Image) WixResizeXY(hscale, vscale float64) error {
	var tmp *C.VipsImage
	if C.vips_resize_wix_xy(img.VipsImage, &tmp, C.double(hscale), C.double(vscale)) != 0 {
		return Error()
	}
	img.swapAndUnref(tmp)
	return nil
}

// WixSharpen applies usm_S_A_T. Must run AFTER unpremultiply and after the
// final extract_area, on the uchar image.
func (img *Image) WixSharpen(sigma, x1, y2, y3, m1, m2 float64) error {
	var tmp *C.VipsImage
	if C.vips_sharpen_wix(img.VipsImage, &tmp,
		C.double(sigma), C.double(x1), C.double(y2),
		C.double(y3), C.double(m1), C.double(m2)) != 0 {
		return Error()
	}
	img.swapAndUnref(tmp)
	return nil
}

func (img *Image) WixGaussBlur(sigma float64) error {
	var tmp *C.VipsImage
	if C.vips_gaussblur_wix(img.VipsImage, &tmp, C.double(sigma)) != 0 {
		return Error()
	}
	img.swapAndUnref(tmp)
	return nil
}

// HasEmbeddedICC reports whether the decoded image carries an ICC profile.
// Only profiled masters are colour-transformed (OP-SPEC §3).
func (img *Image) HasEmbeddedICC() bool {
	return C.vips_has_embedded_icc(img.VipsImage) != 0
}

// WixResetResolution drops the image's resolution back to libvips' default, so
// PNG output carries pHYs 1000. See vips/wix.h.
func (img *Image) WixResetResolution() error {
	var tmp *C.VipsImage
	if C.vips_reset_resolution_wix(img.VipsImage, &tmp) != 0 {
		return Error()
	}
	img.swapAndUnref(tmp)
	return nil
}

// saveWix runs one of the _wix savers into a fresh memory target and wraps the
// result. Mirrors the bookkeeping in Image.Save.
func (img *Image) saveWix(
	imgtype imagetype.Type,
	save func(*C.VipsTarget) C.int,
) (imagedata.ImageData, error) {
	target := C.vips_target_new_to_memory()
	cancel := func() { C.vips_unref_target(target) }

	if save(target) != 0 {
		cancel()
		return nil, Error()
	}

	var imgsize C.size_t
	ptr := C.vips_blob_get(target.blob, &imgsize)
	b := unsafe.Slice((*byte)(ptr), int(imgsize))

	i := imagedata.NewFromBytesWithFormat(imgtype, b)
	i.AddCancel(cancel)
	return i, nil
}

// WixAutorot applies EXIF Orientation and clears the tag.
//
// OP-SPEC §4.0: the CDN rotates BEFORE geometry, so the source dimensions the
// geometry sees are the rotated ones. On a 3088x2316 master with Orientation 6
// asking for fit w_1372,h_1029, the stored size gives scale 0.44430 and a
// 1372x1029 box while the rotated size gives 0.33323 and 771x1029 -- which is
// what the CDN returns. It is format-independent.
func (img *Image) WixAutorot() error {
	var tmp *C.VipsImage
	if C.vips_autorot_wix(img.VipsImage, &tmp) != 0 {
		return Error()
	}
	img.swapAndUnref(tmp)
	return nil
}

// WixRGBA flattens the image to 8-bit RGBA and returns the raw pixels.
//
// Always four bands, so the AVIF encoder always receives an alpha plane --
// which is what prod emits on every rendition regardless of the master.
func (img *Image) WixRGBA() ([]byte, int, int, error) {
	var (
		ptr unsafe.Pointer
		n   C.size_t
	)
	if C.vips_rgba_wix(img.VipsImage, &ptr, &n) != 0 {
		return nil, 0, 0, Error()
	}
	defer C.g_free(C.gpointer(ptr))

	return C.GoBytes(ptr, C.int(n)), img.Width(), img.Height(), nil
}

// WixPixelsPerMetre reports the image resolution the way a PNG pHYs chunk
// encodes it. WebP carries no pHYs, so the §8.3 resolution precedence reads it
// from the image instead.
func (img *Image) WixPixelsPerMetre() uint32 {
	xres := float64(C.vips_xres_wix(img.VipsImage)) // pixels per mm
	if xres <= 0 {
		return 0
	}
	return uint32(xres*1000 + 0.5)
}

// WixSavePNG encodes with libvips' own defaults. The container fix-ups in
// OP-SPEC §8 are applied afterwards, on the encoded bytes.
func (img *Image) WixSavePNG() (imagedata.ImageData, error) {
	return img.saveWix(imagetype.PNG, func(t *C.VipsTarget) C.int {
		return C.vips_pngsave_wix(img.VipsImage, t)
	})
}

// WixSaveWebP encodes VP8 (lossy) or VP8L (lossless), chosen from the STORED
// master's own nature rather than the request. On the lossless path Q is fixed
// at 75 with a near-lossless level of 80 and the URL's q_N does not apply.
func (img *Image) WixSaveWebP(q int, lossless, nearLossless bool, nlLevel int) (imagedata.ImageData, error) {
	return img.saveWix(imagetype.WEBP, func(t *C.VipsTarget) C.int {
		return C.vips_webpsave_wix(img.VipsImage, t, C.int(q),
			cbool(lossless), cbool(nearLossless), C.int(nlLevel))
	})
}

func (img *Image) WixSaveAVIF(q, effort int) (imagedata.ImageData, error) {
	return img.saveWix(imagetype.AVIF, func(t *C.VipsTarget) C.int {
		return C.vips_avifsave_wix(img.VipsImage, t, C.int(q), C.int(effort))
	})
}

func (img *Image) WixSaveJPEG(q int) (imagedata.ImageData, error) {
	return img.saveWix(imagetype.JPEG, func(t *C.VipsTarget) C.int {
		return C.vips_jpegsave_wix(img.VipsImage, t, C.int(q))
	})
}

func cbool(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

// HasNearLosslessLevel reports whether the linked libvips carries patch 0002.
// Without it the lossless WebP path cannot express near_lossless=80 with Q=75
// and falls back to plain --lossless, which is not byte-exact.
func HasNearLosslessLevel() bool {
	return C.vips_has_near_lossless_level() != 0
}

// CheckWixEnvironment enforces OP-SPEC §1 and §2 when the Wix route is enabled.
//
// Anything that would silently change the transform is a hard error, not a
// warning. A wrong build still serves images and still looks healthy -- it just
// renders different bytes -- so the only safe failure mode is refusing to start.
//
// allowUnverified downgrades the libvips build checks to warnings, for running
// on an unmodified upstream base and accepting non-exact output.
func CheckWixEnvironment(allowUnverified bool) (warnings []string, err error) {
	// §2.1 -- no escape hatch: disabling the vector path is one of the two
	// settings that costs roughly 5% of the corpus, and it is never wanted.
	// Measured: 6 of 12 renditions change.
	if _, ok := os.LookupEnv("VIPS_NOVECTOR"); ok {
		return nil, errors.New(
			"VIPS_NOVECTOR is set, which disables the ORC vector path and silently " +
				"changes the transform; unset it (OP-SPEC §2.1)")
	}

	// VIPS_TILE_HEIGHT is deliberately NOT checked. It only mattered under
	// sequential access; the pipeline now opens images with access=random,
	// which removes reducev's line cache entirely (OP-SPEC §2).

	var problems []string

	// §1 pins stock 8.15.5. Other versions resample differently -- reducev was
	// re-vectorised after 8.14 and again later, and the difference shows on any
	// image that varies vertically, which is every photograph.
	if maj, min, mic := WixLibvipsVersion(); maj != 8 || min != 15 {
		problems = append(problems, fmt.Sprintf(
			"libvips is %d.%d.%d, but OP-SPEC §1 pins 8.15.5", maj, min, mic))
	}

	// The one remaining patch is directly detectable.
	if !HasNearLosslessLevel() {
		problems = append(problems,
			"libvips lacks the near_lossless_level property, so the OP-SPEC §1 "+
				"webpsave patch is not applied and lossless WebP cannot be expressed")
	}

	if len(problems) > 0 {
		msg := "the Wix route needs the libvips built by docker/wix/build-base.sh: " +
			strings.Join(problems, "; ")
		if !allowUnverified {
			return nil, errors.New(msg +
				". Refusing to start: this build would serve images that silently do " +
				"not reproduce the CDN. Set IMGPROXY_WIX_ALLOW_UNVERIFIED_LIBVIPS=true " +
				"to run anyway with non-exact output")
		}
		warnings = append(warnings, msg+
			" -- running anyway because IMGPROXY_WIX_ALLOW_UNVERIFIED_LIBVIPS is set; "+
			"output will NOT match the CDN")
	}

	return warnings, nil
}

// WixLibvipsVersion reports the version of the libvips actually linked in.
func WixLibvipsVersion() (major, minor, micro int) {
	return int(C.vips_major_wix()), int(C.vips_minor_wix()), int(C.vips_micro_wix())
}
