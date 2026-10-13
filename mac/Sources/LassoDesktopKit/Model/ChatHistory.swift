import Foundation

/// Everything read of one conversation so far: the newest page as the poll
/// last read it, and every row read since the conversation opened, older
/// pages included (the web chat's `Accumulated`, ChatView.tsx).
///
/// lasso answers a chat with one page, the transcript's last `chatReadBytes`.
/// A long session, or a compacted one, starts that page well after its
/// beginning, so the reader scrolls up for the page above it
/// (`?before=<start_offset>`), and each poll's page is MERGED into what is on
/// screen rather than replacing it: replaced, every older row would fall out
/// at the next poll.
///
/// The rows are tagged with the conversation they were read for (`key`: the
/// pane's, or the bot's) and the transcript that answered (`path`). A page for
/// another pane, or a new session in the same one, starts over instead of
/// splicing two conversations together.
public struct ChatHistory: Equatable, Sendable {
    public private(set) var key = ""
    public private(set) var path = ""
    /// The newest page as last read: its rows are merged into `items`.
    public private(set) var latest: ChatPayload?
    public private(set) var items: [ChatItem] = []
    /// Where the oldest page read upward starts; nil before the first.
    public private(set) var olderStart: Int64?
    /// The page above is on its way.
    public private(set) var loadingOlder = false
    /// A hover preview opens on the latest turn; this is set once the reader
    /// has scrolled above it, and the rows before it are shown too.
    public private(set) var earlier = false
    /// Bumped by every start-over: a page asked for before one is dropped.
    private var epoch = 0

    public init() {}

    /// The newest page with every row read so far: what the views draw.
    public var payload: ChatPayload? {
        guard var p = latest else { return nil }
        p.items = items
        return p
    }

    /// There are rows above the first one read. Before any older page this
    /// is the newest page's word; after one, whether it started past zero.
    public var hasMore: Bool {
        guard let latest else { return false }
        if let s = olderStart { return s > 0 }
        return latest.more ?? false
    }

    /// Where the page above ends: nil when there is none to ask for.
    public var before: Int64? {
        guard let b = olderStart ?? latest?.start_offset, b > 0 else { return nil }
        return b
    }

    /// Take a polled page read for `key`: merged into the rows, or the start
    /// of a new history when it is another conversation's. True when it
    /// started over.
    @discardableResult
    public mutating func take(_ page: ChatPayload, key: String) -> Bool {
        let path = page.path ?? ""
        guard latest != nil, key == self.key, path == self.path else {
            reset()
            self.key = key
            self.path = path
            latest = page
            items = page.items
            return true
        }
        latest = page
        items = Self.merge(items, page.items)
        return false
    }

    /// Nothing read: the card moved to another agent.
    public mutating func reset() {
        let next = epoch + 1
        self = ChatHistory()
        epoch = next
    }

    /// Show the rows before the latest turn (a hover preview, scrolled up).
    public mutating func revealEarlier() { earlier = true }

    /// A read of the page above, as `beginOlder` handed it out.
    public struct OlderRead: Equatable, Sendable {
        public let before: Int64
        let epoch: Int
    }

    /// Start reading the page above: nil while one is on its way, or when
    /// there is none.
    public mutating func beginOlder() -> OlderRead? {
        guard !loadingOlder, hasMore, let b = before else { return nil }
        loadingOlder = true
        return OlderRead(before: b, epoch: epoch)
    }

    /// The page above came back (nil: it failed). Rows not already here go
    /// on top. A failed page leaves everything as it was, and scrolling up
    /// asks again. A page for a conversation since left, or from another
    /// transcript, is dropped.
    public mutating func endOlder(_ read: OlderRead, page: ChatPayload?) {
        guard read.epoch == epoch else { return }
        loadingOlder = false
        guard let page, (page.path ?? "") == path else { return }
        let have = Set(items.map(\.id))
        items = page.items.filter { !have.contains($0.id) } + items
        // A page with nothing older to give must still move the cursor, or
        // the next read would ask for the same empty page forever.
        olderStart = page.start_offset ?? 0
    }

    /// Fold a freshly read page into the rows on screen (the web's
    /// `mergeItems`, lib/chat-merge.ts). Rows already here are UPDATED in
    /// place (a tool card completing, an output growing); new rows go WHERE
    /// THE PAGE PUTS THEM, before the first row of the page already on
    /// screen, or at the end when none follows.
    ///
    /// "Unseen means newest" is not safe although the transcript is
    /// append-only: when the window's start lands between a tool call and
    /// its result, lasso reads a wider one (readLogPage), and that page
    /// carries rows OLDER than any on screen. Appended, they would land
    /// below the newest rows mid-turn.
    ///
    /// Each page is parsed on its own, so an older page can hold a call
    /// whose result landed in a newer one and parse it as still running. A
    /// finished card never goes back to running: the newer parse stands,
    /// whatever order the pages arrived in.
    static func merge(_ prev: [ChatItem], _ incoming: [ChatItem]) -> [ChatItem] {
        guard !prev.isEmpty else { return incoming }
        var at: [String: Int] = [:]
        for (i, it) in prev.enumerated() { at[it.id] = i }
        var out: [ChatItem] = []
        out.reserveCapacity(prev.count + incoming.count)
        // Where each row landed in `out`, for a page that names a row again
        // after one already passed (it should not; this keeps it an update).
        var placed: [String: Int] = [:]
        // How much of `prev` has been copied across.
        var p = 0
        // New rows waiting for the on-screen row they precede.
        var unseen: [ChatItem] = []
        func put(_ it: ChatItem) {
            placed[it.id] = out.count
            out.append(it)
        }
        for item in incoming {
            guard let i = at[item.id] else {
                if placed[item.id] == nil, !unseen.contains(where: { $0.id == item.id }) { unseen.append(item) }
                continue
            }
            if i < p {
                // Out of order relative to the screen: update it where it is.
                if let j = placed[prev[i].id], out[j] != item, !regresses(item, out[j]) { out[j] = item }
                continue
            }
            while p < i {
                put(prev[p])
                p += 1
            }
            unseen.forEach(put)
            unseen = []
            p += 1
            put(regresses(item, prev[i]) ? prev[i] : item)
        }
        while p < prev.count {
            put(prev[p])
            p += 1
        }
        unseen.forEach(put)
        return out
    }

    /// An incoming row says LESS than the one on screen: a tool that has
    /// finished cannot be running again.
    private static func regresses(_ incoming: ChatItem, _ current: ChatItem) -> Bool {
        guard let a = incoming.tool, let b = current.tool else { return false }
        return b.state != "running" && a.state == "running"
    }
}

/// A model that reads a conversation's history (an agent's card or window,
/// a bot's chat): the read of the page above is this one piece of code.
@MainActor
protocol ChatPaging: AnyObject {
    var history: ChatHistory { get set }
    /// The page of the conversation that ends at `before`.
    func olderPage(before: Int64) async throws -> ChatPayload
}

extension ChatPaging {
    /// Read the page above what is on screen, one read at a time.
    func readOlder() async {
        guard let read = history.beginOlder() else { return }
        let page = try? await olderPage(before: read.before)
        history.endOlder(read, page: page)
    }
}
