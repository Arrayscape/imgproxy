# Base image built from docker/wix/Dockerfile: stock libvips 8.15.5 plus the two
# OP-SPEC.md §1 patches, built with the ORC vector path, Highway disabled, and
# no floating-point contraction. Those are part of the Wix transform, not
# preferences -- upstream's base ships 8.18.x built with Highway and default
# FMA, which resamples differently in the low bits.
#
# Parameterised so an unmodified upstream base can be substituted. Doing so
# still builds and runs, but the Wix route will NOT reproduce the CDN:
#   --build-arg BASE_IMAGE=ghcr.io/imgproxy/imgproxy-base
#   --build-arg BASE_IMAGE_VERSION=v4.1.5
ARG BASE_IMAGE="ghcr.io/arrayscape/imgproxy-base-wix"
ARG BASE_IMAGE_VERSION="v4.1.5-vips8.15.5-wix1"

FROM ${BASE_IMAGE}:${BASE_IMAGE_VERSION} AS build

ENV CGO_ENABLED=1

COPY . .
RUN bash -c 'go build -v -ldflags "-s -w" -o /opt/imgproxy/bin/imgproxy ./cli'

# Wix's own output sRGB profile, decoded from the base64 the repo carries. Used
# as the icc_transform DESTINATION for profiled masters; libvips' built-in sRGB
# is measurably not the same profile. The size check is deliberate -- a
# truncated or mis-decoded profile would silently change colour on every
# profiled master.
RUN grep -v '^#' wix/profiles/wix-srgb.icc.b64 | tr -d '\n' | base64 -d \
      > /opt/imgproxy/share/wix-srgb.icc \
 && [ "$(stat -c%s /opt/imgproxy/share/wix-srgb.icc)" = 456 ]

# Remove unnecessary files
RUN rm -rf /opt/imgproxy/lib/pkgconfig /opt/imgproxy/lib/cmake

# ==================================================================================================
# AWS Lambda adapter

FROM public.ecr.aws/awsguru/aws-lambda-adapter:0.9.1 AS lambda-adapter

# ==================================================================================================
# Final image

# debian:bookworm-slim rather than ubuntu:noble, because our libvips is built
# against bookworm's shared libraries (docker/wix/Dockerfile) and links them
# from the distro rather than vendoring them under /opt/imgproxy/lib the way
# upstream's base does. Matching the distro guarantees the soname versions the
# build linked against are the ones present at runtime.
FROM debian:bookworm-slim
LABEL maintainer="Sergey Alexandrovich <darthsim@gmail.com>"

# The runtime counterparts of the -dev packages docker/wix/Dockerfile builds
# against, derived from `ldd libvips.so.42` rather than guessed.
RUN apt-get update \
  && apt-get upgrade -y \
  && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
  bash ca-certificates media-types \
  fontconfig-config fonts-dejavu-core \
  libjemalloc2 libtcmalloc-minimal4 \
  libacl1 libarchive13 libblkid1 libbrotli1 libbsd0 libcairo-gobject2 libcairo2 \
  libcgif0 libdatrie1 libdeflate0 libexif12 libffi8 libfontconfig1 libfreetype6 \
  libfribidi0 libgdk-pixbuf-2.0-0 libglib2.0-0 libgomp1 libgraphite2-3 \
  libharfbuzz0b libicu72 libimagequant0 libjbig0 libjpeg62-turbo liblcms2-2 \
  liblerc4 liblz4-1 libmd0 libmount1 libnettle8 libopenjp2-7 liborc-0.4-0 \
  libpango-1.0-0 libpangocairo-1.0-0 libpangoft2-1.0-0 libpcre2-8-0 \
  libpixman-1-0 libpng16-16 librsvg2-2 libstdc++6 libthai0 libtiff6 \
  libwebp7 libwebpdemux2 libwebpmux3 libx11-6 libxau6 libxcb-render0 \
  libxcb-shm0 libxcb1 libxdmcp6 libxext6 libxml2 libxrender1 libzstd1 \
  # HEIF/AVIF and JXL are libvips DYNAMIC MODULES (vips-heif.so, vips-jxl.so).
  # Without these the modules fail to load and the operations vanish at
  # runtime as `class "heifsave_target" not found` -- a 500, not a build error.
  libheif1 libaom3 libdav1d6 libde265-0 libx265-199 libnuma1 \
  libjxl0.7 libhwy1 \
  && ln -s /usr/lib/$(uname -m)-linux-gnu/libjemalloc.so.2 /usr/local/lib/libjemalloc.so \
  && ln -s /usr/lib/$(uname -m)-linux-gnu/libtcmalloc_minimal.so.4 /usr/local/lib/libtcmalloc_minimal.so \
  && rm -rf /var/lib/apt/lists/* \
  && rm -rf /etc/fonts/conf.d/10-sub-pixel-rgb.conf /etc/fonts/conf.d/11-lcdfilter-default.conf

COPY --from=build /opt/imgproxy/bin/imgproxy /opt/imgproxy/bin/
COPY --from=build /opt/imgproxy/lib /opt/imgproxy/lib
RUN ln -s /opt/imgproxy/bin/imgproxy /usr/local/bin/imgproxy

# The loader needs to be told where libvips lives: our base installs it under
# /opt/imgproxy/lib and registers it via ld.so.conf, but this final stage is a
# fresh image that only receives the copied directory. Without this the binary
# starts and immediately dies with "libvips.so.42: cannot open shared object
# file", which the build itself would not otherwise catch.
RUN echo /opt/imgproxy/lib > /etc/ld.so.conf.d/imgproxy.conf \
 && ldconfig \
 && ldd /opt/imgproxy/bin/imgproxy | grep -q '^\s*libvips\.so\.42 => /opt/imgproxy/lib/' \
 && ! ldd /opt/imgproxy/bin/imgproxy | grep 'not found' \
 && for m in /opt/imgproxy/lib/vips-modules-*/*.so; do \
      ldd "$m" | grep 'not found' && { echo "unresolved deps in $m" >&2; exit 1; }; \
      true; \
    done

COPY docker/entrypoint.sh /usr/local/bin/

COPY docker/imgproxy-build-package /usr/local/bin/

# AWS Lambda adapter
COPY --from=lambda-adapter /lambda-adapter /opt/extensions/lambda-adapter

COPY NOTICE /opt/imgproxy/share/doc/
COPY --from=build /opt/imgproxy/share/wix-srgb.icc /opt/imgproxy/share/

ENV VIPS_WARNING=0
ENV MALLOC_ARENA_MAX=2
ENV FONTCONFIG_PATH="/etc/fonts"
ENV IMGPROXY_MALLOC="malloc"
ENV AWS_LWA_READINESS_CHECK_PATH="/health"
ENV AWS_LWA_INVOKE_MODE="response_stream"
ENV AWS_LWA_ASYNC_INIT="true"

# Disable SVE on ARM64. SVE is slower than NEON on Amazon Graviton 3.
# Measured safe for the Wix transform: 12/12 renditions identical (OP-SPEC §2.1).
ENV VIPS_VECTOR=167772160

# REQUIRED by the Wix transform, and part of it rather than a tuning knob.
# reducev accumulates Y within a strip and restarts at each boundary, so the
# strip height decides which output rows land exactly on a phase tie. libvips
# hardcodes 10; the CDN behaves as 16. Running without it silently produces a
# different transform. See OP-SPEC.md §2.
#
# Only has an effect on a libvips carrying docker/wix/0001; harmless otherwise.
ENV VIPS_TILE_HEIGHT=16

# VIPS_NOVECTOR must NEVER be set: it disables the ORC vector path and changes
# 6 of 12 measured renditions. The Wix handler refuses to start if it is.

RUN groupadd -r imgproxy \
  && useradd -r -u 999 -g imgproxy imgproxy \
  && mkdir -p /var/cache/fontconfig \
  && chmod 777 /var/cache/fontconfig
USER 999

ENTRYPOINT [ "entrypoint.sh" ]
CMD ["imgproxy"]

EXPOSE 8080
