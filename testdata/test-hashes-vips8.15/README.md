# Hash overlay for libvips 8.15.x

`ImageHashCacheMatcher` looks here **before** `testdata/test-hashes/`, per file,
and falls back to it. So this directory holds only the entries that genuinely
differ on the pinned libvips; everything else still comes from upstream's set,
and upstream's directory is left byte-for-byte untouched.

## Why it exists

Most suites compare processed output **byte-exactly** (`HashTypeSHA256` — a
mismatch reports distance 1.0, not a perceptual score). This fork pins libvips
**8.15.5** to reproduce the Wix CDN (`OP-SPEC.md` §1) where upstream builds
8.18.x, so the resampler differs and those comparisons differ with it.

Rewriting upstream's fixtures in place would conflict on every rebase and would
lose the reference for anyone building against stock imgproxy. An overlay keeps
both: build on 8.15 and these win; build on 8.18 and this directory is ignored
entirely. Both are tested.

## What it contains

233 entries, all resampling paths — `TestProcessing` resize/fill/fit/size-limit,
`TestCrop/TestResizeFill`, `TestExtend`, `TestPadding` at dpr 0.5, plus the
`TestColorspace`/`TestFlatten`/`TestWatermark`/`TestApplyFilters` and
`TestMatrix` entries that move with it.

A measurement worth keeping: regenerating **all 896** hashes on the pinned build
changed only **117**, leaving **415 byte-identical**. The pin moves the
resampler and little else, which is what `OP-SPEC.md` describes.

## Regenerating

    TEST_CREATE_MISSING_HASHES=1 go test ./processing/ ./integration_test/

New hashes are written **here**, not to upstream's directory, whenever this
overlay exists. `TEST_CREATE_MISSING_HASHES` only fills gaps, so delete the
entries you want rebaselined first — and do that on a copy, not in place.

Rebaseline only what actually fails. `TestMatrix` uses DCT hashing with a
tolerance, so a hash file can differ byte-wise while still passing: blanket
regeneration there would discard 158 perfectly good upstream references.

## Known failure, unrelated to hashes

11 subtests fail on one fixture, `test-images/tiff/4-bpp-grayscale.tiff`:

    ThunderDecode: Not enough data at scanline 320 (0 != 1512)

**libvips 8.15.5 cannot decode ThunderScan-compressed TIFF; 8.18.4 can** —
measured on both with the same libtiff 4.7.2, so it is the libvips pin, not the
distro and not libtiff. `vipsheader` reads the file; the pixel decode is what
fails, so no amount of regeneration helps. It does not touch the Wix path, which
refuses TIFF masters outright (`WIXEMU.md` §9).
