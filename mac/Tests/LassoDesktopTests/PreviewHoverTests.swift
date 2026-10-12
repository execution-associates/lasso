import AppKit
import Foundation
import Testing

@testable import LassoDesktopKit

/// The agents list's hover preview, tick by tick at the list's 20 Hz: every
/// row rested on opens after the same rest, moving rows swaps it, a fleet
/// poll redrawing the rows changes nothing, and a preview put away from
/// elsewhere comes back for the next row rested on, and for the same row
/// once the pointer has left it.
@Test func thePreviewFollowsTheRowRestedOn() {
    let open = 0.117
    let close = 0.317
    var h = PreviewHover()
    var showing: String?
    let t0 = Date(timeIntervalSince1970: 1000)
    var t = t0
    /// Run the list's poll from `t` for `seconds` with the pointer on `row`.
    func rest(_ row: String?, _ seconds: Double, holds: Bool = false) -> [PreviewHover.Action] {
        var acts: [PreviewHover.Action] = []
        let end = t.addingTimeInterval(seconds)
        while t <= end {
            let a = h.tick(row: row, showing: showing, holds: holds, now: t, openAfter: open, closeAfter: close)
            switch a {
            case .show(let k): showing = k
            case .hide: showing = nil
            case .none: break
            }
            if a != .none { acts.append(a) }
            t = t.addingTimeInterval(0.05)
        }
        return acts
    }

    // A row the pointer arrives on (or a row drawn under a still pointer).
    #expect(rest("a", 0.1) == [])
    #expect(rest("a", 0.1) == [.show("a")])
    // A fleet poll redraws the rows: the same key, nothing restarts or hides.
    #expect(rest("a", 1) == [])
    // Another row swaps it after the same rest, with no leaving in between.
    #expect(rest("b", 0.3) == [.show("b")])

    // Esc in the preview: it stays away while the pointer stays on that row…
    showing = nil
    #expect(rest("b", 1) == [])
    // …and the next row rested on opens as ever,
    #expect(rest("a", 0.3) == [.show("a")])
    // as does the first row again once the pointer has been off it.
    #expect(rest("b", 0.3) == [.show("b")])

    // A click on the row opens its window and puts the preview away; off
    // the list and back, it previews again.
    showing = nil
    #expect(rest("b", 0.3) == [])
    #expect(rest(nil, 0.1) == [])
    #expect(rest("b", 0.3) == [.show("b")])

    // Into the preview (it holds), then off both: it goes after panel_close_ms.
    #expect(rest(nil, 1, holds: true) == [])
    #expect(rest(nil, 0.2) == [])
    #expect(rest(nil, 0.3) == [.hide])
    #expect(showing == nil)
    // A list closed and reopened starts fresh: a row opens after the rest.
    h = PreviewHover()
    #expect(rest("c", 0.3) == [.show("c")])
}

/// A preview already showing moves to another row after the shorter
/// switch rest, and `wait` says when each pending rest is due, so the hover
/// looks again at that moment rather than at its next poll.
@Test func aShowingPreviewSwitchesSoonerAndEachRestIsTimed() {
    var h = PreviewHover()
    let t0 = Date(timeIntervalSince1970: 1000)
    func tick(_ row: String?, _ showing: String?, _ at: Double) -> PreviewHover.Action {
        h.tick(row: row, showing: showing, holds: false, now: t0.addingTimeInterval(at), openAfter: 0.06,
               switchAfter: 0.05, closeAfter: 0.3)
    }
    func wait(_ showing: String?, _ at: Double) -> Double? {
        h.wait(showing: showing, holds: false, now: t0.addingTimeInterval(at), openAfter: 0.06, switchAfter: 0.05,
               closeAfter: 0.3)
    }
    #expect(tick("a", nil, 0) == .none)
    #expect(abs(wait(nil, 0)! - 0.06) < 1e-9, "the open is due in 60 ms")
    #expect(tick("a", nil, 0.061) == .show("a"))
    #expect(wait("a", 0.061) == nil, "nothing pending while it shows the row")
    #expect(tick("b", "a", 0.1) == .none)
    #expect(abs(wait("a", 0.1)! - 0.05) < 1e-9, "the switch is due in 50 ms")
    #expect(tick("b", "a", 0.151) == .show("b"))
    #expect(tick(nil, "b", 0.2) == .none)
    #expect(abs(wait("b", 0.2)! - 0.3) < 1e-9, "the close is due in 300 ms")
    #expect(tick(nil, "b", 0.501) == .hide)
    #expect(wait(nil, 0.501) == nil)
}
