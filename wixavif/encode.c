#include "encode.h"
#include <string.h>
#include <avif/avif.h>

int
wixavif_encode(
    const uint8_t *rgba, int width, int height,
    int min_q, int max_q, int min_alpha_q, int max_alpha_q,
    int speed, int threads,
    uint8_t **out, size_t *out_len, const char **err)
{
  *out = NULL;
  *out_len = 0;
  *err = NULL;

  // 8-bit 4:2:0, FULL range, and no colour description at all: the CDN's
  // sequence headers carry CICP 2/2/2 (unspecified) and its `colr` box is nclx
  // 2/2/2 full-range. There is no ICC in the file even when the master had one
  // -- the profile is consumed by the icc_transform earlier in the pipeline
  // (OP-SPEC §3), not carried into the rendition.
  avifImage *image = avifImageCreate(width, height, 8, AVIF_PIXEL_FORMAT_YUV420);
  if (!image) {
    *err = "avifImageCreate failed";
    return 1;
  }
  image->yuvRange = AVIF_RANGE_FULL;

  avifRGBImage rgb;
  avifRGBImageSetDefaults(&rgb, image);
  rgb.format = AVIF_RGB_FORMAT_RGBA;
  rgb.depth = 8;
  rgb.pixels = (uint8_t *) rgba;
  rgb.rowBytes = (uint32_t) width * 4;

  if (avifImageRGBToYUV(image, &rgb) != AVIF_RESULT_OK) {
    avifImageDestroy(image);
    *err = "avifImageRGBToYUV failed";
    return 1;
  }

  avifEncoder *enc = avifEncoderCreate();
  if (!enc) {
    avifImageDestroy(image);
    *err = "avifEncoderCreate failed";
    return 1;
  }
  enc->minQuantizer = min_q;
  enc->maxQuantizer = max_q;
  enc->minQuantizerAlpha = min_alpha_q;
  enc->maxQuantizerAlpha = max_alpha_q;
  enc->speed = speed;

  // Must be >= 2: libavif sets AV1E_SET_ROW_MT only when maxThreads > 1, and
  // libaom's row-MT encode is a different bitstream. The count is irrelevant.
  enc->maxThreads = threads < 2 ? 2 : threads;

  // flags = 0, NOT AVIF_ADD_IMAGE_FLAG_SINGLE. This is why avifenc can never
  // reproduce the CDN and a direct caller is required. The one flag decides
  // three things at once: aom usage (all-intra vs realtime), still_picture and
  // reduced_still_picture_header (both 0 in prod), and whether a fully opaque
  // alpha plane is dropped -- prod emits an alpha item on EVERY rendition,
  // including opaque JPEG masters.
  avifResult r = avifEncoderAddImage(enc, image, 1, 0);
  if (r != AVIF_RESULT_OK) {
    avifEncoderDestroy(enc);
    avifImageDestroy(image);
    *err = avifResultToString(r);
    return 1;
  }

  avifRWData data = AVIF_DATA_EMPTY;
  r = avifEncoderFinish(enc, &data);
  avifEncoderDestroy(enc);
  avifImageDestroy(image);
  if (r != AVIF_RESULT_OK) {
    *err = avifResultToString(r);
    return 1;
  }

  // Hand back a plain malloc'd buffer so Go frees it without libavif's
  // allocator being involved.
  uint8_t *copy = malloc(data.size);
  if (!copy) {
    avifRWDataFree(&data);
    *err = "out of memory";
    return 1;
  }
  memcpy(copy, data.data, data.size);
  *out = copy;
  *out_len = data.size;
  avifRWDataFree(&data);
  return 0;
}

void
wixavif_free(uint8_t *p)
{
  free(p);
}
