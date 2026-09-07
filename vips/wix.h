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

// Encoders. libvips/CLI defaults only -- see the note on vips_pngsave_go above.
int vips_pngsave_wix(VipsImage *in, VipsTarget *target);
int vips_webpsave_wix(VipsImage *in, VipsTarget *target,
    int Q, int lossless, int near_lossless, int near_lossless_level);
int vips_avifsave_wix(VipsImage *in, VipsTarget *target, int Q, int effort);
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
