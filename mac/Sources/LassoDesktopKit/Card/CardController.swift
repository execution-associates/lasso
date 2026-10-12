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
/// key vocabulary.
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
    /// Kill or Acknowledge resolved the inbox's last entry.
    var onInboxEmpty: () -> Void = {}
    /// The herdr window, focused on an agent (T, the header's button).
    var showInHerdr: ((HostPane) -> Void)?
    /// Unsent replies by agent: a card put away (a click elsewhere, esc)
    /// keeps its draft for that agent's next card.
    private(set) var drafts: [String: String] = [:]

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
            // Still fading out from a close: it comes back as it was.
            if !presence.shown {
                m.startPolling()
                place()
                withAnimationToken(Theme.Motion.cardOpen) { presence.shown = true }
            }
            front(takeKey)
            return
        }
        keepDraft()
        holder.model?.stopPolling()
        let model = CardModel(pane: pane, fleet: fleet, actions: actions)
        model.composer = drafts[pane.key] ?? ""
        model.inbox = inbox
        inbox?.markSeen(pane)
        model.keyMode = !panel.isKeyWindow ? .none : (pointerKeyed ? .lent : .full)
        model.onClose = { [weak self] in self?.close() }
        model.onInboxEmpty = { [weak self] in self?.onInboxEmpty() }
        model.onShowInHerdr = showInHerdr
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
        keepDraft()
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

    /// Keep the card's unsent reply under its agent (or drop a sent one).
    private func keepDraft() {
        guard let m = holder.model else { return }
        drafts[m.pane.key] = m.hasDraft ? m.composer : nil
    }

    /// The card's model (tests).
    var model: CardModel? { holder.model }

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
        if pointerKeyed, CardKeys.takesKeyboard.contains(action) {
            // R or L under a lending pointer: they put the cursor in a field,
            // so the keyboard becomes the card's, as a click would make it,
            // and the pointer leaving no longer takes it back.
            if action == .rename, !m.canRename {
                heldBack = true
                releasePointerKey()
                return true
            }
            Trace.event("card key \(action) under the pointer: the keyboard is the card's")
            pointerKeyed = false
            syncKeyMode()
        }
        Trace.event("card key \(action)")
        if case .close = action {
            m.disarm()
            close()
            return true
        }
        return m.perform(action)
    }
}

/// What a key does in the card or an agent window, decided before anything
/// acts (PLAN 13.7a).
public enum CardKeyAction: Equatable, Sendable, CustomStringConvertible {
    case close, attach, send, reply, rename, stop, kill, acknowledge, next, previous, editStep, takeStep, herdr, digit(Int)
    /// Not a card key: left to the system.
    case unbound
    /// A key outside the hover set while the pointer lent the card the
    /// keyboard: the keyboard goes back to the user's app.
    case handBack

    public var description: String {
        switch self {
        case .digit(let n): "\(n)"
        case .unbound: "unbound"
        case .handBack: "hand back"
        default: CardKeys.bound.first { $0.action == self }?.binding.id ?? "?"
        }
    }
}

public enum CardKeys {
    /// Every single-key binding and what it does, in the order they are
    /// tried. A surface answers the ones in its own set.
    static let bound: [(binding: KeyBinding, action: CardKeyAction)] = [
        (Keys.esc, .close), (Keys.kill, .kill), (Keys.acknowledge, .acknowledge), (Keys.next, .next),
        (Keys.previous, .previous), (Keys.attach, .attach), (Keys.send, .send), (Keys.reply, .reply),
        (Keys.rename, .rename), (Keys.editStep, .editStep), (Keys.takeStep, .takeStep), (Keys.stop, .stop),
        (Keys.herdr, .herdr),
    ]

    /// The keys a card answers when the pointer, not a click, gave it the
    /// keyboard: paging, kill (behind its confirm), acknowledge, esc, and
    /// rename and reply, which take the keyboard for good (`takesKeyboard`).
    public static let hoverSet: [KeyBinding] = [
        Keys.next, Keys.previous, Keys.kill, Keys.acknowledge, Keys.esc, Keys.rename, Keys.reply,
    ]

    /// Hover-set keys that put the cursor in a field (the name, the reply):
    /// pressed under a lending pointer, they make the keyboard the card's,
    /// as a click on it would, before they act.
    public static let takesKeyboard: [CardKeyAction] = [.rename, .reply]

    /// The inbox card's whole vocabulary.
    public static let cardSet: [KeyBinding] = [
        Keys.esc, Keys.kill, Keys.acknowledge, Keys.next, Keys.previous, Keys.attach, Keys.send, Keys.reply,
        Keys.rename, Keys.editStep, Keys.takeStep, Keys.herdr,
    ]

    /// An agent window's: the card's minus paging, Acknowledge and esc (it
    /// has no inbox entry to resolve and closes by its header).
    public static let windowSet: [KeyBinding] = [
        Keys.kill, Keys.attach, Keys.send, Keys.reply, Keys.rename, Keys.editStep, Keys.takeStep, Keys.herdr,
    ]

    /// A bot's chat: no agent to kill, rename or page to.
    public static let chatSet: [KeyBinding] = [
        Keys.esc, Keys.attach, Keys.send, Keys.reply, Keys.editStep, Keys.takeStep,
    ]

    /// A hover preview's, once a click made it the user's: no Kill, and esc closes it.
    public static let previewSet: [KeyBinding] = [
        Keys.esc, Keys.attach, Keys.send, Keys.reply, Keys.rename, Keys.editStep, Keys.takeStep,
    ]

    /// Route a key in the inbox card: ⌘. is Stop wherever the keyboard is;
    /// nil while text is being typed (the field takes it, a `j` stays a `j`);
    /// the hover set only while the pointer lent the keyboard; else the
    /// card's whole vocabulary.
    public static func route(_ e: NSEvent, editing: Bool, pointerKeyed: Bool) -> CardKeyAction? {
        if !editing, pointerKeyed, !Keys.stop.matches(e), !hoverSet.contains(where: { $0.matches(e) }) { return .handBack }
        return route(e, editing: editing, among: cardSet)
    }

    /// Route a key among `set`, the same way: ⌘. first, nothing while
    /// editing, then 1–9 and the set's keys.
    public static func route(_ e: NSEvent, editing: Bool, among set: [KeyBinding]) -> CardKeyAction? {
        // ⌘. is never text, and the field would take it as esc.
        if Keys.stop.matches(e) { return .stop }
        if editing { return nil }
        if let hit = bound.first(where: { b in b.binding.matches(e) && set.contains(b.binding) }) { return hit.action }
        if let n = Keys.digit(e) { return .digit(n) }
        return .unbound
    }
}
