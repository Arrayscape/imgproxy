#ifndef IMGPROXY_VIPS_WIX_H
#define IMGPROXY_VIPS_WIX_H

#include <vips/vips.h>

// Primitives for the Wix CDN reproduction pipeline (OP-SPEC.md §5).
//
// These exist alongside the _go entry points rather than replacing them. Every
// _go function sits on the native imgproxy pipeline's output path, and the two
// places Wix diverges -- vips_resize_go's intermediate cast back to uchar, and
// vips_pngsave_go's FILTER_ALL plus palette auto-quantize -- are deliberate
// imgproxy choices. Changing them would alter upstream's golden output and
// every deployed cache for no benefit here.

// Transform to a SUPPLIED profile.
//
// Unlike vips_icc_transform_standard this does not hard-code "sRGB"/"sGrey",
// does not short-circuit sRGB-IEC61966, and does not override pcs or depth --
// the reference passes only --embedded, so the property defaults must stand.
// Copies through unchanged when the master carries no profile (OP-SPEC §3).
int vips_icc_transform_wix(VipsImage *in, VipsImage **out, const char *profile);

// Alpha sequencing is the CALLER's job: the four branches in OP-SPEC §5 place
// premultiply, the drop extract, the resize and unpremultiply differently.
int vips_premultiply_wix(VipsImage *in, VipsImage **out);
int vips_unpremultiply_wix(VipsImage *in, VipsImage **out);
int vips_copy_wix(VipsImage *in, VipsImage **out);

// Single uniform scale, lanczos3 named explicitly. No "vscale": the measured
// cases are all uniform. No premultiply round trip inside -- that is what
// vips_resize_go does, and its intermediate cast to uchar destroys exactly the
// precision §5.3 depends on.
int vips_resize_wix(VipsImage *in, VipsImage **out, double scale);

// Independent horizontal and vertical scales, for deriving a rendition from a
// cached one (OP-SPEC section 10.1). A cached level is aspect-preserved and
// rounds each axis separately, so the two ratios to a target are not equal --
// on a 1032x24 master the 925 level is 925x21 -- and forcing one scale on both
// axes stretches the image.
int vips_resize_wix_xy(VipsImage *in, VipsImage **out, double hscale, double vscale);

// usm_S_A_T -> --sigma S --m2 A --x1 (T*100) --y2 10 --y3 20 --m1 0.
// vips_apply_filters passes only "sigma" and leaves the rest at libvips
// defaults, which is a different sharpen.
int vips_sharpen_wix(VipsImage *in, VipsImage **out,
    double sigma, double x1, double y2, double y3, double m1, double m2);

int vips_gaussblur_wix(VipsImage *in, VipsImage **out, double sigma);

// Reset the image resolution to libvips' default (1 pixel/mm), which pngsave
// writes as pHYs 1000. Models the resolution being lost in an intermediate's
// write/re-read cycle -- the marker that distinguishes a derived rendition
// from a master render (WIX-URL-SPEC §6.1).
int vips_reset_resolution_wix(VipsImage *in, VipsImage **out);

// Apply EXIF Orientation, and remove the tag so it cannot be applied twice.
// OP-SPEC §4.0: this must run before ANY dimension is read -- sw x sh are the
// rotated dimensions, so getting it wrong computes a different scale and a
// different output box, not merely a rotated image.
int vips_autorot_wix(VipsImage *in, VipsImage **out);

// Flatten to 8-bit RGBA in memory, for handing to the AVIF encoder.
//
// Always four bands: OP-SPEC §7.5's encoder takes RGBA unconditionally, and
// prod emits an alpha item on EVERY rendition including opaque JPEG masters --
// a consequence of avifEncoderAddImage's flags, not of the input having alpha.
// The caller frees *out with g_free.
int vips_rgba_wix(VipsImage *in, void **out, size_t *len);

// Image resolution in pixels/mm, as pngsave would encode into pHYs. WebP has
// no pHYs chunk, so the §8.3 resolution precedence needs it from the image.
double vips_xres_wix(VipsImage *in);

// Encoders. libvips/CLI defaults only -- see the note on vips_pngsave_go above.
int vips_pngsave_wix(VipsImage *in, VipsTarget *target);
int vips_webpsave_wix(VipsImage *in, VipsTarget *target,
    int Q, int lossless, int near_lossless, int near_lossless_level);
int vips_avifsave_wix(VipsImage *in, VipsTarget *target, int Q, int effort);
// §7.4: progressive with libjpeg's own ten-scan jpeg_simple_progression, and
// 4:4:4 at every quality. subsample-mode off is load-bearing -- libvips' `auto`
// subsamples below Q 90 and the CDN never does. Annex K tables scaled by
// libjpeg's quality formula, so the DQT identifies the encoder as well as the
// quality: this must be stock libjpeg-turbo, not mozjpeg.
int vips_jpegsave_wix(VipsImage *in, VipsTarget *target, int Q);

// Runtime assertion that docker/wix/0002 is present in the linked libvips.
int vips_has_near_lossless_level(void);

// Linked libvips version, for the OP-SPEC §1 pin check.
int vips_major_wix(void);
int vips_minor_wix(void);
int vips_micro_wix(void);

// Has the pipeline been materialised? Materialising drops VIPS_META_SEQUENTIAL,
// which is what makes patched reducev install the VIPS_TILE_HEIGHT-sized line
// cache. Losing it silently changes the transform.
int vips_is_sequential_wix(VipsImage *in);

#endif
