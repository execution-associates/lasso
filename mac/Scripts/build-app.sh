#!/usr/bin/env bash
# Build LassoDesktop.app on the Mac with the Command Line Tools only:
# swift build, then assemble the bundle (the window server will not talk to a
# bare binary) and sign it.
#
#   Scripts/build-app.sh [release|debug]
#
# Signing uses the self-signed "LassoDesktop Dev" identity from
# make-signing-identity.sh, so macOS keeps the app's microphone and
# screen-recording grants across rebuilds. Without it the bundle is signed
# ad-hoc and those grants are lost on every build.
set -euo pipefail
cd "$(dirname "$0")/.."

CONFIG="${1:-release}"
APP="dist/LassoDesktop.app"
KC="$HOME/Library/Keychains/lasso-desktop-signing.keychain-db"
VERSION="0.1.0"

swift build -c "$CONFIG" --arch arm64
BIN="$(swift build -c "$CONFIG" --arch arm64 --show-bin-path)/LassoDesktop"

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$BIN" "$APP/Contents/MacOS/LassoDesktop"
sed -e "s/@VERSION@/$VERSION/" -e "s/@BUILD@/$(date +%Y%m%d%H%M%S)/" Scripts/Info.plist.in >"$APP/Contents/Info.plist"
cp -R Spec "$APP/Contents/Resources/Spec"

IDENTITY=""
if [ -f "$KC" ]; then
  IDENTITY=$(security find-identity -p codesigning "$KC" 2>/dev/null | awk -F'"' '/LassoDesktop Dev/ {print $2; exit}')
fi
if [ -n "$IDENTITY" ]; then
  if ! codesign --force --sign "$IDENTITY" --keychain "$KC" --timestamp=none \
    --identifier com.orangecountyai.LassoDesktop "$APP" 2>/tmp/lasso-desktop-codesign.err; then
    cat /tmp/lasso-desktop-codesign.err >&2
    echo "signing failed; if the keychain is locked, run Scripts/dev.sh unlock from titan" >&2
    exit 1
  fi
  echo "signed with: $IDENTITY"
else
  codesign --force --sign - --identifier com.orangecountyai.LassoDesktop "$APP"
  echo "WARNING: no LassoDesktop Dev identity; signed ad-hoc. macOS will forget this app's" >&2
  echo "         permission grants on every rebuild. See Scripts/make-signing-identity.sh." >&2
fi
codesign --verify --strict "$APP"
echo "built $APP ($CONFIG)"
