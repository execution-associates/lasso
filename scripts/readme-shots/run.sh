#!/usr/bin/env bash
# `mise run readme:shots [NAME...]`: retake the README's screenshots
# (docs/assets/readme/) in a throwaway VM. The host only drives it: it stores
# the model key in isb, stages the inputs, and runs the scripts beside this one
# inside the VM, which provision the box, seed a demo fleet with real agents,
# and shoot. NAMEs reshoot just those cells.
#
# READMESHOTS_KEEP=1 leaves the VM, its inputs and the isb secret in place, so
# a later run reuses them (seeding skips what exists): the way to iterate on
# shoot.py. Without it, everything is removed when the task ends.
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"

vm=${READMESHOTS_VM:-lasso-readme-shots}
secret=lasso-readme-shots.deepseek
work=${XDG_CACHE_HOME:-$HOME/.cache}/lasso-readme-shots
# The README's grid also has desktop.webp, the Mac app, which is composed from
# its parity renders (mac/), not shot here; this task never touches it.
cells=(terminal grid chat files browser new-agent hosts mcp bots phone plugins)
dest=docs/assets/readme

# A plain sandbox in incus' default project, never an org's, and never a
# remote daemon (the same rule lasso's plugin sandboxes follow).
unset ISB_ORG INCUS_PROJECT ISB_URL ISB_TOKEN
command -v isb >/dev/null || { echo "readme:shots needs isb on PATH" >&2; exit 1; }
[ -x ./lasso ] || { echo "no ./lasso: run \`mise run build\` first" >&2; exit 1; }

# The key goes from 1Password into isb's store on stdin, never argv or a log.
# The VM only ever sees isb's placeholder for it.
DEEPSEEK_API_KEY="op://titan/a6a2t5ez6pao5zzwmvbea4tbyu/DEEPSEEK_API_KEY" \
  op run -- sh -c 'printf %s "$DEEPSEEK_API_KEY" | isb secret set -q "$1" -' sh "$secret"

# Inputs, staged so the VM mounts only them: the lasso just built, these
# scripts, and the example plugin the Plugins cell shows. Emptied rather than
# recreated: a kept VM's mount follows this directory, not a new one.
mkdir -p "$work/in" "$work/out"
find "$work/in" "$work/out" -mindepth 1 -delete
mkdir -p "$work/in/plugins"
cp ./lasso "$work/in/lasso"
cp -r scripts/readme-shots "$work/in/readme-shots"
cp -r examples/plugins/hello "$work/in/plugins/hello"

export READMESHOTS_VM=$vm READMESHOTS_IN=$work/in READMESHOTS_SECRET=$secret
compose=(-f scripts/isb/readme-shots.yaml)

cleanup() {
  if [ "${READMESHOTS_KEEP:-}" = 1 ]; then
    echo "readme:shots: kept $vm (READMESHOTS_KEEP=1); \`isb rm -f $vm\` and \`isb secret rm $secret\` when done"
    return
  fi
  isb "${compose[@]}" down -q || true
  isb secret rm -q "$secret" || true
  rm -rf "$work"
}
trap cleanup EXIT

# A first boot of a fresh VM now and then never reaches the OS (isb's own
# preview VM retries the same way); a second one has always come up.
if ! isb "${compose[@]}" up -d -q; then
  echo "readme:shots: $vm did not come up; recreating it once" >&2
  isb "${compose[@]}" down -q || true
  isb "${compose[@]}" up -d -q
fi
in_vm() { isb "${compose[@]}" exec -q "$@"; }
in_vm -u root shots -- bash /in/readme-shots/provision.sh
in_vm shots -- bash -l /in/readme-shots/seed.sh
in_vm shots -- bash -l /in/readme-shots/terminals.sh
in_vm shots -- bash -lc 'out=~/readme-shots/out; rm -rf "$out" && uv run -q /in/readme-shots/shoot.py "$out" "$@"' sh "$@"
# The WebPs come back as a tar on stdout. (A writable virtiofs mount fails
# every write with EIO in this VM.)
in_vm -T shots -- bash -c 'cd ~/readme-shots/out && tar -cf - *.webp' | tar -xf - -C "$work/out"

mkdir -p "$dest"
want=("$@")
[ ${#want[@]} -gt 0 ] || want=("${cells[@]}")
fail=0
for c in "${want[@]}"; do
  f=$work/out/$c.webp
  if [ ! -s "$f" ]; then
    echo "missing: $c.webp" >&2
    fail=1
    continue
  fi
  size=$(stat -c %s "$f")
  # shoot.py aims each at about 100 KB, which keeps the README light.
  [ "$size" -le 150000 ] || { echo "too big: $c.webp is $size bytes" >&2; fail=1; }
  cp "$f" "$dest/$c.webp"
done
ls -l "$dest"
exit "$fail"
