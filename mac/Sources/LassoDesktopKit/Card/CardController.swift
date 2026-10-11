import AppKit
import Combine
import SwiftUI

/// Fades the card in and out (motion.card_open / card_close).
@MainActor
final class CardPresence: ObservableObject {
    @Published var shown = false
}

struct CardHost: View {
    @ObservedObject var presence: CardPresence
    @ObservedObject var holder: CardHolder

    var body: some View {
        Group {
            if let model = holder.model {
                CardView(model: model)
            }
        }
        .opacity(presence.shown ? 1 : 0)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }
}

@MainActor
final class CardHolder: ObservableObject {
    @Published var model: CardModel?
}

/// The card's panel: beside the rail, keyboard-driven, through the A.3
/// keycap vocabulary.
@MainActor
public final class CardController {
    let panel: RailPanel
    private let presence = CardPresence()
    private let holder = CardHolder()
    private let fleet: FleetModel
    private let actions: CardActions
    /// Where the rail is, so the card opens beside it.
    var railFrame: () -> NSRect = { .zero }
    /// V in a card: start a voice turn for its composer.
    var onRecord: () -> Void = {}

    public init(fleet: FleetModel, actions: CardActions) {
        self.fleet = fleet
        self.actions = actions
        let size = Theme.Card.size.size
        let host = NSHostingView(rootView: CardHost(presence: presence, holder: holder))
        host.frame = NSRect(origin: .zero, size: size)
        panel = RailPanel(content: host, size: size)
        panel.onKey = { [weak self] e in self?.handle(e) ?? false }
    }

    public var isOpen: Bool { holder.model != nil }

    public func open(_ pane: HostPane) {
        if let m = holder.model, m.pane.key == pane.key {
            panel.makeKeyAndOrderFront(nil)
            return
        }
        holder.model?.stopPolling()
        let model = CardModel(pane: pane, fleet: fleet, actions: actions)
        model.onClose = { [weak self] in self?.close() }
        model.onRecord = { [weak self] in self?.onRecord() }
        holder.model = model
        model.startPolling()
        place()
        panel.makeKeyAndOrderFront(nil)
        withAnimationToken(Theme.Motion.cardOpen) { presence.shown = true }
    }

    func appendToComposer(_ text: String, engine: String) { holder.model?.appendTranscript(text, engine: engine) }
    func say(_ s: String) { holder.model?.say(s) }

    public func close() {
        guard presence.shown else { return }
        holder.model?.stopPolling()
        withAnimationToken(Theme.Motion.cardClose) { presence.shown = false }
        let delay = Theme.Motion.cardClose.value["duration"]?.double ?? 0
        Task { @MainActor [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(delay * 1e9) + 20_000_000)
            guard let self, !self.presence.shown else { return }
            self.panel.orderOut(nil)
            self.holder.model = nil
        }
    }

    /// Leading side of the rail, top-aligned with it, kept on screen
    /// (Theme.Card.placement).
    private func place() {
        let rail = railFrame()
        let size = Theme.Card.size.size
        let gap = Theme.Card.placement.number("gap")
        let margin = Theme.Rail.panelMargin.pt
        let vis = NSScreen.main?.visibleFrame ?? .zero
        var x = rail.minX + margin - gap - size.width
        var y = rail.maxY - margin - size.height
        x = max(vis.minX, x)
        y = min(max(vis.minY, y), vis.maxY - size.height)
        panel.setFrameOrigin(NSPoint(x: x.rounded(), y: y.rounded()))
    }

    private func handle(_ e: NSEvent) -> Bool {
        guard let m = holder.model else { return false }
        // While the composer is being typed in, the field owns the keys; its
        // delegate turns Return into send and esc into leaving the field.
        if m.composerFocused { return false }
        if Keys.esc.matches(e) {
            close()
            return true
        }
        if !Keys.kill.matches(e) { m.disarm() }
        if let n = Keys.digit(e) {
            m.highlight(n)
            return true
        }
        switch true {
        case Keys.attach.matches(e): m.attachScreenshot()
        case Keys.record.matches(e): if !e.isARepeat { m.record() }
        case Keys.send.matches(e):
            if m.composer.isEmpty { m.focusComposer() } else { m.send() }
        case Keys.kill.matches(e): m.kill()
        case Keys.discard.matches(e): m.discard()
        case Keys.next.matches(e): m.page(1)
        case Keys.previous.matches(e): m.page(-1)
        case Keys.editStep.matches(e): m.editStep()
        case Keys.takeStep.matches(e): m.takeStep()
        default: return false
        }
        return true
    }
}
