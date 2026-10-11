import AppKit
import SwiftUI

struct SidePanelHost<Content: View>: View {
    @ObservedObject var presence: CardPresence
    let content: Content

    var body: some View {
        content
            .opacity(presence.shown ? 1 : 0)
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }
}

/// A panel beside the rail, in the card's place, that fades in and out with
/// the card's motion tokens: the chat and onboarding.
@MainActor
public final class SidePanel {
    let panel: RailPanel
    private let presence = CardPresence()
    var railFrame: () -> NSRect = { .zero }
    var onKey: (NSEvent) -> Bool = { _ in false }
    var onKeyUp: (NSEvent) -> Bool = { _ in false }
    var onClosed: () -> Void = {}

    public init<V: View>(size: CGSize, content: V) {
        let host = NSHostingView(rootView: SidePanelHost(presence: presence, content: content))
        host.frame = NSRect(origin: .zero, size: size)
        panel = RailPanel(content: host, size: size)
        panel.onKey = { [weak self] e in self?.onKey(e) ?? false }
        panel.onKeyUp = { [weak self] e in self?.onKeyUp(e) ?? false }
    }

    public var isOpen: Bool { presence.shown }

    public func open() {
        let rail = railFrame()
        let size = panel.frame.size
        let gap = Theme.Card.placement.number("gap")
        let margin = Theme.Rail.panelMargin.pt
        let vis = NSScreen.main?.visibleFrame ?? .zero
        let x = max(vis.minX, rail.minX + margin - gap - size.width)
        let y = min(max(vis.minY, rail.maxY - margin - size.height), vis.maxY - size.height)
        panel.setFrameOrigin(NSPoint(x: x.rounded(), y: y.rounded()))
        panel.makeKeyAndOrderFront(nil)
        withAnimationToken(Theme.Motion.cardOpen) { presence.shown = true }
    }

    public func close() {
        guard presence.shown else { return }
        withAnimationToken(Theme.Motion.cardClose) { presence.shown = false }
        let delay = Theme.Motion.cardClose.value["duration"]?.double ?? 0
        Task { @MainActor [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(delay * 1e9) + 20_000_000)
            guard let self, !self.presence.shown else { return }
            self.panel.orderOut(nil)
            self.onClosed()
        }
    }
}
