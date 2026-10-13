import AppKit
import SwiftUI

/// What a conversation view needs to read further back: which conversation
/// it is, whether the page above is on its way or there is any, and how to
/// ask for it.
struct ScrollBack {
    /// Which conversation: a new one (another transcript) is followed from
    /// its newest row again.
    var conversation = ""
    var loading = false
    /// There is something above the first row shown.
    var more = false
    /// Ask as soon as the reader is near the top, and again until the rows
    /// cover the viewport (the web's fill). A hover preview asks only when
    /// the reader scrolls up to its top: it opens on the latest turn, and
    /// showing more is the reader's doing.
    var fill = true
    var onTop: () -> Void = {}

    /// Nothing above to read: a conversation drawn on its own.
    init() {}

    /// An agent's or a bot's conversation as its model has read it.
    @MainActor
    init<M: Conversation>(_ model: M, more: Bool, fill: Bool, onTop: @escaping () -> Void) {
        let h = model.history
        conversation = h.key + "\u{0}" + h.path
        loading = h.loadingOlder
        self.more = more
        self.fill = fill
        self.onTop = onTop
    }

    init(_ h: ChatHistory, onTop: @escaping () -> Void) {
        conversation = h.key + "\u{0}" + h.path
        loading = h.loadingOlder
        more = h.hasMore
        self.onTop = onTop
    }
}

/// The top of a conversation while the page above is read: a small spinner
/// and what it waits on. Nothing is drawn when there is no more.
struct OlderRow: View {
    var body: some View {
        HStack(spacing: Theme.space("m")) {
            ProgressView().controlSize(.small)
            Text("Loading earlier messages")
                .font(Theme.Fonts.small.font)
                .foregroundStyle(Theme.Text.secondary.color)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, Theme.space("s"))
        .accessibilityElement(children: .combine)
        .accessibilityLabel("Loading earlier messages")
        .parityAnchor("conversation.older")
    }
}

extension View {
    /// Keep the reader's place in the conversation these rows are, and read
    /// further back when they reach its top (`ScrollKeeper`). `top` names
    /// what is drawn first.
    func scrollBack(_ back: ScrollBack, top: String) -> some View {
        background(
            ScrollKeeper(
                conversation: back.conversation, top: (back.loading ? "older\u{0}" : "") + top, more: back.more,
                fill: back.fill, onTop: back.onTop))
    }
}

/// Where the reader is in a conversation that grows at both ends, kept
/// through the AppKit scroll view a SwiftUI ScrollView is (the web chat's
/// `stick` and `restoreHeight`). Drawn behind the rows, it:
/// - follows the newest row while the reader is at the bottom, and stops
///   once they scroll up, so a poll never pulls them off what they read;
/// - holds the reader's place when rows go in above them (an older page,
///   the spinner over it, a preview's earlier turns), by keeping their
///   distance from the bottom, as the web restores by the growth in height;
/// - asks for what is above (`onTop`) when the reader nears the top.
struct ScrollKeeper: NSViewRepresentable {
    let conversation: String
    /// What is drawn first: when it changes, rows went in or out above.
    let top: String
    let more: Bool
    let fill: Bool
    let onTop: () -> Void

    func makeNSView(context: Context) -> ScrollKeeperView { ScrollKeeperView() }

    func updateNSView(_ v: ScrollKeeperView, context: Context) {
        v.update(conversation: conversation, top: top, more: more, fill: fill, onTop: onTop)
    }

    static func dismantleNSView(_ v: ScrollKeeperView, coordinator: ()) { v.detach() }
}

final class ScrollKeeperView: NSView {
    private weak var scroll: NSScrollView?
    private var observers: [NSObjectProtocol] = []
    private var wheel: Any?
    private var conversation: String?
    private var top: String?
    private var more = false
    private var fill = true
    private var onTop: () -> Void = {}
    /// Following the newest row: the reader is at the bottom.
    private(set) var stick = true
    /// Rows went in above: the next change of height keeps the reader's
    /// distance from the bottom rather than from the top.
    private var holdBottom = false
    /// The reader's distances from the top and the bottom at the last look.
    private var fromTop: CGFloat = 0
    private var fromBottom: CGFloat = 0
    private var height: CGFloat = 0
    /// The rows have been laid out and followed to the bottom once. Before
    /// that the top is where every scroll view starts, not where the reader is.
    private var settled = false
    /// A move of ours, not the reader's.
    private var moving = false

    override func hitTest(_ point: NSPoint) -> NSView? { nil }

    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        if window == nil { detach() } else { attach() }
    }

    func update(conversation: String, top: String, more: Bool, fill: Bool, onTop: @escaping () -> Void) {
        self.onTop = onTop
        self.fill = fill
        let opened = more && !self.more
        self.more = more
        if conversation != self.conversation {
            // Another conversation is followed from its newest row.
            if self.conversation != nil { stick = true }
            self.conversation = conversation
            self.top = top
            holdBottom = false
        } else if top != self.top {
            self.top = top
            if !stick { holdBottom = true }
        }
        if opened { askIfNearTop(scrolledUp: false) }
    }

    private func attach() {
        guard let s = enclosingScrollView, s !== scroll else { return }
        detach()
        scroll = s
        s.contentView.postsBoundsChangedNotifications = true
        s.contentView.postsFrameChangedNotifications = true
        s.documentView?.postsFrameChangedNotifications = true
        let nc = NotificationCenter.default
        observers = [
            nc.addObserver(forName: NSView.boundsDidChangeNotification, object: s.contentView, queue: nil) { [weak self] _ in
                MainActor.assumeIsolated { self?.scrolled() }
            },
            nc.addObserver(forName: NSView.frameDidChangeNotification, object: s.contentView, queue: nil) { [weak self] _ in
                MainActor.assumeIsolated { self?.resized() }
            },
        ]
        if let doc = s.documentView {
            observers.append(
                nc.addObserver(forName: NSView.frameDidChangeNotification, object: doc, queue: nil) { [weak self] _ in
                    MainActor.assumeIsolated { self?.resized() }
                })
        }
        // A pull upward at the top, where nothing scrolls: a preview's short
        // turn has nowhere to scroll, so the gesture itself asks.
        wheel = NSEvent.addLocalMonitorForEvents(matching: .scrollWheel) { [weak self] e in
            MainActor.assumeIsolated { self?.wheeled(e) }
            return e
        }
        resized()
    }

    func detach() {
        observers.forEach(NotificationCenter.default.removeObserver)
        observers = []
        if let wheel { NSEvent.removeMonitor(wheel) }
        wheel = nil
        scroll = nil
    }

    /// The document's height, the viewport's, and the reader's distance from
    /// the document's top.
    private func metrics() -> (height: CGFloat, visible: CGFloat, top: CGFloat)? {
        guard let s = scroll, let doc = s.documentView else { return nil }
        let r = s.documentVisibleRect
        let h = doc.frame.height
        return (h, r.height, doc.isFlipped ? r.minY : h - r.maxY)
    }

    /// Put the viewport's top `y` from the document's top.
    private func move(toTop y: CGFloat) {
        guard let s = scroll, let doc = s.documentView else { return }
        let clip = s.contentView
        let h = doc.frame.height
        let visible = clip.bounds.height
        let y = max(0, min(y, h - visible))
        let origin = doc.isFlipped ? y : h - visible - y
        guard abs(clip.bounds.minY - origin) > 0.5 else { return }
        moving = true
        clip.scroll(to: NSPoint(x: clip.bounds.minX, y: origin))
        s.reflectScrolledClipView(clip)
        moving = false
    }

    private func note() {
        guard let m = metrics() else { return }
        height = m.height
        fromTop = m.top
        fromBottom = m.height - m.visible - m.top
    }

    /// The rows or the viewport changed size: follow the newest row, or keep
    /// the reader where they were.
    private func resized() {
        guard let m = metrics(), m.height > 0, m.visible > 0 else { return }
        if !settled {
            settled = true
            move(toTop: .greatestFiniteMagnitude)
        } else if stick {
            move(toTop: .greatestFiniteMagnitude)
        } else if m.height != height {
            // Rows above (a page, its spinner) keep the reader's distance from
            // the bottom; anything else, from the top.
            move(toTop: holdBottom ? m.height - m.visible - fromBottom : fromTop)
            holdBottom = false
        }
        note()
        askIfNearTop(scrolledUp: false)
    }

    /// The reader scrolled (or we moved them).
    private func scrolled() {
        guard settled, !moving, let m = metrics() else { return }
        let up = m.top < fromTop
        let toBottom = m.height - m.visible - m.top
        stick = toBottom < Theme.Live.scrollBack.number("follow")
        note()
        askIfNearTop(scrolledUp: up)
    }

    private func wheeled(_ e: NSEvent) {
        guard more, let s = scroll, e.window === window, e.scrollingDeltaY > 0,
              s.bounds.contains(s.convert(e.locationInWindow, from: nil)), let m = metrics(), m.top <= 1
        else { return }
        ask()
    }

    /// Ask for what is above when the reader is near the top: a window or
    /// card whenever they are (until the rows cover the viewport), a preview
    /// only when they scrolled up there.
    private func askIfNearTop(scrolledUp: Bool) {
        guard settled, more, fill || scrolledUp, let m = metrics(),
              m.top < Theme.Live.scrollBack.number("older")
        else { return }
        ask()
    }

    /// Asked on the next turn of the run loop: the answer publishes, which it
    /// must not do inside the layout or view update that got here.
    private func ask() {
        RunLoop.main.perform(inModes: [.common]) { [weak self] in
            MainActor.assumeIsolated { self?.onTop() }
        }
    }
}
