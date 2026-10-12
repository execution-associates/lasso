#!/usr/bin/env bash
# Run ON TITAN after lasso's release v<version> exists: build, sign and
# notarize LassoDesktop on the Mac (Scripts/release.sh, over ssh) and attach
# the DMG and its Sparkle appcast to that release.
#
#   mac/Scripts/publish.sh [version] [--no-upload]
#
# The version defaults to lasso's (src/version.go). --no-upload stops after
# copying the DMG and appcast to mac/dist/release/. No runner on the Mac
# listens to GitHub: lasso's repo is public, and a fork's workflow must never
# reach a logged-in Mac. LASSO_MAC picks the Mac (default minime).
set -euo pipefail
HERE="$(cd "$(dirname "$0")/.." && pwd)"
MAC="${LASSO_MAC:-minime}"
REMOTE="src/lasso-desktop/mac"
REPO="execution-associates/lasso"

UPLOAD=1
VERSION=""
for a in "$@"; do
  case "$a" in
    --no-upload) UPLOAD=0 ;;
    *) VERSION="$a" ;;
  esac
done
[ -n "$VERSION" ] || VERSION="$(grep -oP 'lassoSemver = "\K[0-9]+\.[0-9]+\.[0-9]+' "$HERE/../src/version.go")"
if [ "$UPLOAD" = 1 ]; then
  gh release view "v$VERSION" --repo "$REPO" >/dev/null || { echo "no release v$VERSION on $REPO yet" >&2; exit 1; }
fi

"$HERE/Scripts/dev.sh" sync
# fnox decrypts with the Mac's ssh key (as Recap's runner does); gh and fnox are Homebrew's.
ssh "$MAC" "export PATH=/opt/homebrew/bin:\$HOME/.local/share/mise/shims:\$PATH FNOX_AGE_KEY_FILE=\$HOME/.ssh/id_ed25519; cd $REMOTE && Scripts/release.sh $VERSION"

OUT="$HERE/dist/release"
mkdir -p "$OUT"
rsync -a "$MAC:$REMOTE/dist/LassoDesktop-$VERSION.dmg" "$MAC:$REMOTE/dist/appcast.xml" "$OUT/"
echo "copied to $OUT"
if [ "$UPLOAD" = 1 ]; then
  gh release upload "v$VERSION" "$OUT/LassoDesktop-$VERSION.dmg" "$OUT/appcast.xml" --clobber --repo "$REPO"
  echo "attached to $REPO v$VERSION"
fi
