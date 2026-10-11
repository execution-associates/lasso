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
            // Fades in moving away from the rail (motion.card_open's slide).
            .offset(x: presence.shown ? 0 : Theme.Motion.cardOpen.number("slide"))
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
    /// What J/K page through.
    var inbox: InboxModel?

    public init(fleet: FleetModel, actions: CardActions) {
        self.fleet = fleet
        self.actions = actions
        let size = Theme.Card.size.size
        let host = NSHostingView(rootView: CardHost(presence: presence, holder: holder))
        host.frame = NSRect(origin: .zero, size: size)
        panel = RailPanel(content: host, size: size)
        panel.hasShadow = true
        panel.onKey = { [weak self] e in self?.handle(e) ?? false }
    }

    public var isOpen: Bool { holder.model != nil }

    /// Open the card on `pane`. `takeKey` false (a hover) shows it without
    /// taking the keyboard from the app the user is typing in: its one-letter
    /// keys act only once a click or the summon key has made it key.
    public func open(_ pane: HostPane, takeKey: Bool = true) {
        if let m = holder.model, m.pane.key == pane.key {
            front(takeKey)
            return
        }
        holder.model?.stopPolling()
        let model = CardModel(pane: pane, fleet: fleet, actions: actions)
        model.inbox = inbox
        inbox?.markSeen(pane)
        model.onClose = { [weak self] in self?.close() }
        holder.model = model
        model.startPolling()
        place()
        front(takeKey)
        withAnimationToken(Theme.Motion.cardOpen) { presence.shown = true }
    }

    private func front(_ takeKey: Bool) { takeKey ? panel.makeKeyAndOrderFront(nil) : panel.orderFrontRegardless() }

    /// Whether the keyboard is the card's.
    var isKey: Bool { panel.isKeyWindow }

    func takeKey() { panel.makeKeyAndOrderFront(nil) }

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

    private func place() { panel.setFrameOrigin(originBesideRail(railFrame(), size: Theme.Card.size.size)) }

    /// The rail moved: stay beside it.
    func follow() { if isOpen { place() } }

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
