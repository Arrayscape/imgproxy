# WIXEMU — the Wix CDN emulation in this fork

This fork of imgproxy serves the Wix CDN's image URL grammar, so a site
migrating off Wix can keep the image URLs already in its markup and still
render sizes nobody pre-computed.

It is an **addition**, not a replacement: imgproxy's own URL grammar and
pipeline are untouched and keep working. The Wix route is off by default.

The behaviour reproduced here is specified in three documents, which are
authoritative wherever this file is vaguer:

| Document | Covers |
|---|---|
| `WIX-URL-SPEC.md` | What the CDN does, observed from outside |
| `OP-SPEC.md` | The libvips build, environment and operation sequence that reproduces it |
| `SCORECARD.md` | Measured coverage against live CDN output |

---

## 1. Enabling it

```
IMGPROXY_WIX_ENABLED=true
IMGPROXY_WIX_SOURCE_URL_TEMPLATE=local:///%s
IMGPROXY_LOCAL_FILESYSTEM_ROOT=/masters
```

| Variable | Default | Meaning |
|---|---|---|
| `IMGPROXY_WIX_ENABLED` | `false` | Mounts the `/media` route |
| `IMGPROXY_WIX_PATH_PREFIX` | `/media` | Where the grammar is mounted |
| `IMGPROXY_WIX_SOURCE_URL_TEMPLATE` | — | Required. Exactly one `%s`, replaced by the media id. Any imgproxy source scheme works: `local:///%s`, `s3://bucket/masters/%s`, `https://host/%s` |
| `IMGPROXY_WIX_SRGB_PROFILE` | `/opt/imgproxy/share/wix-srgb.icc` | Wix's own sRGB profile. Falls back to the copy embedded in the binary |
| `IMGPROXY_WIX_AVIF` | `true` | Enables the AVIF branch of format negotiation |
| `IMGPROXY_WIX_AVIF_SPEED` | `8` | `effort = 9 - speed` |
| `IMGPROXY_WIX_SIGNATURE_MODE` | `off` | `off` or `query` (see §6) |

The route registers ahead of imgproxy's catch-all, so both grammars coexist.

## 2. URL grammar

```
/media/<media-id>/v1/<op>/<params>/<filename>     a transform
/media/<media-id>                                 the stored original
```

- `<media-id>` is opaque. **Its extension lies** — it records the original
  upload format, not what is stored, so nothing branches on it. Four of the
  reference site's 366 masters advertise `~mv2.png` and hold lossy WebP.
- `<filename>` affects caching only, never output.
- Segments chain left to right and may repeat, with or without repeated `/v1/`.

### Operations

| Op | Behaviour |
|---|---|
| `fit` | Scale to sit inside `w x h`. Never enlarges. Output dimensions are **floored**, so the result can be a pixel under the box |
| `fill` | Scale to cover `w x h`, then crop the overflow. May enlarge. Output is exactly `w x h` |
| `crop` | Extract the rectangle `x, y, w, h`. Rectangles leaving the source are clipped, not rejected |

### Parameters

| Param | Status | Notes |
|---|---|---|
| `w_N`, `h_N` | implemented | Target box |
| `x_N`, `y_N` | implemented | Crop origin |
| `al_<anchor>` | `al_c` verified; other anchors **unverified** | Its *presence* changes the centring rounding independently of its value — see §4 |
| `fp_<x>_<y>` | `fp_0.50_0.50` verified; other values **unverified** | Focal point, continuous |
| `usm_<s>_<a>_<t>` | implemented | Sharpen. Sigma above 10 falls back to 0.5 |
| `blur_N` | implemented, **thinly verified** | Gaussian blur, sigma N. Verified on one image at one sigma |
| `q_N` | implemented | Lossy quality, default 85. Ignored on the lossless WebP path, which fixes Q at 75, and read differently again for JPEG — see §5 |
| `quality_auto` | **parsed, inert** | Boolean, not numeric: `quality_20`, `quality_50` and `quality_best` are all identical on the CDN and only `auto` vs not-`auto` matters. What `auto` actually changes was never determined, so we parse it and ignore it |
| `lg_N` | **accepted and ignored** | No case has ever been found where it changes anything |
| `enc_avif`, `enc_auto` | implemented | Opt-in flag for format negotiation, *not* a format name |
| `enc_<other>` | accepted and ignored | Unrecognised |

## 3. Output format is negotiated, not named

`enc` opts in; the format then comes from the request's `Accept` header,
preferring **avif > webp > the stored master's own format**:

| URL | Accept | Output |
|---|---|---|
| `enc_avif` | `*/*` | master's format |
| `enc_avif` | `image/avif,image/webp,*/*` | AVIF |
| `enc_avif` | `image/webp,*/*` | WebP |
| `enc_avif` | `image/png` | master's format |
| no `enc` | anything | master's format |

A client sending `Accept: */*` therefore never sees AVIF, whatever the URL says.
Responses carry `Vary: Accept`.

**Without `enc_`, the filename extension decides and `Accept` is ignored
entirely** (§4.1). So a PNG master really does serve JPEG for a `.jpg` name —
the two rules select different encoders, and opting out of negotiation is not a
no-op. An unrecognised or absent extension falls back to the master's format.
In practice production always opts in: 2613 of 2648 rendition urls carry `enc_`.

**WebP codec follows the stored master**, sniffed from magic bytes: a lossless
master (PNG) encodes to VP8L, a lossy one (JPEG, WebP) to VP8. Not alpha
presence, and not encode-both-and-keep-the-smaller.

## 4. The three behaviours most likely to surprise you

**EXIF Orientation is applied before geometry.** A master tagged Orientation 6
is rotated first, and `sw x sh` are the *rotated* dimensions. Getting this
wrong does not merely rotate the output — it computes a different scale and a
different output box, so every downstream rule is wrong too. A 3088x2316 master
asked to `fit w_1372,h_1029` returns 771x1029, not 1372x1029. Format-independent.


**`fit` floors.** A 1725x1294 master fitted into 1120x840 returns **1119**x840,
not 1120x840. Rounding instead of flooring changes roughly half of all `fit`
renditions by a pixel.

**`al`'s presence changes the crop, even when its value is a no-op.** With `al`
the centring halves are taken separately (`sw/2 - hw/2`); without it they are
combined (`(sw - hw)/2`). The two differ by one pixel whenever the source
dimension is even and the crop window odd. So `fill/w_123,h_66` and
`fill/w_123,h_66,al_c` are *different crops* — despite `al_c` being centre,
which is what the URL does anyway.

## 5. Metadata

**Renditions are stripped, in both containers.** Any `/v1/` URL returns a
synthesised 11-tag EXIF whitelist and nothing else: Orientation, XResolution, YResolution,
ResolutionUnit, YCbCrPositioning, ExifVersion, ComponentsConfiguration,
FlashpixVersion, ColorSpace, PixelXDimension, PixelYDimension. No GPS, no camera
make or model, no timestamps, no software, no serial numbers. libvips forwards
source metadata by default and also writes a `zTXt` chunk and a *second* `eXIf`
chunk; all three are removed. **This is a security control**, and it is covered
by a test that renders a master carrying camera and timestamp tags and asserts
none of it survives.

WebP needs its own fix-ups (OP-SPEC §8.4), and they are required for
byte-exactness rather than cosmetic — the coded payload was already exact,
which is exactly why a payload-only comparison reported WebP as finished while
22 of 54 *files* still differed. The CDN always emits

```
RIFF WEBP  VP8X  [ICCP]  [ALPH]  VP8|VP8L  EXIF
```

so three things are corrected: stray `XMP ` chunks are dropped (libvips writes
them, the CDN never does, and they carry whatever the uploader's file held);
the `EXIF` chunk is rebuilt as `"Exif\0\0"` + the same canonical 180 bytes
rather than forwarding the master's, which libvips emits big-endian and without
`YCbCrPositioning`; and `VP8X` is always written, synthesised when libvips
omitted it, with flags `0x08` EXIF, `+0x20` ICCP, `+0x10` alpha and a canvas
size read from the VP8/VP8L bitstream.

> ### ⚠ The bare media path deliberately leaks
>
> `/media/<media-id>` with no `/v1/` segment returns the stored original
> **exactly as uploaded, metadata included** — camera model, timestamps and GPS
> coordinates among it. Media ids are public in every rendition URL on a page,
> so anyone can strip the transform suffix and retrieve the untouched original.
>
> This reproduces the CDN's own behaviour and is **intentional**, for
> compatibility. It is not an oversight and not a bug.
>
> Closing it is a product decision, not an implementation detail. It affects
> only this path — rendition output is stripped either way — and a later commit
> may add the option. Until then, if originals must not be reachable, do not
> expose this path: restrict it at the reverse proxy, or strip metadata on
> ingest into the master store.

### 5.1 JPEG

JPEG has its own encoder settings and its own quality rule, neither shared with
WebP:

    vips jpegsave IN OUT.jpg --Q <q> --interlace --subsample-mode off

Progressive with libjpeg's own ten-scan progression, 4:4:4 at every quality, no
restart markers. `--subsample-mode off` is load-bearing: libvips' `auto`
subsamples below Q 90 and the CDN never does. Stock **libjpeg-turbo**, not
mozjpeg — the DQT identifies the encoder as well as the quality. This fork
links 3.2.0 (upstream imgproxy's pin) against the reference image's 2.1.5;
that difference has been checked and does not move the coded stream.

Quality (§7.4), which is **not** the WebP rule:

| URL | Q |
|---|---|
| `enc_` on **every** segment | 80, `q_N` ignored |
| `enc_` on **some** segments | 90, `q_N` ignored |
| no `enc_` anywhere | the **last** segment's `q_N`, default 90 |

The conjunction is not academic: production's
`crop/…/fill/…,q_85,enc_avif,quality_auto` carries no `enc` on the crop segment,
so it encodes at **90** — not the 85 it asks for, nor the 80 the flat form
gives. `quality_N` is inert for JPEG.

The container is `SOI APP1(Exif) [APP2(ICC)] DQT … EOI`, with no APP0 JFIF: the
APP1 is replaced with the canonical 180-byte block, the XMP APP1 and every
non-ICC APPn are dropped, and APP2 is kept verbatim. Resolution follows §8.3
but reads the **master's JFIF density** in place of `pHYs`, which a JPEG lacks.

This is the largest metadata leak of any output format if skipped — a full
camera EXIF with an embedded thumbnail (6429 bytes on one measured master) plus
40 KB of XMP.

**Scope:** these settings reproduce JPEG from a **JPEG master**, which is what
production does. A **PNG master** requested as `.jpg` also returns JPEG, and
they do *not* reproduce it — 1/35, with quality mispredicted on 20. No
production url does this.

### 5.2 AVIF

Encoded through **libavif 0.11.1 + libaom 3.6.0**, linked statically, not
through libvips. Not a tuning choice: the CDN's AVIFs carry `hdlr` name
`libavif` and libavif's own box layout, so libheif output would be the wrong
encoder however it was configured. There are no container fix-ups — all 429
header bytes already match, differing only in `iloc` offsets, which are
functions of payload size.

Three settings are load-bearing and none is guessable:

- **`avifEncoderAddImage(enc, image, 1, 0)` — flags 0**, not
  `AVIF_ADD_IMAGE_FLAG_SINGLE`. This is why `avifenc` can never reproduce the
  CDN and a direct C caller is required. It decides three things at once: aom
  usage (all-intra vs realtime), `still_picture`/`reduced_still_picture_header`
  (both 0 in prod), and whether a fully opaque alpha plane is dropped — prod
  emits an alpha item on **every** rendition, including opaque JPEG masters.
  There is a test for exactly that.
- **`maxThreads >= 2`.** libavif sets `AV1E_SET_ROW_MT` only above 1, and
  libaom's row-MT encode is a *different bitstream*. The count is irrelevant.
- **libavif built with `-ffp-contract=off`.** Its `reformat.c` does RGB→YUV in
  float; aarch64 contracts `a*b+c` into `fmla` and x86-64 baseline cannot, so
  without the flag the two architectures feed libaom different YUV. libaom
  itself needs no flags.

Settings: profile 0, 8-bit, 4:2:0, full range, CICP 2/2/2, no ICC (the profile
is consumed by the `icc_transform` earlier, not carried into the rendition),
speed 9. `q_N` selects `minQuantizer` from a **28-point measured table** — the
curve has no closed form — with alpha fixed at 27/35 regardless of `q`, and a
url with no `q_` behaving as `q_90`. `maxQuantizer` is inert (every value from
minQ to 63 gives identical bytes); `minQ + 8` reproduces what Wix sends.

A `q` outside the 28 measured points falls back to the nearest one and is
flagged internally as inexact — it cannot be derived, only guessed. No
production url uses one.

**Status: byte-exact, 8/8** on freshly uploaded masters spanning 218x100 to
3581x2385 at quality 80/85/90 (OP-SPEC §7.5).

The earlier 21/25 was not an encoder residual. The CDN returns TWO renditions
for one AVIF transform -- a from-master encode and one built from a cached
ancestor -- and picks per request, so a single request scores that coin flip
rather than the encoder. AVIF carries no `Exif` item, so unlike PNG, JPEG and
WebP there is no marker to tell the two apart from the bytes. Six fresh renders
of one transform returned `23294, 23128, 23128, 23128, 23294, 23128`; 23294 is
ours. Any AVIF figure below 8/8 is measuring how heavily a master has been
probed -- each request creates an entry that becomes a candidate ancestor for
the next -- not encoder accuracy.

Because libaom 3.6.0 shares a soname with the 3.14.1 the base builds for
libheif, both AVIF libraries are **static in a private prefix**
(`/opt/wix-avif`) and linked into the binary; nothing of them is needed at
runtime.

## 6. Access control

Wix URLs are unsigned, and this route is unsigned by default. Two ways to bound it:

- **A reverse-proxy allowlist**, which is how the reference deployment runs:
  nginx holds the set of transform URLs the build published and 404s everything
  else, so the set of addressable renditions is bounded by construction.
- **`IMGPROXY_WIX_SIGNATURE_MODE=query`**, which requires `?sig=<base64url>` over
  the canonical path, verified with imgproxy's existing `IMGPROXY_KEY`/`IMGPROXY_SALT`.
  A query parameter rather than a path segment, because a path signature would
  break the URL shape — which is the whole point of the feature.

Unsigned and unbounded, a client can request thousands of distinct sizes, each
one a cache miss and seconds of encoding. Do one of the two.

## 7. Effects on imgproxy's own tests

Pinning libvips changes imgproxy's native pipeline output, so its golden hashes
were rebaselined — see `testdata/test-hashes/README.md`. Regenerating all 896
changed only 117, all resampling paths, which is a useful confirmation that the
pin changes the resampler and little else.

Two genuine incompatibilities between imgproxy v4 and libvips 8.15.5 turned up
and are fixed in `vips/vips.c`, both version-guarded so an 8.16+ build is
unaffected:

| Loader | Property | Effect on 8.15.5 |
|---|---|---|
| `tiffload_source` | `unlimited` | every TIFF load failed |
| `jxlload_source` | `page`, `n` | every JXL load failed |

Neither degrades gracefully: libvips rejects an unknown property outright, so
passing one makes that entire format unreadable rather than just disabling the
option. `IMGPROXY_TIFF_UNLIMITED` and animated JXL are inert on 8.15.5.

One capability is genuinely lost to the pin: **libvips 8.15.5 cannot decode
ThunderScan-compressed TIFF**, where 8.18.4 can — measured on both with the same
libtiff underneath. It costs 11 subtests on a single fixture and nothing else;
see the test-hashes README.

## 8. Build requirements

The transform depends on the libvips build, not just the code, so the fork ships
a base image. `docker/wix/build-base.sh` builds it as a **thin fork of
imgproxy's own base image build**, not a replacement:

```
./docker/wix/build-base.sh
docker build -f docker/Dockerfile -t imgproxy-wix .
```

Upstream's base builds every dependency from source into `/opt/imgproxy/lib`
with `-Dmodules=disabled`, which is what lets the runtime image stay
`ubuntu:noble` and copy a single directory. All of that is kept. The fork
changes only what OP-SPEC.md §1 requires, against a pinned upstream commit:

| Change | Why |
|---|---|
| libvips 8.18.5 → **8.15.5** | the resampler the CDN's output was measured against |
| one patch in `docker/wix/` | `0002` gives `webpsave` a near-lossless level independent of `Q` |
| **add liborc**, `-Dorc=enabled -Dhighway=disabled` | upstream builds Highway and no ORC at all; the CDN's results come from ORC's fixed-point path, and Highway changes them in the low bits |
| `-ffp-contract=off`, scoped to libvips | FMA contraction changes resample results |
| `patch` added to the deps stage | upstream's build image does not ship it |
| `-Ddocs` dropped | 8.15.5 spells it `-Dgtk_doc`/`-Ddoxygen`, both off by default |

Every other dependency — libjpeg, libpng, libwebp, lcms2, libtiff, libjxl, glib
— stays at upstream's pinned version. That matters if a byte difference ever
shows up: it narrows the cause to one library rather than a whole distro.

The edits are asserted against exact upstream text and fail loudly if upstream
moves, rather than silently building something that is not the transform.

At runtime:

- **Images are opened with `access=random`.** OP-SPEC §2 calls this "the single
  most important rule in this document after the geometry". Under sequential
  access `reducev` sits behind a line cache whose strip height decides which
  output rows land on a phase tie, so the result depends both on that height
  *and* on what consumes the resize — chaining a sharpen after it moves the
  strip boundaries and changes the output, which is why no single tile height
  could satisfy both the plain and the sharpened path. Random access removes
  the cache and is byte-exact on both.
- **No environment variables are required.** `VIPS_TILE_HEIGHT` is irrelevant
  under random access and is no longer set or checked.
- `VIPS_NOVECTOR` must never be set — the handler refuses to start if it is.
  `VIPS_VECTOR` and `VIPS_CONCURRENCY` are both measured safe.
- The pipeline is chained lazily; nothing materialises an intermediate, because
  that would break the demand coupling `reducev` depends on (OP-SPEC §5).
- Host architecture is irrelevant given the build flags: arm64 and amd64 were
  measured byte-identical across the whole corpus.

Deliberately **not** applied: libvips#4909, the enlargement half-pixel fix. Wix
runs stock libvips and shares the displacement, so patching it moves output
*away* from the CDN on every enlargement.

Substituting the unmodified upstream base still builds, but the Wix route
refuses to start rather than serve output that silently does not reproduce the
CDN — see `IMGPROXY_WIX_ALLOW_UNVERIFIED_LIBVIPS` in §1.

## 9. Master formats

Every format Wix's docs list has been probed against the live upload API, and
the disposition decides what the transform path must do. Ingest sniffs
**content**, not the extension, and so does this fork.

| Master | Ingest disposition | What we do |
|---|---|---|
| JPEG, PNG, WebP | stored verbatim | decode, transform, encode |
| **AVIF** | kept verbatim, needs a decoder | decode, transform, encode |
| **GIF** | passed through untransformed | **serve the stored bytes** |
| TIFF, HEIC/HEIF, BMP | transcoded to a PNG derivative | refuse (422) |
| JPEG 2000 | transcoded to a JPEG derivative | refuse (422) |
| RAW | demosaiced to a JPEG derivative | refuse (422) |
| SVG | routed out of `/media` entirely | refuse (422) |
| JPEG XL | rejected at ingest | refuse (422) |
| anything unrecognised | — | refuse (422) |

**GIF is not transformed.** Ingest stores it verbatim and the media router
answers rather than the image manipulator, so `w_180,h_135` returns the
FULL-SIZE original — which is also how animation survives. We serve the stored
bytes with `Content-Type: image/gif`, decided before decode because there is
nothing to decode.

**AVIF is the only format that leaves an outstanding obligation.** It is kept
verbatim and genuinely decoded, so an AVIF master needs a real decoder. The
reference build has no libheif and cannot open one; this fork's base can, so an
AVIF master decodes and renders rather than erroring. That is a prerequisite,
**not** byte-exactness: decode measures **3/12** whole-file, with the residual
in the YCbCr→RGB matrix step (`matrix=0` exact, `matrix=6` — BT.601, what real
cameras emit — off by ±1..5). Zero AVIF masters in the corpus, so no production
impact today.

**The refused formats are unobservable, not unsupportable.** Whether ingest
transcodes them (the canonical id becomes a derivative), demosaics them, routes
them to another subsystem, or rejects them outright, no renderable master of
them can reach the transform path — so there is no CDN behaviour to reproduce
and rendering one would be inventing a transform.

Unrecognised formats are refused for the same reason plus a practical one:
JPEG 2000 and RAW have no imgproxy type and sniff as `Unknown`, and refusing up
front beats failing later with a confusing decoder error. If Wix's ingest
changes, `wix.FormatDisposition` is the single place to update.

Refusal is decided by **content**, matching Wix's own 406 on bytes whose
filename and MIME type both claimed PNG. A TIFF behind a `~mv2.png` id is
refused, and there is a test for it.

## 10. What is verified, and what is not

Against live CDN output, on the master render path:

| Area | Score |
|---|---|
| `fit` | 908 / 908 byte-exact |
| `fill` | 433 / 433 byte-exact |
| `crop` | 70 / 70 |
| `usm` | 685 / 685 |
| WebP VP8 (lossy) | 20 / 20 — whole-file |
| WebP VP8L (lossless) | 29 / 29 — whole-file |
| JPEG | 56 / 56 — whole-file |
| AVIF | **21 / 25** — residual unexplained |

**Not verified.** Treat these as best-effort, implemented from the specification
rather than measured:

- **AVIF input (decoding a master).** Supported, not reproduced: a
  libvips+libheif build is 3/12 whole-file byte-exact and the residual is in
  the decode's YCbCr→RGB matrix step, not in anything downstream. Zero AVIF
  masters in the corpus, so zero impact today.
- **AVIF output.** Verified: the negotiation is exact and the encoder settings
  reproduce the CDN **byte for byte, 8/8** on virgin masters (§5.2). The
  previously unexplained 21/25 residual turned out to be the CDN's per-request
  choice between a from-master and an ancestor-derived rendition, not an
  encoder difference.
- **JPEG from a PNG master.** §7.4's settings reproduce JPEG from a JPEG master,
  not from a PNG one (1/35 upstream, quality mispredicted on 20). Reachable via
  a `.jpg` name with no `enc_`, but no production url does it.
- **`blur_N`** — one image, one sigma.
- **`fp_<x>_<y>`** other than `0.50_0.50`. Whether `fp`'s presence changes the
  rounding the way `al`'s does is untested.
- **`al` non-centre anchors** — behaviour observed, never scored.
- **Everything about derived renditions.** Nothing in §10 has been scored
  against CDN output: the corpus deliberately excludes cache-derived rows,
  because which ancestor the CDN picked is not reproducible. The rules are
  implemented as specified, but "given the ancestor, the output is byte-exact"
  is a claim the specs make about the CDN, not one this fork has verified about
  itself. The one part that *is* measured is the marker: a cache-derived
  rendition carries `pHYs 1000` and EXIF XResolution `25400/1000`, confirmed on
  1307 of 1307 such renditions, and this fork reproduces both.

- **Chains beyond `crop* -> fit|fill`.** The grammar composes freely and this
  implementation composes the general form: each segment is computed against
  the previous segment's *output*, and its crop window is mapped back into
  master coordinates so the whole chain still renders as one `extract_area`
  plus one resize. Only these shapes are measured, though:
  `crop* -> (fit|fill)` (the corpus), `crop -> crop`, and a single
  `scale -> crop` fuse. **`scale -> scale`, and any chain longer than that, is
  our own arithmetic and is unverified against the CDN.**
- **`lg_N`** — unexplained rather than proven inert.

## 11. Coverage against WIX-URL-SPEC

| Spec section | Status |
|---|---|
| §1 URL grammar, bare media path | implemented |
| §1.1 extension lies — sniff magic bytes | implemented |
| §1.2 chaining | implemented; verified only for `crop* -> (fit\|fill)`, `crop -> crop`, one `scale -> crop` |
| §2 `fit` / `fill` / `crop`, out-of-bounds clipping | implemented |
| §3.1 `w` `h` `x` `y` | implemented |
| §3.2 `al` presence rounding + anchors | presence and `al_c` verified; other anchors unverified |
| §3.3 `fp` | `fp_0.50_0.50` verified; other values unverified |
| §3.4 `usm` | implemented |
| §3.5 `blur_N` | implemented, one image / one sigma |
| §3.6 `lg_N` | accepted and ignored |
| §3.7 `enc` / `q` | implemented |
| §3.7 `quality_auto` | **parsed but inert** |
| §4 format negotiation | implemented, all rows verified |
| §5 WebP codec selection | implemented |
| §6 / §10.1 cache-derived renditions | implemented as a plain resize, **off by default** — see below |
| §7.1 renditions stripped | implemented |
| §7.2 originals not stripped | implemented, deliberately |
| §7.3 colour → Wix sRGB | implemented, including on a bare `crop` |
| §9.1 transform headers | implemented; `Vary` conditional on `enc_`, ranges served un-advertised |
| §9.2 original headers | implemented; etag verified as md5 of the body, 304 shape asserted |
| §9.3 error statuses | implemented; one cosmetic divergence, see §13.2 |
| §9.4 no `content-encoding` | implemented |
| §9.5 GIF pass-through headers | implemented, **measured** — takes the §9.2 set, byte-identical to the bare original |

## 12. Cache-derived renditions (§6 / OP-SPEC §10)

The CDN caches transformed output and uses it as the input to later transforms,
which is how roughly half of a mature URL set is produced. This fork does the
same, **off by default**:

```
IMGPROXY_WIX_DERIVATION_CACHE=true
IMGPROXY_WIX_DERIVATION_CACHE_PATH=/var/cache/wix   # durable; omit for in-process
IMGPROXY_WIX_DERIVATION_CACHE_SIZE=536870912        # bytes of payload
IMGPROXY_WIX_DERIVATION_MAX_DEPTH=1                 # derive only from a master render
```

**Give it a path.** Without one the cache is in-process: empty after every
restart and not shared between replicas, so derivation stays far rarer here
than on the CDN, where roughly half of a mature URL set is derived. The
on-disk store is content-addressed, LRU-evicted against a byte budget, and
rebuilds its index by scanning the directory on open — an index file can go
stale or be truncated by a crash, while the directory is the truth. Payloads
are written through a temp file and renamed, so a crash cannot leave a torn
entry that a later run would serve as complete; a payload whose metadata is
missing is dropped rather than served. Effects are recorded in the sidecar, so
a sharpened rendition is never offered as a resamplable ancestor after a
restart.

Off by default because **deriving deliberately changes output bytes**, and the
master path is the one measured byte-exact against the CDN. With it off, nothing
is ever served from a rendition — there is a test asserting exactly that.

**Deriving is a plain resize, not the pipeline.** This is the part OP-SPEC
§10.1 says an implementation is most likely to get wrong, and it is right —
both earlier attempts here were wrong, in opposite directions. The natural
assumption, "same pipeline, different input", is false:

```
from the MASTER          §5: shrink + reduce, premultiply where there is
                         alpha, the §4.5 drop rule, icc_transform, crop
from a CACHED RENDITION  vips resize LEVEL.png OUT.png \
                             <W_target/W_level> --vscale <H_target/H_level>
```

Measured on the CDN's own renditions: resizing a cached level down to a target
below it reproduces that target **pixel-exactly** (mean|d| 0.0000, max 0) over
five level→target pairs, and `979 → 900` correctly does *not* match, which is
the selection rule working.

So on this path there is no premultiply even when the image has alpha, no drop
rule, no sharpen or blur, no `icc_transform` (the ancestor is already in Wix's
sRGB — transforming again would move the colours twice), and no crop.

Two details that are easy to get wrong, both pinned by tests:

- **The two axes get independent scales.** A cached level is aspect-preserved
  and rounded each axis separately when it was made — on a 1032×24 master the
  925 level is 925×21, not 925×24 — so a single scale, or forcing a height,
  stretches the image into something that matches nothing.
- **The target's dimensions come from the plan resolved against the MASTER**,
  never by re-resolving the URL against the ancestor. A URL's output size is a
  property of the URL and the master alone: the same request must come back the
  same size whether it was served from the master or derived, and only the
  pixels may differ. Re-resolving against the ancestor — which is what this
  fork did before §10.1 was measured — changes the size too, and the response
  still looks like a perfectly valid image.

**Deriving is proven to be reproducible.** OP-SPEC 1.7.0 briefly claimed
otherwise — that a derived rendition could not be reproduced by re-running the
pipeline on the cached ancestor — and 1.9.0 retracted that: the test behind it
had used ancestors that cannot be ancestors, namely widths the probing session
had itself requested. Resampling the CDN's own cached rendition reproduces its
derived rendition byte-exactly, 8 of 8. The model this cache is built on is the
right one.

**Which ancestor gets used is our policy, not the CDN's.** The CDN picks the
smallest *pyramid level* above the target that the answering node can reach,
else the master. The levels are a fixed internal set per master and are **not**
the widths anyone requested — on a 1032-wide master they are 979, 925, 839,
758, 604, 545, 472 and 391, and asking for a fresh `w_911` does not make 911 an
ancestor for a later `w_887`. Where those widths come from is unmapped, so the
level set cannot be reproduced.

That is why the CDN can answer one url two different ways: which node handled
the request, and what it held at that instant, is not observable. Both answers
are correct. An implementation has no such ambiguity — it owns its cache and
knows which levels exist — so OP-SPEC §10 asks only for a policy that is a pure
function of the request and the cache contents, and names "smallest cached
rendition at least as large as the target, else the master" as the obvious
choice. Ours is that, plus the soundness conditions it leaves implicit. An
entry is usable only if **all** of:

1. it has no effects baked in — a sharpened or blurred rendition is not a
   resamplable source;
2. it is PNG — deriving from a lossy re-encode compounds artefacts;
3. **its source rectangle is exactly the region the new request reads** — not
   merely one that contains it. OP-SPEC §10 suggests "smallest rendition at
   least as large as the target", which is unsound on its own (two `fill`s of
   different aspect ratios can both be larger while covering disjoint parts of
   the master), and under §10.1 containment is not enough either: with no crop
   step, resizing an ancestor that merely contains the target squashes the
   whole ancestor into the target's box. In practice this means only
   whole-master targets derive — `fit`, which uses the entire source. A `fill`
   that crops, or a `crop` op, falls back to the master, which is also the only
   thing the CDN could do: its pyramid levels are whole-master and
   aspect-preserved, so a cropped target has no level it could have come from;
4. it is at least as large as the target on both axes — never upsample from a
   rendition, the master still has the detail;
5. its derivation depth is under the cap;
6. it covers the whole master — the shape the CDN's own pyramid levels have.

Among those, the smallest by area wins, ties broken on the source rectangle so
the choice does not depend on insertion order or map iteration.

**A request carrying `usm` or `blur` is never derived at all.** §10.1 says the
derivation path does not sharpen, so deriving such a request would silently
drop the effect the URL asked for. Falling back to the master is always
permitted, so it does that instead. Our fixtures cannot distinguish the absence
of premultiply — the alpha master carries a constant alpha, where premultiply
round-trips to the identity — so that part of §10.1 is assured by construction
and by reading `Derive`, not by a test.

**Nothing in the response says which input was used.** On the CDN a from-master
and a cache-derived rendition are identical at the header level — only the
payload's own `pHYs`/`XResolution` marker tells them apart — and OP-SPEC §12
says an implementation should not try to signal derivation status either. For
our own tests and for diagnosing a staging cache, `IMGPROXY_WIX_DEBUG_SOURCE_HEADER=true`
adds `X-Wix-Source: master | cache | derived | passthrough`. It is off by
default and should stay off in production.

**Cached intermediates are stripped before being used as a source.** §6.1 notes
that a derived rendition reads `pHYs 1000` because the intermediate lost its
resolution in a write/re-read cycle. The CDN's intermediates carry no metadata
at all; ours are complete PNGs, so an ancestor's metadata would otherwise travel
into the derived rendition and diverge in two ways at once — `pHYs` would keep
the master's value, *and* the canonical EXIF's `XResolution` would be derived
from the ancestor's `eXIf` instead of from the rendition's own `pHYs`. Both are
bugs. The ancestor is therefore stripped and its resolution reset at read-back,
so a derived rendition carries `pHYs 1000` and an `XResolution` of `25400/1000`
derived from it, per §8.3 step 3 — which is what the CDN emits, measured on
1307 of 1307 cache-derived renditions. The master path is unaffected.

## 13. Response headers

The header set is decided by which of the CDN's two services would have
answered, not by the format or the operation. WIX-URL-SPEC §9.

**Transforms** — anything with a `/v1/` segment:

    cache-control: public, max-age=2592000, immutable
    vary: Accept                      only when the url carries enc_avif or enc_auto

Constant across every op, format, quality and geometry. `IMGPROXY_TTL` does not
apply to this route; the value is part of the emulation, not a tuning knob.

`Vary` is conditional and its absence is meaningful: with no `enc_`, `Accept` is
ignored entirely (§4.1) and the filename extension alone decides the format, so
there is nothing for a cache to vary on. Sending `Vary: Accept` anyway — which
this fork used to do — splits caches on a header that cannot change the answer.

Transforms carry **no `etag`, no `last-modified`, no `expires`, no
`accept-ranges`**, whether served from the master or cache-derived; the two are
indistinguishable at the header level. Range requests are still honoured (`206`
with a `Content-Range`) despite `Accept-Ranges` never being advertised — both
halves of that are measured CDN behaviour.

**Originals** — the bare `/media/<id>`:

    cache-control: public, max-age=15552000, immutable
    expires:       date + 15552000s
    last-modified: the stored object's own modification time
    etag:          "<32-hex>", the md5 of the body
    accept-ranges: bytes

Six times a transform's TTL, and no `vary` — there is no negotiation on this
path. Conditional GET is honoured: `If-None-Match` or `If-Modified-Since`
return `304`, and that `304` omits `content-type`, `content-length` and
`last-modified` while keeping `etag`, `expires` and `cache-control`.

`Content-Encoding` is never sent on any route or format, even when the client
advertises `gzip, br, deflate`.

**GIF is the exception to routing by url shape.** A `/v1/...` transform url on
a `.gif` media id is answered by the media router, so it carries the ORIGINAL
header set — 180 days, etag, last-modified, expires, accept-ranges — and the
bytes are the untransformed master, byte-identical to the bare original. This
is measured. An emulation that picks the header set from the url pattern rather
than from which service would answer gets exactly this case wrong, so the
choice here is made after the format is known, not before.

### 13.1 Errors

Two shapes, because two different tiers produce them. Which one a request gets
says where it was rejected:

| Condition | Status | Body | `cache-control` |
|---|---|---|---|
| Unrecognised op, malformed path, nonexistent media id | 403 | `Forbidden` | `no-cache, private, must-revalidate, proxy-revalidate, no-store` |
| Malformed parameter *value* on a valid op | 400 | `(fil) (dimensions) invalid width abc` | `private, no-cache, no-store, must-revalidate` |

The first never reaches the image manipulator, so it reads as a routing
failure rather than a processing one — 403, not 404 and not 500. The second
does reach it, and the body names the value it could not use. The two
`cache-control` strings differ in both content and directive order; that is
reproduced deliberately rather than normalised.

An unrecognised **output extension** is not an error at all. `.jxl`, `.tiff`,
`.bmp` and `.gif` return `200` and fall back to the master's own stored format,
byte-identical to naming that format outright. Only `png`, `jpg`/`jpeg`,
`webp` and `avif` are recognised — the set the CDN can actually encode. This is
an allowlist rather than a filter over what libvips supports, because
`imagetype` knows both JXL and TIFF and would otherwise answer `.jxl` with
JPEG XL, a capability the CDN does not have (§9).

### 13.2 Implementation notes

**Errors are written by this route, not by imgproxy's error middleware.** The
middleware hard-codes `text/plain` and, with `IMGPROXY_DEVELOPMENT_ERRORS_MODE`
on, replaces the body with a stack trace. Both are wrong here: §9.3 pins the
body *and* the content-type — a charset on the 400, none on the 403 — and a
client parsing our errors should not see a different contract because an
operator turned on debugging. Monitoring and the access log still happen; error
*reporting* does not, because these are client mistakes and carry
`ShouldReport(false)`, which the middleware would have honoured anyway.

**`Expires` is anchored to the stored object's `Last-Modified`**, not to the
clock. §12 requires it be computed once rather than per request, and the CDN's
own freeze point — when the entry was cached — is not observable from outside.
The object's modification time is the one timestamp both ends agree on that
does not move between requests. When the origin supplies no `Last-Modified`,
`Expires` is omitted rather than invented: a per-request value would silently
slide the freshness lifetime forward on every fetch.

Only one op token in a 400 body is on record — `fill` prints as `fil`. That
single measured value is reproduced; the other ops print their own names rather
than generalising a truncation rule from one sample.

Unemulated, and deliberately: `Age` (this is a single tier, so there is no
shared cache in front to age against — §12 makes it optional), `HEAD`,
multi-part and out-of-range `Range` requests, and `x-wixmp-trace`.

## 14. Not implemented

**A persistent rendition cache.** The derivation cache in §10 is in-process
only, so it is empty after a restart and not shared between replicas. An
on-disk or shared store would make derivation actually common the way it is on
the CDN; the `wixcache.Store` interface exists for that.

**`quality_auto`.** Parsed and ignored — see §2. The spec establishes that only
`auto` vs not-`auto` matters, but never what `auto` changes.
