#!/usr/bin/env bash
# Build Lasso.app, the Mac app, on the Mac with the Command Line Tools only:
# swift build, then assemble the bundle (the window server will not talk to a
# bare binary) and sign it.
#
#   Scripts/build-app.sh [release|debug]
#
# Signing uses the self-signed "LassoDesktop Dev" identity from
# make-signing-identity.sh, so macOS keeps the app's microphone and
# screen-recording grants across rebuilds. Without it the bundle is signed
# ad-hoc and those grants are lost on every build.
#
# A release (Scripts/release.sh) sets instead:
#   LASSO_DESKTOP_VERSION   the version (lasso's), CFBundleShortVersionString and CFBundleVersion
#   SIGN_IDENTITY           "Developer ID Application: …", with SIGN_KEYCHAIN holding it:
#                           hardened runtime and a secure timestamp, as notarization needs
#   SPARKLE_FEED, SPARKLE_ED_KEY   the update feed and its public key; without
#                           them the app never checks for updates (a dev build)
set -euo pipefail
cd "$(dirname "$0")/.."

CONFIG="${1:-release}"
APP="dist/Lasso.app"
KC="$HOME/Library/Keychains/lasso-desktop-signing.keychain-db"
VERSION="${LASSO_DESKTOP_VERSION:-0.1.0}"
BUILD="${LASSO_DESKTOP_VERSION:-$(date +%Y%m%d%H%M%S)}"

swift build -c "$CONFIG" --arch arm64
BIN="$(swift build -c "$CONFIG" --arch arm64 --show-bin-path)/Lasso"

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources" "$APP/Contents/Frameworks"
cp "$BIN" "$APP/Contents/MacOS/Lasso"
sed -e "s/@VERSION@/$VERSION/" -e "s/@BUILD@/$BUILD/" \
  -e "s|@FEED@|${SPARKLE_FEED:-}|" -e "s|@EDKEY@|${SPARKLE_ED_KEY:-}|" Scripts/Info.plist.in >"$APP/Contents/Info.plist"
# Sparkle ships as an XCFramework through SwiftPM; the executable finds it
# through the @executable_path/../Frameworks rpath Package.swift sets.
SPARKLE_FW="$(find .build/artifacts -type d -name Sparkle.framework -path '*macos*' | head -n1)"
[ -n "$SPARKLE_FW" ] || { echo "Sparkle.framework not found under .build/artifacts" >&2; exit 1; }
ditto "$SPARKLE_FW" "$APP/Contents/Frameworks/Sparkle.framework"
cp -R Spec "$APP/Contents/Resources/Spec"
cp -R Resources/Fonts "$APP/Contents/Resources/Fonts"
cp -R Resources/Sounds "$APP/Contents/Resources/Sounds"
cp -R Resources/Brand "$APP/Contents/Resources/Brand"

# The app icon: Resources/AppIcon.png (the EXA monogram on the rail's ink,
# 1024 px) cut into an iconset and packed with the macOS tools.
ICONSET="$(mktemp -d)/AppIcon.iconset"
mkdir -p "$ICONSET"
for s in 16 32 128 256 512; do
  sips -z $s $s Resources/AppIcon.png --out "$ICONSET/icon_${s}x${s}.png" >/dev/null
  sips -z $((s * 2)) $((s * 2)) Resources/AppIcon.png --out "$ICONSET/icon_${s}x${s}@2x.png" >/dev/null
done
iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/AppIcon.icns"
rm -rf "$(dirname "$ICONSET")"

# Sign Sparkle's pieces one by one, innermost first (--deep would sign them
# with the app's settings), then the app.
sign_all() {
  local fw="$APP/Contents/Frameworks/Sparkle.framework/Versions/B"
  "$@" "$fw/XPCServices/Installer.xpc"
  "$@" --preserve-metadata=entitlements "$fw/XPCServices/Downloader.xpc"
  "$@" "$fw/Autoupdate"
  "$@" "$fw/Updater.app"
  "$@" "$APP/Contents/Frameworks/Sparkle.framework"
  "$@" --identifier com.orangecountyai.Lasso "$APP"
}

if [ -n "${SIGN_IDENTITY:-}" ]; then
  # A release: Developer ID, hardened runtime, secure timestamp.
  sign_all codesign --force --sign "$SIGN_IDENTITY" ${SIGN_KEYCHAIN:+--keychain "$SIGN_KEYCHAIN"} --options runtime --timestamp
  echo "signed with: $SIGN_IDENTITY"
else
  IDENTITY=""
  if [ -f "$KC" ]; then
    IDENTITY=$(security find-identity -p codesigning "$KC" 2>/dev/null | awk -F'"' '/LassoDesktop Dev/ {print $2; exit}')
  fi
  if [ -n "$IDENTITY" ]; then
    if ! sign_all codesign --force --sign "$IDENTITY" --keychain "$KC" --timestamp=none 2>/tmp/lasso-desktop-codesign.err; then
      cat /tmp/lasso-desktop-codesign.err >&2
      echo "signing failed; if the keychain is locked, run Scripts/dev.sh unlock from titan" >&2
      exit 1
    fi
    echo "signed with: $IDENTITY"
  else
    sign_all codesign --force --sign -
    echo "WARNING: no LassoDesktop Dev identity; signed ad-hoc. macOS will forget this app's" >&2
    echo "         permission grants on every rebuild. See Scripts/make-signing-identity.sh." >&2
  fi
fi
codesign --verify --strict "$APP"
echo "built $APP ($CONFIG)"
