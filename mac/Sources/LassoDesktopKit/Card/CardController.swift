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
        // A click makes the card the user's: the whole vocabulary, and no
        // giving the keyboard back when the pointer leaves.
        panel.onMouseDown = { [weak self] in
            self?.pointerKeyed = false
            self?.syncKeyMode()
        }
        // The card shows (and answers) the keys of how it holds the keyboard.
        NotificationCenter.default.addObserver(forName: NSWindow.didBecomeKeyNotification, object: panel, queue: .main) {
            [weak self] _ in MainActor.assumeIsolated { self?.syncKeyMode() }
        }
        NotificationCenter.default.addObserver(forName: NSWindow.didResignKeyNotification, object: panel, queue: .main) {
            [weak self] _ in
            MainActor.assumeIsolated {
                // Clicked away: a lent keyboard is simply gone.
                self?.pointerKeyed = false
                self?.syncKeyMode()
            }
        }
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
        model.keyMode = !panel.isKeyWindow ? .none : (pointerKeyed ? .lent : .full)
        model.onClose = { [weak self] in self?.close() }
        holder.model = model
        model.startPolling()
        place()
        front(takeKey)
        withAnimationToken(Theme.Motion.cardOpen) { presence.shown = true }
    }

    private func front(_ takeKey: Bool) {
        if takeKey { self.takeKey() } else { panel.orderFrontRegardless() }
    }

    /// Whether the keyboard is the card's.
    var isKey: Bool { panel.isKeyWindow }

    private func syncKeyMode() {
        let mode: CardModel.KeyMode = !panel.isKeyWindow ? .none : (pointerKeyed ? .lent : .full)
        if holder.model?.keyMode != mode { holder.model?.keyMode = mode }
    }

    /// The pointer, not a click, lent the card the keyboard (PLAN 13.7a):
    /// only the hover set acts, and it goes back when the pointer leaves.
    private(set) var pointerKeyed = false
    /// A key outside the hover set gave the keyboard back: it stays with the
    /// user's app until the pointer has left the card and come back.
    private var heldBack = false
    /// The keyboard was lent once on this pointer visit: if it went away (a
    /// click in another app), it is not taken again until the pointer leaves
    /// and comes back.
    private var lentThisVisit = false

    func takeKey() {
        pointerKeyed = false
        panel.makeKeyAndOrderFront(nil)
        // Opening never puts the cursor in the composer: one-letter keys page.
        panel.makeFirstResponder(nil)
        syncKeyMode()
    }

    /// The pointer is over a hover-opened card: lend it the keyboard.
    func takeKeyForPointer() {
        guard !panel.isKeyWindow, !heldBack, !lentThisVisit else { return }
        lentThisVisit = true
        pointerKeyed = true
        panel.makeKeyAndOrderFront(nil)
        panel.makeFirstResponder(nil)
        syncKeyMode()
        Trace.event("card took the keyboard (pointer over it)")
    }

    /// The pointer left the card: give back a lent keyboard, and lend it
    /// again next time the pointer comes over.
    func pointerLeft() {
        heldBack = false
        lentThisVisit = false
        releasePointerKey()
    }

    /// Give back a keyboard the pointer lent: the user's app has it again.
    func releasePointerKey() {
        guard pointerKeyed else { return }
        pointerKeyed = false
        if panel.isKeyWindow { panel.resignKey() }
        syncKeyMode()
        Trace.event("card gave the keyboard back")
    }

    func say(_ s: String) { holder.model?.say(s) }

    public func close() {
        guard presence.shown else { return }
        pointerKeyed = false
        lentThisVisit = false
        heldBack = false
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
        let action = CardKeys.route(e, editing: m.composerFocused || panel.editingText, pointerKeyed: pointerKeyed)
        guard let action else {
            // A focused composer's key: the field types it.
            return false
        }
        if case .handBack = action {
            Trace.event("card key outside the hover set: keyboard handed back")
            heldBack = true
            releasePointerKey()
            return true
        }
        Trace.event("card key \(action)")
        switch action {
        case .kill: break
        default: m.disarm()
        }
        switch action {
        case .close: close()
        case .digit(let n): m.highlight(n)
        case .attach: m.attachFile()
        case .send: if m.composer.isEmpty { m.focusComposer() } else { m.send() }
        case .kill: m.kill()
        case .acknowledge: m.acknowledge()
        case .next: m.page(1)
        case .previous: m.page(-1)
        case .editStep: m.editStep()
        case .takeStep: m.takeStep()
        case .unbound: return false
        case .handBack: break
        }
        return true
    }
}

/// What a key does in the card, decided before anything acts (PLAN 13.7a).
public enum CardKeyAction: Equatable, CustomStringConvertible {
    case close, attach, send, kill, acknowledge, next, previous, editStep, takeStep, digit(Int)
    /// Not a card key: left to the system.
    case unbound
    /// A key outside the hover set while the pointer lent the card the
    /// keyboard: the keyboard goes back to the user's app.
    case handBack

    public var description: String {
        switch self {
        case .close: "esc"
        case .attach: "a"
        case .send: "enter"
        case .kill: "x"
        case .acknowledge: "e"
        case .next: "j"
        case .previous: "k"
        case .editStep: "tab"
        case .takeStep: "right"
        case .digit(let n): "\(n)"
        case .unbound: "unbound"
        case .handBack: "hand back"
        }
    }
}

public enum CardKeys {
    /// The keys a card answers when the pointer, not a click, gave it the
    /// keyboard: paging, kill (behind its confirm), acknowledge, and esc.
    public static let hoverSet: [KeyBinding] = [Keys.next, Keys.previous, Keys.kill, Keys.acknowledge, Keys.esc]

    /// Route a key: nil while text is being typed (the field takes it, a
    /// `j` stays a `j`); the hover set only while the pointer lent the
    /// keyboard; else the card's whole vocabulary.
    public static func route(_ e: NSEvent, editing: Bool, pointerKeyed: Bool) -> CardKeyAction? {
        if editing { return nil }
        if pointerKeyed, !hoverSet.contains(where: { $0.matches(e) }) { return .handBack }
        switch true {
        case Keys.esc.matches(e): return .close
        case Keys.kill.matches(e): return .kill
        case Keys.acknowledge.matches(e): return .acknowledge
        case Keys.next.matches(e): return .next
        case Keys.previous.matches(e): return .previous
        case Keys.attach.matches(e): return .attach
        case Keys.send.matches(e): return .send
        case Keys.editStep.matches(e): return .editStep
        case Keys.takeStep.matches(e): return .takeStep
        default: break
        }
        if let n = Keys.digit(e) { return .digit(n) }
        return .unbound
    }
}
