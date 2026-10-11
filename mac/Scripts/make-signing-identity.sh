#!/usr/bin/env bash
# Run ON THE MAC, once: create a self-signed code-signing identity for
# LassoDesktop in its own keychain. The keychain password arrives on stdin
# (dev.sh pipes it from `secret LASSO_DESKTOP_SIGNING_KEYCHAIN` on titan).
#
# Why: macOS keeps microphone, screen-recording and accessibility grants per
# signature. An ad-hoc signature changes every build, so the grants would be
# lost on each rebuild; a stable identity keeps them.
#
# The key carries this app's permission grants, so it stays in this keychain,
# is usable only by codesign, and never leaves the Mac.
set -euo pipefail

KC="$HOME/Library/Keychains/lasso-desktop-signing.keychain-db"
CN="LassoDesktop Dev ($(scutil --get LocalHostName))"

IFS= read -r PW
[ -n "$PW" ] || { echo "no keychain password on stdin" >&2; exit 2; }

if [ -f "$KC" ] && security find-identity -p codesigning "$KC" 2>/dev/null | grep -q "\"$CN\""; then
  echo "identity already present: $CN"
  exit 0
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cd "$tmp"

cat > req.cnf <<CNF
[req]
distinguished_name = dn
x509_extensions = ext
prompt = no
[dn]
CN = $CN
[ext]
basicConstraints = critical, CA:false
keyUsage = critical, digitalSignature
extendedKeyUsage = critical, codeSigning
subjectKeyIdentifier = hash
CNF
openssl req -x509 -newkey rsa:2048 -nodes -days 3650 -config req.cnf -keyout key.pem -out cert.pem 2>/dev/null
P12PW=$(openssl rand -hex 16)
openssl pkcs12 -export -inkey key.pem -in cert.pem -name "$CN" -out id.p12 -passout "pass:$P12PW"

[ -f "$KC" ] || security create-keychain -p "$PW" "$KC"
security set-keychain-settings "$KC"           # no auto-lock timeout
security unlock-keychain -p "$PW" "$KC"
security import id.p12 -k "$KC" -P "$P12PW" -T /usr/bin/codesign >/dev/null
security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$PW" "$KC" >/dev/null

# codesign looks identities up through the user search list.
if ! security list-keychains -d user | grep -q "lasso-desktop-signing"; then
  # shellcheck disable=SC2046
  security list-keychains -d user -s "$KC" $(security list-keychains -d user | tr -d '"')
fi

cp /usr/bin/true probe
if codesign -s "$CN" --keychain "$KC" -f probe 2>err.txt; then
  echo "identity ready: $CN"
else
  echo "codesign refused the identity:" >&2
  cat err.txt >&2
  exit 1
fi
