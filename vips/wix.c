#include "wix.h"

int
vips_icc_transform_wix(VipsImage *in, VipsImage **out, const char *profile)
{
  // OP-SPEC §3: skip entirely when the master has no embedded profile.
  if (!vips_image_get_typeof(in, VIPS_META_ICC_NAME))
    return vips_copy(in, out, NULL);

  return vips_icc_transform(in, out, profile, "embedded", TRUE, NULL);
}

int
vips_premultiply_wix(VipsImage *in, VipsImage **out)
{
  return vips_premultiply(in, out, NULL);
}

int
vips_unpremultiply_wix(VipsImage *in, VipsImage **out)
{
  return vips_unpremultiply(in, out, NULL);
}

int
vips_copy_wix(VipsImage *in, VipsImage **out)
{
  return vips_copy(in, out, NULL);
}

int
vips_resize_wix(VipsImage *in, VipsImage **out, double scale)
{
  return vips_resize(in, out, scale, "kernel", VIPS_KERNEL_LANCZOS3, NULL);
}

int
vips_resize_wix_xy(VipsImage *in, VipsImage **out, double hscale, double vscale)
{
  return vips_resize(in, out, hscale,
      "vscale", vscale, "kernel", VIPS_KERNEL_LANCZOS3, NULL);
}

int
vips_sharpen_wix(VipsImage *in, VipsImage **out,
    double sigma, double x1, double y2, double y3, double m1, double m2)
{
  return vips_sharpen(in, out,
      "sigma", sigma,
      "x1", x1,
      "y2", y2,
      "y3", y3,
      "m1", m1,
      "m2", m2,
      NULL);
}

int
vips_gaussblur_wix(VipsImage *in, VipsImage **out, double sigma)
{
  return vips_gaussblur(in, out, sigma, NULL);
}

int
vips_reset_resolution_wix(VipsImage *in, VipsImage **out)
{
  return vips_copy(in, out, "xres", 1.0, "yres", 1.0, NULL);
}

int
vips_autorot_wix(VipsImage *in, VipsImage **out)
{
  if (vips_autorot(in, out, NULL))
    return 1;

  // autorot leaves the tag behind; strip it so a later autorot (or a saver
  // that honours it) cannot rotate a second time.
  vips_autorot_remove_angle(*out);
  return 0;
}

int
vips_rgba_wix(VipsImage *in, void **out, size_t *len)
{
  VipsImage *base = vips_image_new();
  VipsImage **t = (VipsImage **) vips_object_local_array(VIPS_OBJECT(base), 4);
  VipsImage *cur = in;

  if (cur->Type != VIPS_INTERPRETATION_sRGB) {
    if (vips_colourspace(cur, &t[0], VIPS_INTERPRETATION_sRGB, NULL)) {
      g_object_unref(base);
      return 1;
    }
    cur = t[0];
  }
  if (!vips_image_hasalpha(cur)) {
    if (vips_addalpha(cur, &t[1], NULL)) {
      g_object_unref(base);
      return 1;
    }
    cur = t[1];
  }
  if (cur->BandFmt != VIPS_FORMAT_UCHAR) {
    if (vips_cast(cur, &t[2], VIPS_FORMAT_UCHAR, NULL)) {
      g_object_unref(base);
      return 1;
    }
    cur = t[2];
  }

  *out = vips_image_write_to_memory(cur, len);
  g_object_unref(base);
  return *out == NULL;
}

double
vips_xres_wix(VipsImage *in)
{
  return vips_image_get_xres(in);
}

int
vips_pngsave_wix(VipsImage *in, VipsTarget *target)
{
  // Every property at its libvips default: compression 6, filter NONE,
  // interlace FALSE, palette FALSE, keep ALL. vips_pngsave_go forces
  // FILTER_ALL and auto-quantizes any source that arrived with a palette;
  // both change the IDAT stream.
  return vips_pngsave_target(in, target, NULL);
}

int
vips_webpsave_wix(VipsImage *in, VipsTarget *target,
    int Q, int lossless, int near_lossless, int near_lossless_level)
{
  // §7.2 lossy: stock defaults otherwise -- effort 4, no preset, no smart
  // subsample. libwebp 1.2.2 through 1.5.0 produce identical payloads.
  if (!lossless)
    return vips_webpsave_target(in, target, "Q", Q, NULL);

  // §7.3 lossless: needs patch 0002. libvips drives near_lossless from Q, so
  // near_lossless=80 with quality=75 cannot otherwise be expressed.
  return vips_webpsave_target(in, target,
      "Q", Q,
      "lossless", TRUE,
      "near_lossless", near_lossless ? TRUE : FALSE,
      "near_lossless_level", near_lossless_level,
      NULL);
}

int
vips_avifsave_wix(VipsImage *in, VipsTarget *target, int Q, int effort)
{
  // UNVERIFIED against the CDN: negotiation is understood, encoder settings
  // were never compared. OP-SPEC §11.
  return vips_heifsave_target(in, target,
      "Q", Q,
      "bitdepth", 8,
      "compression", VIPS_FOREIGN_HEIF_COMPRESSION_AV1,
      "effort", effort,
      NULL);
}

int
vips_jpegsave_wix(VipsImage *in, VipsTarget *target, int Q)
{
  // optimize_coding is deliberately absent: libjpeg optimises progressive
  // scans anyway, so setting it changes nothing.
  return vips_jpegsave_target(in, target,
      "Q", Q,
      "interlace", TRUE,
      "subsample_mode", VIPS_FOREIGN_SUBSAMPLE_OFF,
      NULL);
}

int
vips_has_near_lossless_level(void)
{
  GType t = g_type_from_name("VipsForeignSaveWebpTarget");
  if (!t)
    return 0;

  GObjectClass *c = g_type_class_ref(t);
  if (!c)
    return 0;

  int found = g_object_class_find_property(c, "near_lossless_level") != NULL;
  g_type_class_unref(c);
  return found;
}

int
vips_major_wix(void)
{
  return vips_version(0);
}

int
vips_minor_wix(void)
{
  return vips_version(1);
}

int
vips_micro_wix(void)
{
  return vips_version(2);
}

int
vips_is_sequential_wix(VipsImage *in)
{
  return vips_image_get_typeof(in, VIPS_META_SEQUENTIAL) != 0;
}
