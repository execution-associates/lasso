#!/usr/bin/env bash
# Run ON THE MAC: swift test. The Command Line Tools ship swift-testing's macro
# plugin in a subdirectory SwiftPM does not search, so point the compiler at it.
set -euo pipefail
cd "$(dirname "$0")/.."
PLUGINS=/Library/Developer/CommandLineTools/usr/lib/swift/host/plugins/testing
flags=()
[ -d "$PLUGINS" ] && flags=(-Xswiftc -plugin-path -Xswiftc "$PLUGINS")
swift test "${flags[@]}" "$@"
