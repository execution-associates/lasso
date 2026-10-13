import AppKit
import SwiftUI
import Testing

@testable import LassoDesktopKit

// A link in the chat shows the pointing hand over its glyphs (LinkCursor.swift).

@MainActor private var linked: AttributedString {
    MessageBubble.styled("Read the [lasso docs](https://example.com/docs) before you start, then **ask**.")
}

@available(macOS 15, *)
@MainActor private func layout(_ s: AttributedString) -> LinkLayout { LinkLayout(text: s, font: Theme.Fonts.chat.font) }

/// A LinkText as a chat bubble holds it, `width` wide, laid out in a
/// hosting view; its cursor area.
@available(macOS 15, *)
@MainActor private func hosted(_ s: AttributedString, width: CGFloat) -> (NSHostingView<some View>, LinkCursorView?) {
    let host = NSHostingView(
        rootView: LinkText(s).font(Theme.Fonts.chat.font).fixedSize(horizontal: false, vertical: true)
            .frame(width: width, alignment: .leading))
    host.frame = NSRect(x: 0, y: 0, width: width, height: 400)
    host.layoutSubtreeIfNeeded()
    return (host, cursorArea(in: host))
}

@available(macOS 15, *)
@MainActor private func cursorArea(in v: NSView) -> LinkCursorView? {
    if let l = v as? LinkCursorView { return l }
    for s in v.subviews { if let l = cursorArea(in: s) { return l } }
    return nil
}

@MainActor @Test func onlyTextWithALinkIsMarked() {
    #expect(LinkRuns.hasLink(linked))
    #expect(!LinkRuns.hasLink(MessageBubble.styled("plain **bold** and `code`")))
}

/// The link's glyphs are found where they are drawn: one rect, after "Read
/// the ", on the first line; on over it, off beside and below it.
@MainActor @Test func aLinksRectsAreWhereItIsDrawn() throws {
    guard #available(macOS 15, *) else { return }
    let r = layout(linked).rects(in: CGSize(width: 300, height: 0))
    try #require(r.count == 1, "rects: \(r)")
    #expect(r[0].minX > 20 && r[0].minY < 2 && r[0].width > 40 && r[0].height > 10, "\(r[0])")
    let on = { (p: CGPoint) in r.contains { $0.contains(p) } }
    #expect(on(CGPoint(x: r[0].midX, y: r[0].midY)))
    #expect(!on(CGPoint(x: 4, y: r[0].midY)), "on \"Read\"")
    #expect(!on(CGPoint(x: r[0].maxX + 8, y: r[0].midY)), "on \"before\"")
    #expect(!on(CGPoint(x: r[0].midX, y: r[0].maxY + 8)), "on the line below")
}

/// A link broken over two lines is two rects, one per line.
@MainActor @Test func aWrappedLinkIsARectPerLine() throws {
    guard #available(macOS 15, *) else { return }
    let s = MessageBubble.styled("See [the whole of the lasso documentation for agents](https://example.com) now.")
    let r = layout(s).rects(in: CGSize(width: 200, height: 0))
    try #require(r.count == 2, "rects: \(r)")
    #expect(r[1].minY > r[0].maxY - 1 && r[1].minX < 2, "\(r)")
}

/// The marked text lays out as the plain one: drawn, the same pixels.
@MainActor @Test func theMarkedTextDrawsAsThePlainOne() throws {
    guard #available(macOS 15, *) else { return }
    func bitmap(_ v: some View) -> NSBitmapImageRep? {
        let r = ImageRenderer(content: v)
        r.proposedSize = ProposedViewSize(width: 300, height: nil)
        r.scale = 2
        return r.nsImage.flatMap { $0.tiffRepresentation }.flatMap(NSBitmapImageRep.init(data:))
    }
    let a = try #require(bitmap(LinkRuns.marked(linked).font(Theme.Fonts.chat.font).textRenderer(LinkRectsRenderer(rects: LinkRects()))))
    let b = try #require(bitmap(Text(linked).font(Theme.Fonts.chat.font)))
    try #require(a.pixelsWide == b.pixelsWide && a.pixelsHigh == b.pixelsHigh, "\(a.size) vs \(b.size)")
    var differ = 0
    for y in 0..<a.pixelsHigh {
        for x in 0..<a.pixelsWide where a.colorAt(x: x, y: y) != b.colorAt(x: x, y: y) { differ += 1 }
    }
    #expect(differ == 0, "\(differ) pixels differ")
}

/// Laid out again at the size it has on screen, a text breaks its lines
/// where it did at the width it was given: the rects the cursor area finds
/// are the ones at the bubble's width, across widths and texts.
@MainActor @Test func theCursorAreaFindsTheLinksWhereTheBubbleDrewThem() throws {
    guard #available(macOS 15, *) else { return }
    let texts = [
        "Read the [lasso docs](https://example.com/docs) before you start, then **ask**.",
        "The fix is in [PR #4211](https://github.com/x/y/pull/4211), and `swift test` passes; see [the run](https://ci.example.com/run/1) for the log.",
        "[Start here](https://example.com) — the rest of this line is long enough to wrap a few times over at narrow widths, with **bold** in it.",
    ]
    for t in texts {
        let s = MessageBubble.styled(t)
        for width in stride(from: CGFloat(140), through: 420, by: 13) {
            let (_, area) = hosted(s, width: width)
            let a = try #require(area, "no cursor area at \(width)")
            let want = layout(s).rects(in: CGSize(width: width, height: 0))
            #expect(a.rects == want, "at \(width) (text \(a.bounds.size)): \(a.rects) vs \(want)")
        }
    }
}

/// The cursor area takes no clicks: they go to the text (its link, its
/// selection). A text without links has none.
@MainActor @Test func theCursorAreaTakesNoClicks() throws {
    guard #available(macOS 15, *) else { return }
    let (_, area) = hosted(linked, width: 300)
    let a = try #require(area)
    #expect(a.hitTest(NSPoint(x: 10, y: 10)) == nil)
    #expect(hosted(MessageBubble.styled("no links **here**"), width: 300).1 == nil)
}

/// The hand is the cursor on a link, and back on the text it is the arrow:
/// in a window that is not key (a preview) AppKit sets no I-beam there. A
/// text the pointer is not on leaves the cursor alone, whichever text looks
/// first.
@MainActor @Test func theHandFollowsTheLinkUnderThePointer() throws {
    guard #available(macOS 15, *) else { return }
    let host = NSHostingView(
        rootView: VStack(alignment: .leading, spacing: 10) {
            LinkText(linked)
            LinkText(MessageBubble.styled("[Another](https://example.com/2) link, below."))
        }
        .font(Theme.Fonts.chat.font).frame(width: 300, alignment: .leading))
    let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 300, height: 200), styleMask: [.borderless], backing: .buffered, defer: true)
    window.contentView = host
    host.layoutSubtreeIfNeeded()
    func areas(_ v: NSView) -> [LinkCursorView] { (v as? LinkCursorView).map { [$0] } ?? v.subviews.flatMap(areas) }
    let found = areas(host).sorted { $0.convert(NSPoint.zero, to: nil).y > $1.convert(NSPoint.zero, to: nil).y }
    try #require(found.count == 2)
    let (a, b) = (found[0], found[1])
    let link = try #require(a.rects.first)
    NSCursor.arrow.set()

    a.look(NSPoint(x: link.midX, y: link.midY))
    #expect(NSCursor.current == NSCursor.pointingHand)
    // The pointer is off b's link and was never on one there: no change.
    b.look(NSPoint(x: 1, y: -50))
    #expect(NSCursor.current == NSCursor.pointingHand, "another text took the hand away")
    a.look(NSPoint(x: link.maxX + 10, y: link.midY))
    #expect(NSCursor.current == NSCursor.arrow, "back on the text")
    // Onto b's link before a hears the pointer left it: a leaves it be.
    a.look(NSPoint(x: link.midX, y: link.midY))
    let other = try #require(b.rects.first)
    b.look(NSPoint(x: other.midX, y: other.midY))
    a.look(NSPoint(x: link.midX, y: -100))
    #expect(NSCursor.current == NSCursor.pointingHand, "a took the hand back from b")
    b.look(NSPoint(x: other.maxX + 10, y: other.midY))
    #expect(NSCursor.current == NSCursor.arrow)
}

/// On the screen, behind a non-activating panel of an inactive app: the
/// real pointer's moves reach the text, and on the link's glyphs the cursor
/// it sets is the pointing hand; off them, on the text and above it, it is
/// not. The cursor this process set, not the system's: the test host is a
/// background-only process, whose cursor the window server does not show.
/// A real click on the link still opens it.
@MainActor @Test func thePointerIsAHandOnALink() async throws {
    guard #available(macOS 15, *), canPostClicks() else { return }
    let panel = NSPanel(
        contentRect: NSRect(x: 0, y: 0, width: 360, height: 120), styleMask: [.nonactivatingPanel, .borderless],
        backing: .buffered, defer: false)
    panel.level = .floating
    let screen = NSScreen.screens[0].frame
    panel.setFrameTopLeftPoint(NSPoint(x: screen.midX - 180, y: screen.maxY - 120))
    let opened = OpenedURL()
    panel.contentView = NSHostingView(
        rootView: LinkText(linked).font(Theme.Fonts.chat.font).textSelection(.enabled)
            .frame(width: 300, alignment: .leading).padding(20)
            .environment(\.openURL, OpenURLAction { url in
                opened.url = url
                return .handled
            }))
    panel.orderFrontRegardless()
    defer { panel.orderOut(nil) }
    try? await Task.sleep(nanoseconds: 400_000_000)

    let area = try #require(panel.contentView.flatMap(cursorArea(in:)))
    let link = try #require(area.rects.first)
    func move(_ p: NSPoint) async {
        CGEvent(mouseEventSource: nil, mouseType: .mouseMoved, mouseCursorPosition: screenPoint(area, p), mouseButton: .left)?
            .post(tap: .cghidEventTap)
        try? await Task.sleep(nanoseconds: 300_000_000)
    }
    func isHand() -> Bool { NSCursor.current == NSCursor.pointingHand }
    NSCursor.arrow.set()
    await move(NSPoint(x: 4, y: link.midY))
    #expect(!isHand(), "on \"Read\"")
    await move(NSPoint(x: link.midX, y: link.midY))
    #expect(isHand(), "on the link")
    await move(NSPoint(x: link.maxX + 10, y: link.midY))
    #expect(!isHand(), "on \"before\"")
    await move(NSPoint(x: link.midX, y: link.midY))
    await move(NSPoint(x: link.midX, y: -30))
    #expect(!isHand(), "off the text, above the link")

    await realClick(area, at: NSPoint(x: link.midX, y: link.midY))
    #expect(opened.url == URL(string: "https://example.com/docs"))
}

private final class OpenedURL: @unchecked Sendable {
    var url: URL?
}
