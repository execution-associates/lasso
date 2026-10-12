/// A column's row order held still while it is open (PLAN 16): a fleet
/// update changes what a row says, never where it is, so the row under a
/// resting pointer stays the one it was. A row that appears meanwhile goes
/// after the rest, in the order it came; a row that goes is dropped.
public struct FrozenOrder: Equatable, Sendable {
    private var rank: [String: Int]?

    public init() {}

    public var isFrozen: Bool { rank != nil }

    /// Hold `keys`, in this order, from now on.
    public mutating func freeze(_ keys: [String]) {
        var r: [String: Int] = [:]
        for (i, k) in keys.enumerated() where r[k] == nil { r[k] = i }
        rank = r
    }

    public mutating func thaw() { rank = nil }

    /// `items` (already in their live order) in the held order: held rows
    /// first by rank, then the new ones as they came. Unchanged when thawed.
    public func apply<T>(_ items: [T], key: (T) -> String) -> [T] {
        guard let rank else { return items }
        return items.enumerated().sorted { a, b in
            let (ra, rb) = (rank[key(a.element)], rank[key(b.element)])
            switch (ra, rb) {
            case let (x?, y?): return x < y
            case (.some, nil): return true
            case (nil, .some): return false
            case (nil, nil): return a.offset < b.offset
            }
        }.map(\.element)
    }
}
