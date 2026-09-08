# Golden hashes

These are imgproxy's own regression hashes, compared by
`testutil.ImageHashCacheMatcher`. Most suites use `HashTypeSHA256` — a **byte
exact** comparison, where any difference reports distance 1.0 — while
`TestMatrix` uses `HashTypeDct`, a perceptual comparison with a tolerance.

## Rebaselined for the pinned libvips

This fork pins **libvips 8.15.5** (see `docker/wix/Dockerfile` and
`OP-SPEC.md` §1), because reproducing the Wix CDN depends on that exact
resampler. Upstream imgproxy builds against 8.18.x, so the hashes shipped
upstream describe a different resampler and cannot pass here.

Roughly 230 hashes were regenerated on the pinned build (see
`docker/wix/build-base.sh`). The regeneration was
deliberately narrow — only entries whose tests actually failed were replaced,
so upstream's values survive everywhere the two builds agree.

Measured while doing it, which is the useful part: regenerating **all** 896
hashes changed only **117**, leaving **415 byte-identical**. The pin therefore
changes the resampler and essentially nothing else — every changed entry is a
resize, fill, fit, crop-with-resize, extend or padding case. That matches what
`OP-SPEC.md` says about `reducev` and the lanczos coefficients.

To regenerate after an intentional change:

    TEST_CREATE_MISSING_HASHES=1 go test ./processing/ ./integration_test/

`TEST_CREATE_MISSING_HASHES` only fills gaps, so delete the entries you want
rebaselined first — and do that on a copy, not in place, unless you mean it.

## Known failure: `tiff/4-bpp-grayscale.tiff`

11 subtests fail on this one fixture, in `TestMatrix` and `TestImageHash`:

    ThunderDecode: Not enough data at scanline 320 (0 != 1512)

**libvips 8.15.5 cannot decode ThunderScan-compressed TIFF; 8.18.4 can.**
Measured directly on both, with the same libtiff 4.7.2 underneath:

    vips-8.15.5   vips avg tiff/4-bpp-grayscale.tiff  ->  FAILS
    vips-8.18.4   vips avg tiff/4-bpp-grayscale.tiff  ->  decodes OK

So it is the libvips pin, not the distro and not libtiff — the same test passes
on stock imgproxy. `vipsheader` reads the file; the pixel decode is what fails,
so it is not a hash mismatch and regeneration cannot fix it.

This is the one capability the pin costs, and it is narrow: one obscure TIFF
compression scheme. Every other format the test corpus contains decodes.
