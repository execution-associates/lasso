#!/usr/bin/env bash
# Run ON TITAN, from anywhere in the repo. Rewrites mac/Resources/Icons: the
# Lucide icons the Mac app draws, as SVGs, plus Lucide's license.
#
#   mac/Scripts/lucide-icons.sh
#
# The icons are the web app's own: the lucide-react that src/web/bun.lock pins
# (its sha512 is in the lockfile). Every lucide-react icon module exports its
# drawing as data (`__iconNode`, the elements of Lucide's 24 px SVG), so the
# SVGs are rebuilt from that data, in lucide-static's form, and no package is
# added. Which icons: the raw values of `enum Lucide` in
# mac/Sources/LassoDesktopKit/Theme/Lucide.swift. Add a case, run this.
#
# Reading the modules runs third-party code (lucide-react, and bun install when
# node_modules is missing), so that half runs inside this worktree's dev
# container like every frontend command (scripts/container.sh). The container
# sees only src/web, so it prints the data as JSON and lucide-icons.py writes
# the files here, accepting nothing but plain SVG shapes with numeric geometry.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
MAC="$(cd "$HERE/.." && pwd)"
REPO="$(cd "$MAC/.." && pwd)"
# shellcheck source=../../scripts/container.sh
. "$REPO/scripts/container.sh"

mapfile -t NAMES < <(awk '/^public enum Lucide/ { on = 1; next } on && /^}/ { exit } on' "$MAC/Sources/LassoDesktopKit/Theme/Lucide.swift" |
  grep -oE '^ *case [A-Za-z0-9]+( = "[a-z0-9-]+")?$' |
  sed -E 's/^ *case ([A-Za-z0-9]+)( = "([a-z0-9-]+)")?$/\1 \3/' | awk '{ print ($2 != "" ? $2 : $1) }')
[ "${#NAMES[@]}" -gt 0 ] || { echo "no icons found in Lucide.swift" >&2; exit 1; }

JS='const fs = require("fs")
const root = "node_modules/lucide-react"
const pkg = JSON.parse(fs.readFileSync(root + "/package.json", "utf8"))
const out = { version: pkg.version, license: fs.readFileSync(root + "/LICENSE", "utf8"), icons: {} }
for (const n of process.argv.slice(1)) {
  const m = await import(process.cwd() + "/" + root + "/dist/esm/icons/" + n + ".mjs")
  if (!Array.isArray(m.__iconNode)) throw new Error("lucide-react has no icon " + n)
  out.icons[n] = m.__iconNode
}
console.log(JSON.stringify(out))'

container_ensure >/dev/null
container_run "[ -d node_modules/lucide-react ] || bun install --frozen-lockfile >&2; bun -e $(printf %q "$JS") ${NAMES[*]}" |
  uv run --quiet --script "$HERE/lucide-icons.py" "$MAC/Resources/Icons" "${NAMES[@]}"
