# Built by docker/wix/build-base.sh: imgproxy's own base image build, forked to
# change libvips only -- 8.15.5 with the OP-SPEC.md §1 patch, the ORC vector
# path instead of Highway, and no floating-point contraction. Every other
# dependency stays at upstream's pinned version.
#
# Substituting the unmodified upstream base still builds and runs, but the Wix
# route will refuse to start rather than serve a transform that silently does
# not reproduce the CDN (set IMGPROXY_WIX_ALLOW_UNVERIFIED_LIBVIPS=true to
# override):
#   --build-arg BASE_IMAGE=ghcr.io/imgproxy/imgproxy-base
#   --build-arg BASE_IMAGE_VERSION=v4.1.5
ARG BASE_IMAGE="ghcr.io/arrayscape/imgproxy-base-wix"
ARG BASE_IMAGE_VERSION="v4.1.5-vips8.15.5-wix2"

FROM ${BASE_IMAGE}:${BASE_IMAGE_VERSION} AS build

ENV CGO_ENABLED=1

COPY . .
RUN bash -c 'go build -v -ldflags "-s -w" -o /opt/imgproxy/bin/imgproxy ./cli'

# Wix's own output sRGB profile, decoded from the base64 the repo carries. It is
# the icc_transform DESTINATION for profiled masters; libvips' built-in sRGB is
# measurably a different profile. The size assertion is deliberate -- a
# truncated decode would silently change colour on every profiled master.
RUN mkdir -p /opt/imgproxy/share \
 && grep -v '^#' wix/profiles/wix-srgb.icc.b64 | tr -d '\n' | base64 -d \
      > /opt/imgproxy/share/wix-srgb.icc \
 && [ "$(stat -c%s /opt/imgproxy/share/wix-srgb.icc)" = 456 ]

# Remove unnecessary files
RUN rm -rf /opt/imgproxy/lib/pkgconfig /opt/imgproxy/lib/cmake

# ==================================================================================================
# AWS Lambda adapter

FROM public.ecr.aws/awsguru/aws-lambda-adapter:0.9.1 AS lambda-adapter

# ==================================================================================================
# Final image

FROM public.ecr.aws/ubuntu/ubuntu:noble
LABEL maintainer="Sergey Alexandrovich <darthsim@gmail.com>"

RUN apt-get update \
  && apt-get upgrade -y \
  && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
  bash \
  ca-certificates \
  libstdc++6 \
  fontconfig-config \
  fonts-dejavu-core \
  media-types \
  libjemalloc2 \
  libtcmalloc-minimal4 \
  && ln -s /usr/lib/$(uname -m)-linux-gnu/libjemalloc.so.2 /usr/local/lib/libjemalloc.so \
  && ln -s /usr/lib/$(uname -m)-linux-gnu/libtcmalloc_minimal.so.4 /usr/local/lib/libtcmalloc_minimal.so \
  && rm -rf /var/lib/apt/lists/* \
  && rm -rf /etc/fonts/conf.d/10-sub-pixel-rgb.conf /etc/fonts/conf.d/11-lcdfilter-default.conf

COPY --from=build /opt/imgproxy/bin/imgproxy /opt/imgproxy/bin/
COPY --from=build /opt/imgproxy/lib /opt/imgproxy/lib
RUN ln -s /opt/imgproxy/bin/imgproxy /usr/local/bin/imgproxy

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

# Disable SVE on ARM64. SVE is slower than NEON on Amazon Graviton 3
ENV VIPS_VECTOR=167772160

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
