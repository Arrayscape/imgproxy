# imgproxy build base carrying the libvips the Wix CDN reproduction requires.
#
# This is NOT ghcr.io/imgproxy/imgproxy-base: that ships libvips 8.18.x built
# with Highway and default FMA contraction, and both change resample results in
# the low bits. OP-SPEC.md §1 pins stock 8.15.5 with the ORC vector path,
# Highway off, and -ffp-contract=off. Those are part of the transform, not
# preferences -- with FMA on or ORC off the score drops by roughly 5%.
#
# Two patches, both applying cleanly to the stock 8.15.5 tarball:
#   0001  reducev tile height from VIPS_TILE_HEIGHT (the CDN behaves as 16,
#         libvips hardcodes 10)
#   0002  webpsave near-lossless level independent of Q (the CDN emits
#         near_lossless=80 with quality=75, which libvips cannot express)
#
# Everything else the pipeline needs is already upstream in 8.15: the lanczos
# coefficients (#3553), the sRGB<->XYZ matrices (#3527/#3491), the sharpen
# sigma bound (#3270).
#
# Deliberately absent: libvips#4909, the enlargement half-pixel fix. Wix runs
# stock libvips and shares the displacement, so patching it moves us AWAY from
# the CDN on every enlargement. See DECISIONS.md.
#
#   docker build -f docker/wix/Dockerfile -t ghcr.io/arrayscape/imgproxy-base-wix:local .

# The Debian release is a deliberate pin, not an accident. OP-SPEC.md §1 pins
# the libvips build, but the distro decides what that build links against, and
# two of those libraries land directly on what we are reproducing:
#
#   zlib      1.2.13 (bookworm) -> 1.3.1 (trixie)   PNG IDAT *is* deflate output
#   liblcms2  2.14   (bookworm) -> 2.16   (trixie)  does the ICC transform
#
# bookworm is what the 1341/1341 corpus was measured on. Moving to trixie is a
# re-verification job, not a version bump: build both, render the corpus,
# compare. Note trixie's 64-bit time_t transition also renames packages
# (libglib2.0-0t64 etc), so the apt list below needs work as well.
ARG DEBIAN_RELEASE=bookworm

FROM debian:${DEBIAN_RELEASE}

ARG VIPS_VERSION=8.15.5
ARG GO_VERSION=1.26.6
ARG CONTRACT=off
ARG ORC=enabled
ARG HIGHWAY=disabled

ENV PREFIX=/opt/imgproxy

RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      build-essential meson ninja-build pkg-config wget ca-certificates patch git \
      libglib2.0-dev libexpat1-dev libjpeg62-turbo-dev libpng-dev libtiff-dev \
      libwebp-dev liblcms2-dev libexif-dev libgsf-1-dev libgif-dev librsvg2-dev \
      liborc-0.4-dev libheif-dev libjxl-dev libcgif-dev libimagequant-dev \
      libarchive-dev libopenjp2-7-dev \
      icc-profiles-free colord-data python3 cmake webp \
 && rm -rf /var/lib/apt/lists/*

# Go, for the imgproxy build stage that uses this image.
RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in amd64) goarch=amd64 ;; arm64) goarch=arm64 ;; *) echo "unsupported: $arch" >&2; exit 1 ;; esac; \
    wget -qO /tmp/go.tgz "https://go.dev/dl/go${GO_VERSION}.linux-${goarch}.tar.gz"; \
    tar -C /usr/local -xzf /tmp/go.tgz; \
    rm /tmp/go.tgz

WORKDIR /src
RUN wget -q https://github.com/libvips/libvips/releases/download/v${VIPS_VERSION}/vips-${VIPS_VERSION}.tar.xz \
 && tar xf vips-${VIPS_VERSION}.tar.xz
WORKDIR /src/vips-${VIPS_VERSION}

COPY docker/wix/0001-reducev-configurable-tile-height.patch /p1.patch
COPY docker/wix/0002-webpsave-separate-near-lossless-level.patch /p2.patch
RUN patch -p1 < /p1.patch && patch -p1 < /p2.patch

# Part of the transform, not tuning. See OP-SPEC.md §1.
ENV CFLAGS=-ffp-contract=${CONTRACT} \
    CXXFLAGS=-ffp-contract=${CONTRACT}

# --libdir=lib because Debian's meson defaults it to lib/<triplet>, and
# imgproxy's build expects a flat ${PREFIX}/lib (PKG_CONFIG_LIBDIR, RPATH).
RUN meson setup build --buildtype=release --prefix=${PREFIX} --libdir=lib \
      -Dintrospection=disabled -Dorc=${ORC} -Dhighway=${HIGHWAY} \
 && ninja -C build -j"$(nproc)" \
 && ninja -C build install \
 && echo "${PREFIX}/lib" > /etc/ld.so.conf.d/imgproxy.conf && ldconfig

ENV PATH=/usr/local/go/bin:${PREFIX}/bin:$PATH \
    PKG_CONFIG_LIBDIR=${PREFIX}/lib/pkgconfig:/usr/lib/pkgconfig:/usr/lib/aarch64-linux-gnu/pkgconfig:/usr/lib/x86_64-linux-gnu/pkgconfig:/usr/share/pkgconfig \
    LD_LIBRARY_PATH=${PREFIX}/lib

# Fail the build rather than ship a base that silently renders differently.
RUN set -eux; \
    vips --version; \
    vips --version | grep -q "vips-${VIPS_VERSION}"; \
    # patch 0002 must be present: without it lossless WebP cannot be expressed
    vips webpsave --help-operation 2>&1 | grep -q near-lossless-level; \
    # ORC on, Highway off: both are part of the transform (OP-SPEC Â§1)
    vips --vips-config | tr "," "\n" | grep -qi "with ORC: true"; \
    vips --vips-config | tr "," "\n" | grep -qi "with highway: false"; \
    # libspng and libpng produce different PNG bytes; the reference
    # build has libspng absent, so this build must too.
    vips --vips-config | tr "," "\n" | grep -qi "with libspng: false"; \
    go version

WORKDIR /work
