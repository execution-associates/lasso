import Foundation

/// Which open agent windows belong to agents that have ended: an agent that
/// closed itself, or was closed from herdr, the web or another Mac. Its
/// window goes, as one killed from the window does.
///
/// A window counts its agent gone only after the fleet has listed it as an
/// agent at least once since the window opened (a new agent's window opens
/// on a pane whose agent is still booting), and then missed it on
/// `missesToClose` listings in a row from a host that answered (a host that
/// errored says nothing about its agents, and one listing can miss an agent
/// mid restart).
struct GoneWindows {
    static let missesToClose = 2

    private var seen: Set<String> = []
    private var misses: [String: Int] = [:]

    /// One fleet listing: `open` are the windows' pane keys, `agents` the
    /// agents listed now, `hostErrors` the hosts that did not answer.
    /// Answers the windows to close.
    mutating func listing(open: Set<String>, agents: Set<String>, hostErrors: Set<String>) -> [String] {
        seen.formIntersection(open)
        misses = misses.filter { open.contains($0.key) }
        var gone: [String] = []
        for key in open.sorted() {
            if agents.contains(key) {
                seen.insert(key)
                misses[key] = nil
                continue
            }
            guard seen.contains(key), !hostErrors.contains(Self.host(of: key)) else { continue }
            let n = (misses[key] ?? 0) + 1
            misses[key] = n
            if n >= Self.missesToClose { gone.append(key) }
        }
        for key in gone {
            seen.remove(key)
            misses[key] = nil
        }
        return gone
    }

    /// A pane key is `host NUL pane_id`.
    static func host(of key: String) -> String {
        key.split(separator: "\u{0}", maxSplits: 1, omittingEmptySubsequences: false).first.map(String.init) ?? ""
    }
}
