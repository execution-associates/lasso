import AppKit
import SwiftUI

// A link in the chat shows the pointing hand while the pointer is on its
// glyphs. SwiftUI's Text does not say where it drew its links, and the app
// (linked against the 14 SDK, as SwiftPM records it) never has a
// TextRenderer called for what is on screen. An ImageRenderer still calls
// one: so when the pointer comes onto a text with links, the same text is
// laid out off screen at the size it has on screen, its links' runs marked,
// by a renderer that notes where they are (macOS 15; on 14 the pointer is
// left as it is). The text on screen is the plain Text it always was.
//
// The chat's windows are non-activating panels of an app that is usually
// not the active one, where cursor rects and .onHover do not reach, so the
// pointer is read by a tracking area that works with the app inactive, as
// ColumnHover's, and the cursor is set on every move. macOS shows a cursor
// an inactive app sets only over its key window or a titled one, and these
// panels are borderless and often not key (a preview never is), so the app
// asks the window server to show its cursor in the background
// (BackgroundCursor).

/// Inline Markdown (MessageBubble.styled) as a Text whose links show the
/// pointing hand. Off a link the cursor is the one the window shows for the
/// selectable text (LinkCursorView.look).
struct LinkText: View {
    let text: AttributedString
    @Environment(\.font) private var font
    @Environment(\.lineLimit) private var lineLimit
    @Environment(\.multilineTextAlignment) private var alignment

    init(_ text: AttributedString) { self.text = text }

    var body: some View {
        if #available(macOS 15, *), LinkRuns.hasLink(text) {
            Text(text).background(
                LinkCursorArea(layout: LinkLayout(text: text, font: font, lineLimit: lineLimit, alignment: alignment)))
        } else {
            Text(text)
        }
    }
}

/// A run of text that is a link, as the renderer finds it in the layout.
@available(macOS 15, *)
struct LinkMark: TextAttribute {}

enum LinkRuns {
    static func hasLink(_ s: AttributedString) -> Bool {
        s.runs.contains { $0.link != nil }
    }

    /// The same text, its links' runs marked: one Text per stretch of equal
    /// link, joined, so it lays out as the one Text it was.
    @available(macOS 15, *)
    static func marked(_ s: AttributedString) -> Text {
        var out: Text?
        for (link, range) in s.runs[\.link] {
            var piece = Text(AttributedString(s[range]))
            if link != nil { piece = piece.customAttribute(LinkMark()) }
            out = out.map { Text("\($0)\(piece)") } ?? piece
        }
        return out ?? Text(s)
    }
}

/// What a LinkText lays out with: its text and the environment that shapes
/// its lines.
@available(macOS 15, *)
struct LinkLayout: Equatable {
    var text: AttributedString
    var font: Font?
    var lineLimit: Int?
    var alignment: TextAlignment = .leading

    /// The links' rects (top-left origin) with the text laid out at `size`,
    /// the size it has on screen: given its own width, a Text breaks its
    /// lines where it did.
    @MainActor func rects(in size: CGSize) -> [CGRect] {
        let found = LinkRects()
        let r = ImageRenderer(
            content: LinkRuns.marked(text).font(font).lineLimit(lineLimit).multilineTextAlignment(alignment)
                .textRenderer(LinkRectsRenderer(rects: found)))
        r.proposedSize = ProposedViewSize(width: size.width, height: nil)
        _ = r.cgImage
        return found.rects
    }
}

/// The rects a renderer noted as it drew.
final class LinkRects: @unchecked Sendable {
    var rects: [CGRect] = []
}

/// Draws the text as it is, noting where its marked runs are.
@available(macOS 15, *)
struct LinkRectsRenderer: TextRenderer {
    let rects: LinkRects

    func draw(layout: Text.Layout, in ctx: inout GraphicsContext) {
        var found: [CGRect] = []
        for line in layout {
            for run in line where run[LinkMark.self] != nil { found.append(run.typographicBounds.rect) }
            ctx.draw(line)
        }
        rects.rects = found
    }
}

/// Behind a LinkText, its size: where the pointer is, over its links.
@available(macOS 15, *)
struct LinkCursorArea: NSViewRepresentable {
    let layout: LinkLayout

    func makeNSView(context: Context) -> LinkCursorView { LinkCursorView(layout: layout) }
    func updateNSView(_ view: LinkCursorView, context: Context) { view.layout = layout }
}

@available(macOS 15, *)
final class LinkCursorView: NSView {
    var layout: LinkLayout {
        didSet { if layout != oldValue { measured = nil } }
    }
    /// The links' rects and the size they were found at: found when the
    /// pointer first needs them, again once the text or its size changes.
    private var measured: (size: CGSize, rects: [CGRect])?
    /// The text whose link the pointer is on, which set the hand: one at a
    /// time, so a text the pointer leaves never takes the cursor back from
    /// the one it went onto, whichever hears of the move first.
    private weak static var hand: LinkCursorView?
    /// While the hand is this text's. AppKit puts the selectable text's
    /// I-beam back by itself after the move that set the hand (the pointer
    /// coming onto the text, a scroll settling), so the hand is set again on
    /// a 20 Hz poll, as ColumnHover polls, until the pointer is off the link.
    private var poll: Timer?
    private var scrolls: [NSObjectProtocol] = []

    init(layout: LinkLayout) {
        self.layout = layout
        super.init(frame: .zero)
    }

    required init?(coder: NSCoder) { nil }

    override var isFlipped: Bool { true }
    /// Clicks and drags go to the text: its links and its selection.
    override func hitTest(_ point: NSPoint) -> NSView? { nil }

    var rects: [CGRect] {
        if let m = measured, m.size == bounds.size { return m.rects }
        let r = layout.rects(in: bounds.size)
        measured = (bounds.size, r)
        return r
    }

    /// The cursor for the pointer at `p` (this view's space): the hand on a
    /// link, every time, since AppKit may have put another back. Off it, the
    /// cursor the window would show: in a key window the selectable text's
    /// I-beam, and past the text AppKit's own (it resets the cursor as the
    /// pointer leaves the text); in any other window AppKit sets none, and
    /// it is the arrow. Only what is shown counts: the view does not clip,
    /// so its visible rect can reach past its bounds.
    func look(_ p: NSPoint) {
        let shown = bounds.intersection(visibleRect).contains(p)
        if shown, rects.contains(where: { $0.contains(p) }) {
            NSCursor.pointingHand.set()
            if Self.hand !== self { take() }
        } else if Self.hand === self {
            release()
            if window?.isKeyWindow != true {
                NSCursor.arrow.set()
            } else {
                (shown ? NSCursor.iBeam : NSCursor.arrow).set()
            }
        }
    }

    /// Look where the pointer is now, when it is over this window.
    private func lookAtPointer() {
        guard let window, NSWindow.windowNumber(at: NSEvent.mouseLocation, belowWindowWithWindowNumber: 0) == window.windowNumber
        else {
            if Self.hand === self { release() }
            return
        }
        look(convert(window.mouseLocationOutsideOfEventStream, from: nil))
    }

    private func take() {
        Self.hand?.release()
        Self.hand = self
        let t = Timer(timeInterval: 1.0 / 20, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.lookAtPointer() }
        }
        RunLoop.main.add(t, forMode: .common)
        poll = t
    }

    private func release() {
        poll?.invalidate()
        poll = nil
        if Self.hand === self { Self.hand = nil }
    }

    private func look(_ event: NSEvent) { look(convert(event.locationInWindow, from: nil)) }

    override func mouseMoved(with event: NSEvent) { look(event) }
    override func mouseEntered(with event: NSEvent) { look(event) }
    override func mouseExited(with event: NSEvent) {
        guard Self.hand === self else { return }
        release()
        if window?.isKeyWindow != true { NSCursor.arrow.set() }
    }

    /// Pointer events over the text, whether or not the app is active, from
    /// when it is in a window. A scroll moves the text under a still
    /// pointer, which sends no event: look again when any scroll view around
    /// it scrolls, while the pointer is over this window.
    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        for o in scrolls { NotificationCenter.default.removeObserver(o) }
        scrolls = []
        guard window != nil else {
            if Self.hand === self {
                release()
                NSCursor.arrow.set()
            }
            return
        }
        // Before the first hand: AppKit does not send a cursor that is
        // already its current one again, so a hand the window server
        // dropped would stay dropped.
        _ = BackgroundCursor.enabled
        if trackingAreas.isEmpty {
            addTrackingArea(
                NSTrackingArea(
                    rect: .zero, options: [.mouseMoved, .mouseEnteredAndExited, .activeAlways, .inVisibleRect], owner: self,
                    userInfo: nil))
        }
        var scroll = enclosingScrollView
        while let s = scroll {
            s.contentView.postsBoundsChangedNotifications = true
            scrolls.append(
                NotificationCenter.default.addObserver(
                    forName: NSView.boundsDidChangeNotification, object: s.contentView, queue: .main
                ) { [weak self] _ in
                    MainActor.assumeIsolated { self?.lookAtPointer() }
                })
            scroll = s.enclosingScrollView
        }
    }
}

/// Lets the app set the cursor while it is not the active app, through the
/// window server's connection property for it. The property is private, so
/// it is looked up: where it is missing, a link's hand shows only in a key
/// window.
enum BackgroundCursor {
    @MainActor static let enabled: Bool = {
        guard let all = dlopen(nil, RTLD_NOW), let connection = dlsym(all, "_CGSDefaultConnection"),
              let setProperty = dlsym(all, "CGSSetConnectionProperty")
        else { return false }
        typealias Connection = @convention(c) () -> Int32
        typealias SetProperty = @convention(c) (Int32, Int32, CFString, CFTypeRef) -> Int32
        let c = unsafeBitCast(connection, to: Connection.self)()
        return unsafeBitCast(setProperty, to: SetProperty.self)(c, c, "SetsCursorInBackground" as CFString, kCFBooleanTrue) == 0
    }()
}
