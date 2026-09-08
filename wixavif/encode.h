#ifndef IMGPROXY_WIXAVIF_H
#define IMGPROXY_WIXAVIF_H

#include <stdint.h>
#include <stdlib.h>

// Encode RGBA pixels to AVIF exactly the way the Wix CDN does.
// OP-SPEC.md §7.5. Returns 0 on success and fills *out/*out_len with a buffer
// the caller frees with wixavif_free; non-zero on failure, with a static
// message in *err.
int wixavif_encode(
    const uint8_t *rgba, int width, int height,
    int min_q, int max_q, int min_alpha_q, int max_alpha_q,
    int speed, int threads,
    uint8_t **out, size_t *out_len, const char **err);

void wixavif_free(uint8_t *p);

#endif
