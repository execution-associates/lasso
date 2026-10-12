#!/usr/bin/env bash
# Build a LassoDesktop release on minime: Developer ID signed, notarized,
# stapled, in a DMG, with the Sparkle appcast that announces it.
#
#   Scripts/release.sh <version>        # lasso's version, e.g. 6.5.0
#
# Writes dist/LassoDesktop-<version>.dmg and dist/appcast.xml. Scripts/publish.sh
# runs it from titan and attaches both to lasso's GitHub release v<version>, so
# the app's feed, .../releases/latest/download/appcast.xml, names the newest.
#
# The signing identity is imported from fnox into a throwaway keychain and
# notarization uses an API key, so the login keychain is never needed and it
# runs over ssh. The pattern is Recap's (macos/release.sh there).
# Secrets come from fnox by name, never from the environment's values:
#   SIGN_P12_SECRET, SIGN_P12_PASSWORD_SECRET   the Developer ID .p12 (base64) and its password
#   ASC_KEY_ID_SECRET, ASC_ISSUER_SECRET, ASC_KEY_P8_SECRET   App Store Connect API key (notarization)
#   SPARKLE_KEY_SECRET                          the EdDSA private key updates are signed with
# The certificate and notary key are Recap's (same Developer ID, JF4UNR89NW);
# the update key is LassoDesktop's own. Sparkle's public key is committed in
# Scripts/sparkle-public-key.txt.
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:?usage: Scripts/release.sh <version>}"
REPO="execution-associates/lasso"
FEED="https://github.com/$REPO/releases/latest/download/appcast.xml"
DMG_NAME="LassoDesktop-$VERSION.dmg"
DMG_URL="https://github.com/$REPO/releases/download/v$VERSION/$DMG_NAME"

SIGN_P12_SECRET="${SIGN_P12_SECRET:-RECAP_SIGN_P12_B64}"
SIGN_P12_PASSWORD_SECRET="${SIGN_P12_PASSWORD_SECRET:-RECAP_SIGN_P12_PASSWORD}"
ASC_KEY_ID_SECRET="${ASC_KEY_ID_SECRET:-RECAP_ASC_KEY_ID}"
ASC_ISSUER_SECRET="${ASC_ISSUER_SECRET:-RECAP_ASC_ISSUER_ID}"
ASC_KEY_P8_SECRET="${ASC_KEY_P8_SECRET:-RECAP_ASC_KEY_P8}"
SPARKLE_KEY_SECRET="${SPARKLE_KEY_SECRET:-LASSO_DESKTOP_SPARKLE_PRIVATE_KEY}"

ED_PUBLIC="$(tr -d '[:space:]' <Scripts/sparkle-public-key.txt 2>/dev/null || true)"
[ -n "$ED_PUBLIC" ] || { echo "Scripts/sparkle-public-key.txt is missing: generate the update key first" >&2; exit 1; }

WORK="$(mktemp -d)"
KEYCHAIN="$HOME/Library/Keychains/lasso-desktop-release-$$.keychain-db"
ORIG_KEYCHAINS="$(security list-keychains -d user | tr -d '"')"
cleanup() {
  # shellcheck disable=SC2086
  security list-keychains -d user -s $ORIG_KEYCHAINS >/dev/null 2>&1 || true
  security delete-keychain "$KEYCHAIN" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

# 1. The Developer ID identity, in a keychain that exists for this run only.
KC_PW="$(openssl rand -hex 20)"
fnox get "$SIGN_P12_SECRET" | base64 --decode >"$WORK/id.p12"
security create-keychain -p "$KC_PW" "$KEYCHAIN"
security set-keychain-settings -lut 21600 "$KEYCHAIN"
security unlock-keychain -p "$KC_PW" "$KEYCHAIN"
security import "$WORK/id.p12" -k "$KEYCHAIN" -P "$(fnox get "$SIGN_P12_PASSWORD_SECRET")" \
  -T /usr/bin/codesign -T /usr/bin/security >/dev/null
security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$KC_PW" "$KEYCHAIN" >/dev/null
# shellcheck disable=SC2086
security list-keychains -d user -s "$KEYCHAIN" $ORIG_KEYCHAINS
IDENTITY="$(security find-identity -v -p codesigning "$KEYCHAIN" | sed -n 's/.*"\(Developer ID Application: [^"]*\)".*/\1/p' | head -n1)"
[ -n "$IDENTITY" ] || { echo "no Developer ID Application identity in $SIGN_P12_SECRET" >&2; exit 1; }

# 2. Build and sign: hardened runtime, timestamps, the feed and its key.
LASSO_DESKTOP_VERSION="$VERSION" SIGN_IDENTITY="$IDENTITY" SIGN_KEYCHAIN="$KEYCHAIN" \
  SPARKLE_FEED="$FEED" SPARKLE_ED_KEY="$ED_PUBLIC" Scripts/build-app.sh release
APP="dist/LassoDesktop.app"

# 3. Notarize the app and staple the ticket to it.
fnox get "$ASC_KEY_P8_SECRET" >"$WORK/AuthKey.p8"
NOTARY=(--key "$WORK/AuthKey.p8" --key-id "$(fnox get "$ASC_KEY_ID_SECRET")" --issuer "$(fnox get "$ASC_ISSUER_SECRET")")
ditto -c -k --keepParent "$APP" "$WORK/notarize.zip"
xcrun notarytool submit "$WORK/notarize.zip" "${NOTARY[@]}" --wait
xcrun stapler staple "$APP"

# 4. The DMG: the app and an Applications link; signed, notarized, stapled.
STAGE="$WORK/dmg"
mkdir -p "$STAGE"
ditto "$APP" "$STAGE/LassoDesktop.app"
ln -s /Applications "$STAGE/Applications"
rm -f "dist/$DMG_NAME"
hdiutil create -volname "Lasso" -srcfolder "$STAGE" -ov -format UDZO -quiet "dist/$DMG_NAME"
codesign --sign "$IDENTITY" --keychain "$KEYCHAIN" --timestamp "dist/$DMG_NAME"
xcrun notarytool submit "dist/$DMG_NAME" "${NOTARY[@]}" --wait
xcrun stapler staple "dist/$DMG_NAME"
spctl --assess --type open --context context:primary-signature -v "dist/$DMG_NAME"

# 5. The appcast: one item, the DMG's EdDSA signature and length from sign_update.
SIGN_UPDATE="$(find .build/artifacts -path '*Sparkle/bin/sign_update' | head -n1)"
[ -x "$SIGN_UPDATE" ] || { echo "Sparkle's sign_update not found under .build/artifacts" >&2; exit 1; }
fnox get "$SPARKLE_KEY_SECRET" | tr -d "\n" >"$WORK/sparkle.key"
ED_ATTRS="$("$SIGN_UPDATE" -f "$WORK/sparkle.key" "dist/$DMG_NAME")"
cat >dist/appcast.xml <<XML
<?xml version="1.0" encoding="utf-8"?>
<rss version="2.0" xmlns:sparkle="http://www.andymatuschak.org/xml-namespaces/sparkle">
  <channel>
    <title>LassoDesktop</title>
    <link>$FEED</link>
    <item>
      <title>LassoDesktop $VERSION</title>
      <link>https://github.com/$REPO/releases/tag/v$VERSION</link>
      <sparkle:version>$VERSION</sparkle:version>
      <sparkle:shortVersionString>$VERSION</sparkle:shortVersionString>
      <sparkle:minimumSystemVersion>14.0</sparkle:minimumSystemVersion>
      <pubDate>$(LC_ALL=C date -u "+%a, %d %b %Y %H:%M:%S +0000")</pubDate>
      <enclosure url="$DMG_URL" type="application/octet-stream" $ED_ATTRS/>
    </item>
  </channel>
</rss>
XML
echo "released dist/$DMG_NAME and dist/appcast.xml ($IDENTITY)"
