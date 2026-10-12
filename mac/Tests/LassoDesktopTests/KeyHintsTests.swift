import Foundation
import Testing

@testable import LassoDesktopKit

/// Keycaps go once ⌘ is up even when no release event reaches Lasso (a ⌘⇧
/// screenshot over a non-activating panel): a watch re-reads ⌘ while they
/// show, and nothing runs while they are hidden.
@MainActor @Test func keycapsHideOnceCommandIsUpWithoutAnEvent() async throws {
    let hints = KeyHints()
    var down = true
    hints.commandDown = { down }
    #expect(!hints.watching)
    hints.visible = true
    #expect(hints.watching)
    hints.check()
    #expect(hints.visible)
    down = false
    try await Task.sleep(nanoseconds: UInt64(Theme.Live.revealKeys.seconds * 3 * 1e9))
    #expect(!hints.visible && !hints.watching)
    // Hidden by the monitor's own event: the watch stops with them.
    down = true
    hints.visible = true
    hints.visible = false
    #expect(!hints.watching)
}
