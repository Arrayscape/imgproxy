#!/usr/bin/env bash
#
# Build the imgproxy base image the Wix emulation needs.
#
# This is a thin FORK of imgproxy's own base image build, not a replacement.
# Upstream builds every dependency from source into /opt/imgproxy/lib, which is
# what lets the runtime image stay ubuntu:noble and copy one directory. We keep
# all of that and change only what OP-SPEC.md §1 requires of libvips:
#
#   1. libvips 8.15.5 instead of 8.18.5
#   2. the webpsave near-lossless-level patch
#   3. the ORC vector path instead of Highway
#   4. -ffp-contract=off for the libvips compile
#
# (3) is the only change that adds a dependency: upstream builds Highway and no
# ORC at all, while the CDN's resample results come from ORC's fixed-point path.
# Highway and FMA contraction each change results in the low bits.
#
# Everything else -- libjpeg, libpng, libwebp, lcms2, libtiff, libjxl, glib and
# the rest -- stays at upstream's pinned versions. If the corpus later shows a
# byte difference, that narrows it to a specific library rather than a distro.
#
#   ./docker/wix/build-base.sh [tag]
set -euo pipefail

UPSTREAM_REPO=${UPSTREAM_REPO:-https://github.com/imgproxy/imgproxy-docker-base.git}
# Pinned so the fork is reproducible. Bump deliberately, then re-score.
UPSTREAM_REF=${UPSTREAM_REF:-6007701fa019c3b587269747982e6cbda32d3340}

VIPS_VERSION=${VIPS_VERSION:-8.15.5}
# 0.4.33 is what the corpus was measured against (Debian bookworm's orc).
ORC_VERSION=${ORC_VERSION:-0.4.33}

TAG=${1:-ghcr.io/arrayscape/imgproxy-base-wix:v4.1.5-vips${VIPS_VERSION}-wix1}

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

echo "==> cloning ${UPSTREAM_REPO} @ ${UPSTREAM_REF}"
git clone --quiet "$UPSTREAM_REPO" "$WORK/base"
git -C "$WORK/base" checkout --quiet "$UPSTREAM_REF"

# sed is deliberately anchored on exact upstream text and asserted afterwards:
# if upstream moves, this must fail loudly rather than silently build something
# that is not the transform.
require() { grep -qF "$2" "$1" || { echo "FORK FAILED: expected text missing from $1: $2" >&2; exit 1; }; }

cd "$WORK/base"

# --- 1. libvips version ------------------------------------------------------
require versions.sh "export VIPS_VERSION="
# python rather than sed -i: GNU and BSD sed disagree on the -i argument, and
# this script runs on developer machines as well as in CI.
VIPS_VERSION="$VIPS_VERSION" ORC_VERSION="$ORC_VERSION" python3 - <<'PY'
import os, pathlib, re
p = pathlib.Path("versions.sh"); s = p.read_text()
s, n = re.subn(r"^export VIPS_VERSION=.*$",
               "export VIPS_VERSION=" + os.environ["VIPS_VERSION"], s, flags=re.M)
assert n == 1, "expected exactly one VIPS_VERSION line, found %d" % n
s += "export ORC_VERSION=%s\n" % os.environ["ORC_VERSION"]
p.write_text(s)
PY

# --- 2. the two libvips patches ---------------------------------------------
# Upstream's Dockerfile already does `COPY ... *.patch ./` into /root.
cp "$HERE"/0002-webpsave-separate-near-lossless-level.patch .

# --- 3. `patch`, which upstream's deps stage does not install ----------------
require Dockerfile "    nasm \\"
python3 - <<'INNER'
import pathlib
p = pathlib.Path("Dockerfile"); s = p.read_text()
old = "    nasm \\\n"
assert s.count(old) == 1, "expected exactly one nasm line in the deps stage"
p.write_text(s.replace(old, old + "    patch \\\n", 1))
INNER

# --- 4. ORC: a dependency upstream does not build ---------------------------
require download-deps.sh "print_download_stage vips \$VIPS_VERSION"
python3 - <<'PY'
import pathlib
p = pathlib.Path("download-deps.sh"); s = p.read_text()
s = s.replace('''print_download_stage vips $VIPS_VERSION''', '''print_download_stage orc $ORC_VERSION
mkdir $DEPS_SRC/orc
cd $DEPS_SRC/orc
curl -Ls https://gstreamer.freedesktop.org/src/orc/orc-$ORC_VERSION.tar.xz \\
  | tar -xJC . --strip-components=1

print_download_stage vips $VIPS_VERSION''', 1)
p.write_text(s)
PY

require build-deps.sh "print_build_stage vips \$VIPS_VERSION"
python3 - <<'PY'
import pathlib
p = pathlib.Path("build-deps.sh"); s = p.read_text()

orc = '''print_build_stage orc $ORC_VERSION
cd $DEPS_SRC/orc
CFLAGS="${CFLAGS} -O3" \\
meson setup _build \\
  --buildtype=release \\
  --strip \\
  --wrap-mode=nofallback \\
  --prefix=$TARGET_PATH \\
  --libdir=lib \\
  -Dorc-test=disabled \\
  -Dbenchmarks=disabled \\
  -Dexamples=disabled \\
  -Dtests=disabled \\
  -Dtools=disabled
ninja -C _build
ninja -C _build install
rm -rf $TARGET_PATH/lib/*.a $TARGET_PATH/lib/*.la

'''

old = '''print_build_stage vips $VIPS_VERSION
cd $DEPS_SRC/vips
CFLAGS="${CFLAGS} -O3" CXXFLAGS="${CXXFLAGS} -O3" \\
meson setup _build \\
  --buildtype=release \\
  --strip \\
  --wrap-mode=nofallback \\
  --prefix=$TARGET_PATH \\
  --libdir=lib \\
  -Ddocs=false \\
  -Dintrospection=disabled \\
  -Dmodules=disabled'''
assert old in s, "upstream vips build stage changed; re-check the fork"

new = orc + '''print_build_stage vips $VIPS_VERSION
cd $DEPS_SRC/vips
# -Ddocs was only added in 8.16; 8.15.5 spells it -Dgtk_doc/-Ddoxygen, both
# off by default in a release tarball, so the option is simply dropped.
# OP-SPEC.md §1. The reducev tile-height patch this build used to carry is
# gone: it only mattered under sequential access, and the pipeline now opens
# images with access=random (§2), which removes the line cache entirely.
patch -p1 < /root/0002-webpsave-separate-near-lossless-level.patch
# -ffp-contract=off is scoped to libvips: FMA contraction changes resample
# results, and the reference build applies it to libvips only.
CFLAGS="${CFLAGS} -O3 -ffp-contract=off" CXXFLAGS="${CXXFLAGS} -O3 -ffp-contract=off" \\
meson setup _build \\
  --buildtype=release \\
  --strip \\
  --wrap-mode=nofallback \\
  --prefix=$TARGET_PATH \\
  --libdir=lib \\
  -Dintrospection=disabled \\
  -Dmodules=disabled \\
  -Dorc=enabled \\
  -Dhighway=disabled'''
s = s.replace(old, new, 1)
p.write_text(s)
PY

echo "==> fork applied:"
echo "    libvips  $(grep '^export VIPS_VERSION=' versions.sh | cut -d= -f2)"
echo "    orc      $(grep '^export ORC_VERSION=' versions.sh | cut -d= -f2)"
echo "    patch    0002, -Dorc=enabled -Dhighway=disabled, -ffp-contract=off"
echo "==> building ${TAG}"
docker build -t "$TAG" .
echo "==> built ${TAG}"
