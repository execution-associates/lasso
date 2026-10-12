#!/usr/bin/env bash
# Run ON THE MAC: the tests, in a process linked as the app is. The Command
# Line Tools ship swift-testing's macro plugin in a subdirectory SwiftPM does
# not search, so point the compiler at it.
#
# AppKit and SwiftUI pick some behaviour by the SDK version the process's
# executable was linked against. `swift test` runs the tests inside
# swiftpm-testing-helper, linked against a newer SDK than the app, so a test
# could pass there and the app still fail (a hidden NSHostingView takes clicks
# under the app's, not under the helper's). Scripts/test-host.swift loads the
# test bundle as the helper does, built here with the SDK version the bundle
# records, which is the app's.
set -euo pipefail
cd "$(dirname "$0")/.."
PLUGINS=/Library/Developer/CommandLineTools/usr/lib/swift/host/plugins/testing
flags=()
[ -d "$PLUGINS" ] && flags=(-Xswiftc -plugin-path -Xswiftc "$PLUGINS")
swift build --build-tests "${flags[@]}"
BUNDLE="$(swift build --show-bin-path)/LassoDesktopTests.xctest/Contents/MacOS/LassoDesktopTests"
read -r MINOS SDK < <(vtool -show-build "$BUNDLE" | awk '$1 == "minos" {m = $2} $1 == "sdk" {s = $2} END {print m, s}')
DEV="$(xcode-select -p)"
HOST=.build/test-host
swiftc -O Scripts/test-host.swift -o "$HOST" \
  -Xlinker -platform_version -Xlinker macos -Xlinker "$MINOS" -Xlinker "$SDK" \
  -Xlinker -rpath -Xlinker "$DEV/Library/Developer/Frameworks" \
  -Xlinker -rpath -Xlinker "$DEV/Library/Developer/usr/lib"
# One test at a time: the UI tests share the one key window, and a test
# that takes it in another's await breaks that one's typing.
"$HOST" "$BUNDLE" --testing-library swift-testing --no-parallel "$@"
