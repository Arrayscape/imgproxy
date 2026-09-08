#include <vips/vips.h>

typedef struct _ImgproxyLoadOptions {
  // Access mode. SEQUENTIAL by default, which is what the streaming pipeline
  // wants. RANDOM is required by the Wix reproduction: reducev puts a
  // sequential input behind a line cache whose strip height changes which
  // output rows land on a phase tie, so under SEQUENTIAL the result depends on
  // the strip height AND on what consumes the resize. See OP-SPEC.md §2.
  VipsAccess Access;

  double Shrink;      // Shrink-on-load factor. 1.0 means no shrinking.
  gboolean Thumbnail; // Whether to load thumbnail (for heif).

  int Page;  // Page number to load (for multi-page images).
  int Pages; // Number of pages to load (for multi-page images).

  gboolean PngUnlimited;  // Whether to disable vips_pngload limits.
  gboolean SvgUnlimited;  // Whether to disable vips_svgload limits.
  gboolean TiffUnlimited; // Whether to disable vips_tiffload limits.
} ImgproxyLoadOptions;

typedef struct _ImgproxySaveOptions {
  gboolean JpegProgressive; // Whether to save JPEG as progressive.

  gboolean PngInterlaced;    // Whether to save PNG as interlaced.
  gboolean PngQuantize;      // Whether to quantize PNG (save with palette).
  int PngQuantizationColors; // Number of colors to use in PNG quantization.

  VipsForeignWebpPreset WebpPreset; // WebP preset to use.
  int WebpEffort;                   // WebP encoding effort level.

  int AvifSpeed; // AVIF encoding speed.

  int JxlEffort; // JPEG XL encoding effort.
} ImgproxySaveOptions;
