#!/usr/bin/env bash
# Builds installer/macos/Nexler.pkg from the universal nexler binary.
#
# macOS-only — pkgbuild, productbuild, sips, and iconutil are all
# macOS-native tools (part of Xcode Command Line Tools, preinstalled on
# GitHub's macos-latest runner image). Invoked by
# .github/workflows/release.yml's macos-installer job; cannot be run or
# tested from the Windows machine that wrote this script — see
# BUILDING.md for exactly what has and hasn't been verified.
#
# Expects installer/macos/payload/nexler-amd64 and nexler-arm64 to exist
# already (built by the CI job, or by hand per BUILDING.md) — this script
# only does the macOS-native packaging steps, not the Go cross-compile.
set -euo pipefail

VERSION="${1:?usage: build-pkg.sh <version>}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
PAYLOAD_DIR="$SCRIPT_DIR/payload"
BUILD_DIR="$SCRIPT_DIR/build"
STAGING="$BUILD_DIR/staging"
IDENTIFIER="com.klivolks.nexler"

for f in "$PAYLOAD_DIR/nexler-amd64" "$PAYLOAD_DIR/nexler-arm64"; do
  if [ ! -f "$f" ]; then
    echo "build-pkg.sh: missing $f — build both darwin/amd64 and darwin/arm64 first" >&2
    exit 1
  fi
done

rm -rf "$BUILD_DIR"
mkdir -p "$STAGING/bin" "$STAGING/share"

echo "==> Universal binary (lipo)"
lipo -create -output "$STAGING/bin/nexler" \
  "$PAYLOAD_DIR/nexler-amd64" "$PAYLOAD_DIR/nexler-arm64"
chmod +x "$STAGING/bin/nexler"
lipo -info "$STAGING/bin/nexler"

echo "==> Icon (.icns via sips + iconutil, from the committed master PNG)"
# sips only resizes the already-rasterized nexler-1024.png here — it's
# never asked to decode the source SVG itself, since sips's own SVG
# support is inconsistent across macOS versions and couldn't be verified
# from the non-macOS machine that wrote this script. The PNG was
# rasterized once, reliably, by build/gen-icons (a pure-Go tool) — see
# that directory's own doc comment.
ICONSET="$BUILD_DIR/nexler.iconset"
mkdir -p "$ICONSET"
MASTER_PNG="$SCRIPT_DIR/nexler-1024.png"
for spec in "16:icon_16x16.png" "32:icon_16x16@2x.png" "32:icon_32x32.png" "64:icon_32x32@2x.png" \
            "128:icon_128x128.png" "256:icon_128x128@2x.png" "256:icon_256x256.png" \
            "512:icon_256x256@2x.png" "512:icon_512x512.png" "1024:icon_512x512@2x.png"; do
  size="${spec%%:*}"
  name="${spec##*:}"
  sips -z "$size" "$size" "$MASTER_PNG" --out "$ICONSET/$name" >/dev/null
done
iconutil -c icns "$ICONSET" -o "$STAGING/share/nexler.icns"

# Defense in depth: postinstall must be executable for pkgbuild's --scripts
# to produce a runnable script inside the pkg. It's already chmod +x'd in
# git, but this re-asserts it at build time in case that mode is ever lost
# again (e.g. from an edit made on a non-macOS machine, which is how this
# was lost the first time and shipped a pkg that failed on every install
# with "postinstall ... isn't executable").
chmod +x "$SCRIPT_DIR/postinstall"

echo "==> pkgbuild"
pkgbuild \
  --root "$STAGING" \
  --identifier "$IDENTIFIER" \
  --version "$VERSION" \
  --scripts "$SCRIPT_DIR" \
  --install-location /usr/local/nexler \
  "$BUILD_DIR/component.pkg"

echo "==> productbuild"
sed "s/{{VERSION}}/$VERSION/g" "$SCRIPT_DIR/distribution.xml.tmpl" > "$BUILD_DIR/distribution.xml"
cp "$REPO_ROOT/README.md" "$BUILD_DIR/README.md"
cp "$REPO_ROOT/LICENSE" "$BUILD_DIR/LICENSE"
cp "$SCRIPT_DIR/welcome.html" "$BUILD_DIR/welcome.html"
productbuild \
  --distribution "$BUILD_DIR/distribution.xml" \
  --package-path "$BUILD_DIR" \
  --resources "$BUILD_DIR" \
  "$SCRIPT_DIR/Nexler.pkg"

echo "==> Done: $SCRIPT_DIR/Nexler.pkg"
