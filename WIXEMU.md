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
| `IMGPROXY_WIX_ALLOW_WRONG_TILE_HEIGHT` | `false` | Permits a `VIPS_TILE_HEIGHT` other than 16 |

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
| `q_N` | implemented | Lossy quality, default 85. Ignored on the lossless WebP path, which fixes Q at 75 |
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

**WebP codec follows the stored master**, sniffed from magic bytes: a lossless
master (PNG) encodes to VP8L, a lossy one (JPEG, WebP) to VP8. Not alpha
presence, and not encode-both-and-keep-the-smaller.

## 4. The two behaviours most likely to surprise you

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

**Renditions are stripped.** Any `/v1/` URL returns a synthesised 11-tag EXIF
whitelist and nothing else: Orientation, XResolution, YResolution,
ResolutionUnit, YCbCrPositioning, ExifVersion, ComponentsConfiguration,
FlashpixVersion, ColorSpace, PixelXDimension, PixelYDimension. No GPS, no camera
make or model, no timestamps, no software, no serial numbers. libvips forwards
source metadata by default and also writes a `zTXt` chunk and a *second* `eXIf`
chunk; all three are removed. **This is a security control**, and it is covered
by a test that renders a master carrying camera and timestamp tags and asserts
none of it survives.

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

## 7. Build requirements

The transform depends on the libvips build, not just the code. `docker/wix/Dockerfile`
builds it:

- **stock libvips 8.15.5** plus two patches in `docker/wix/`:
  `0001` makes `reducev`'s line-cache tile height configurable, `0002` gives
  `webpsave` a near-lossless level independent of `Q`.
- `-Dorc=enabled -Dhighway=disabled`, `-ffp-contract=off`. Highway and FMA
  contraction each change resample results in the low bits.
- `VIPS_TILE_HEIGHT=16` at runtime. libvips defaults to 10; **running without it
  silently produces a different transform**, so the handler refuses to start if
  it is set to anything else.
- `VIPS_NOVECTOR` must never be set — the handler refuses to start if it is.
  `VIPS_VECTOR` and `VIPS_CONCURRENCY` are both measured safe.
- Host architecture is irrelevant given those flags: arm64 and amd64 were
  measured byte-identical across the whole corpus.

Deliberately **not** applied: libvips#4909, the enlargement half-pixel fix. Wix
runs stock libvips and shares the displacement, so patching it moves output
*away* from the CDN on every enlargement.

## 8. What is verified, and what is not

Against live CDN output, on the master render path:

| Area | Score |
|---|---|
| `fit` | 908 / 908 byte-exact |
| `fill` | 433 / 433 byte-exact |
| `crop` | 70 / 70 |
| `usm` | 684 / 685 |
| WebP VP8 / VP8L | 5/5 and 23/27 — **payload only**, the RIFF container was never compared |

**Not verified.** Treat these as best-effort, implemented from the specification
rather than measured:

- **AVIF** — the negotiation that selects it is exact; the encoder settings were
  never compared against the CDN.
- **JPEG output** — never scored.
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

## 9. Coverage against WIX-URL-SPEC

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
| §6 cache-derived renditions | implemented, **off by default** — see below |
| §7.1 renditions stripped | implemented |
| §7.2 originals not stripped | implemented, deliberately |
| §7.3 colour → Wix sRGB | implemented, including on a bare `crop` |

## 10. Cache-derived renditions (§6 / OP-SPEC §10)

The CDN caches transformed output and uses it as the input to later transforms,
which is how roughly half of a mature URL set is produced. This fork does the
same, **off by default**:

```
IMGPROXY_WIX_DERIVATION_CACHE=true
IMGPROXY_WIX_DERIVATION_CACHE_SIZE=536870912   # bytes, in-process
IMGPROXY_WIX_DERIVATION_MAX_DEPTH=1            # derive only from a master render
```

Off by default because **deriving deliberately changes output bytes**, and the
master path is the one measured byte-exact against the CDN. With it off, nothing
is ever served from a rendition — there is a test asserting exactly that.

**The ancestor is the source.** Both specs say "the pipeline is identical; only
the input differs" and "run through the same pipeline", so the URL's segments
are resolved against the ancestor's dimensions exactly as if it were the master
— not mapped from a master-relative plan into ancestor coordinates. That
distinction is not academic: the two disagreed on 38% of sampled cases, with
different scales, different drop counts and crops a pixel wider.

**Which ancestor gets used is our policy, not the CDN's.** The CDN's choice
depends on node-local cache state at the moment an entry was first created:
adjacent widths one pixel apart resolve differently, and once created an entry
is frozen and shared. That is not reproducible in principle, so we define a
policy that is a pure function of the request and the cache contents. An entry
is usable only if **all** of:

1. it has no effects baked in — a sharpened or blurred rendition is not a
   resamplable source;
2. it is PNG — deriving from a lossy re-encode compounds artefacts;
3. **its source rectangle contains the region the new request reads.** OP-SPEC
   §10 suggests "smallest rendition at least as large as the target", but that
   alone is unsound: two `fill`s of different aspect ratios can both be larger
   while covering disjoint parts of the master. This is the missing predicate;
4. it is at least as large as the target on both axes — never upsample from a
   rendition, the master still has the detail;
5. its derivation depth is under the cap;
6. it covers the whole master. Since the ancestor is treated as the source, a
   cropped ancestor would re-frame every later transform against the crop
   rather than the master. Whether the CDN does that is unmeasured, so it is
   refused rather than guessed at.

Among those, the smallest by area wins, ties broken on the source rectangle so
the choice does not depend on insertion order or map iteration.

Responses carry `X-Wix-Source: master | derived | cache` so which input produced
them is observable from outside.

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

## 11. Not implemented

**A persistent rendition cache.** The derivation cache in §10 is in-process
only, so it is empty after a restart and not shared between replicas. An
on-disk or shared store would make derivation actually common the way it is on
the CDN; the `wixcache.Store` interface exists for that.

**`quality_auto`.** Parsed and ignored — see §2. The spec establishes that only
`auto` vs not-`auto` matters, but never what `auto` changes.
