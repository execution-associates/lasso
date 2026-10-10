#!/usr/bin/env bash
# Run one of the OpenBot example plugin's bun scripts inside this worktree's dev
# container (see scripts/container.sh), the same isolation as src/web's.
#
#   usage: container-openbot.sh <script> [args...]   e.g. container-openbot.sh build
#
# The plugin is its own small Vite project (examples/plugins/openbot/web) with
# its own node_modules, mounted by scripts/isb/openbot.yaml.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/container.sh
. "$HERE/container.sh"

LASSO_OPENBOT="$(cd "$HERE/../examples/plugins/openbot" && pwd -P)"
export LASSO_OPENBOT
container_with openbot.yaml
container_ensure

GUEST_OPENBOT_WEB="$GUEST_ROOT/examples/plugins/openbot/web"
# Install only when node_modules is missing, against the lockfile once there
# is one (the first install writes it).
container_run_in "$GUEST_OPENBOT_WEB" \
  "test -d node_modules || if test -f bun.lock; then bun install --frozen-lockfile; else bun install; fi"
container_run_in "$GUEST_OPENBOT_WEB" "bun run $*"
