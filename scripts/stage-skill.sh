#!/usr/bin/env bash
# Copies lasso's agent skill (the repo's SKILL.md and the docs/ it links into)
# to src/skill/lasso, where the binary's go:embed can reach it: go:embed cannot
# look above the module root (src/). /mcp serves it over the MCP Skills
# extension (src/mcpskill.go). The copy keeps the repo's layout, so every
# relative link in SKILL.md and the docs resolves the same way under
# skill://lasso/ as it does in the repo. Only Markdown is copied; images and
# anything else in docs/ stay out of the binary.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
dest="$root/src/skill/lasso"
rm -rf "$root/src/skill"
mkdir -p "$dest"
cp "$root/SKILL.md" "$dest/SKILL.md"
cd "$root"
find docs -type f -name '*.md' | while IFS= read -r f; do
  mkdir -p "$dest/$(dirname "$f")"
  cp "$f" "$dest/$f"
done
