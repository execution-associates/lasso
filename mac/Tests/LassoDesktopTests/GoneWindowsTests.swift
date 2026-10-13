import Foundation
import Testing

@testable import LassoDesktopKit

/// An agent that ends elsewhere takes its window with it, but only once it
/// was seen, only on listings its host answered, and only after two misses.
@Test func goneWindowsCloseOnlyForAnAgentThatEnded() {
    let a = "local\u{0}w1:p1"
    let b = "norm\u{0}w2:p1"
    var g = GoneWindows()
    // A new agent's window opens before its agent is listed: no close.
    #expect(g.listing(open: [a], agents: [], hostErrors: []).isEmpty)
    #expect(g.listing(open: [a], agents: [], hostErrors: []).isEmpty)
    // Seen once, then missed twice: closed on the second miss.
    #expect(g.listing(open: [a, b], agents: [a, b], hostErrors: []).isEmpty)
    #expect(g.listing(open: [a, b], agents: [b], hostErrors: []).isEmpty)
    #expect(g.listing(open: [a, b], agents: [b], hostErrors: []) == [a])
    // A host that errored says nothing; a listing that has it again resets.
    #expect(g.listing(open: [b], agents: [], hostErrors: ["norm"]).isEmpty)
    #expect(g.listing(open: [b], agents: [], hostErrors: ["norm"]).isEmpty)
    #expect(g.listing(open: [b], agents: [], hostErrors: []).isEmpty)
    #expect(g.listing(open: [b], agents: [b], hostErrors: []).isEmpty)
    #expect(g.listing(open: [b], agents: [], hostErrors: []).isEmpty)
    #expect(g.listing(open: [b], agents: [], hostErrors: []) == [b])
    #expect(GoneWindows.host(of: b) == "norm")
}
