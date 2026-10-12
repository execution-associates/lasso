import AppKit
import SwiftUI

struct ListHost: View {
    @ObservedObject var presence: CardPresence
    let model: ListModel

    var body: some View {
        ListView(model: model)
            .opacity(presence.shown ? 1 : 0)
            // Fades in moving away from the rail (motion.card_open's slide).
            .offset(x: presence.shown ? 0 : Theme.Motion.cardOpen.number("slide"))
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }
}

/// The list panel, in the card's place beside the rail; the two never show
/// at once.
@MainActor
public final class ListController {
    let panel: RailPanel
    public let model: ListModel
    private let presence = CardPresence()
    var railFrame: () -> NSRect = { .zero }
    /// The pointer's row, the highlight and the preview (`ColumnHover`).
    let hover: ColumnHover

    public init(model: ListModel) {
        self.model = model
        let size = Theme.List.size.size
        let host = NSHostingView(rootView: ListHost(presence: presence, model: model))
        host.frame = NSRect(origin: .zero, size: size)
        panel = RailPanel(content: host, size: size)
        panel.hasShadow = true
        hover = ColumnHover(panel: panel, column: model)
        // A pinned copy of a row previews its agent.
        hover.keyFor = { id in id.hasPrefix("pinned:") ? String(id.dropFirst(7)) : id }
        panel.onKey = { [weak self] e in self?.handle(e) ?? false }
        model.onClose = { [weak self] in self?.close() }
    }

    public var isOpen: Bool { presence.shown }

    public func toggle() { isOpen ? close() : open() }

    /// `takeKey` false (a hover) shows the list without taking the keyboard.
    public func open(takeKey: Bool = true) {
        place()
        if takeKey { panel.makeKeyAndOrderFront(nil) } else { panel.orderFrontRegardless() }
        withAnimationToken(Theme.Motion.cardOpen) { presence.shown = true }
        hover.start()
    }

    public func close() {
        // A rename in progress is kept, as a click away keeps it, and the
        // keyboard goes back to the app that had it.
        if model.renaming != nil { model.commitRename() }
        if panel.isKeyWindow { panel.resignKey() }
        hover.stop()
        withAnimationToken(Theme.Motion.cardClose) { presence.shown = false }
        model.confirmEnd = nil
        let delay = Theme.Motion.cardClose.value["duration"]?.double ?? 0
        Task { @MainActor [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(delay * 1e9) + 20_000_000)
            guard let self, !self.presence.shown else { return }
            self.panel.orderOut(nil)
        }
    }

    private func place() { panel.setFrameOrigin(originBesideRail(railFrame(), size: Theme.List.size.size)) }

    var isKey: Bool { panel.isKeyWindow }

    func takeKey() { panel.makeKeyAndOrderFront(nil) }

    /// The rail moved: stay beside it.
    func follow() { if isOpen { place() } }

    private func handle(_ e: NSEvent) -> Bool {
        // Renaming: ⏎ keeps the name, esc keeps the old one, the rest is typing.
        if model.renaming != nil {
            switch e.keyCode {
            case KeyCode.escape: model.cancelRename()
            case KeyCode.returnKey, KeyCode.keypadEnter: model.commitRename()
            default: return false
            }
            return true
        }
        switch e.keyCode {
        case KeyCode.escape:
            if model.confirmEnd != nil { model.confirmEnd = nil } else { close() }
        case KeyCode.up:
            model.move(-1)
            hover.keyboardMoved()
        case KeyCode.down:
            model.move(1)
            hover.keyboardMoved()
        case KeyCode.returnKey, KeyCode.keypadEnter: model.openSelected()
        default: return false
        }
        return true
    }
}

/// The list's side effects through lasso's API.
@MainActor
public final class LiveFleetActions: FleetActions {
    let client: LassoClient
    let fleet: FleetModel
    let card: LiveCardActions

    public init(client: LassoClient, fleet: FleetModel, card: LiveCardActions) {
        self.client = client
        self.fleet = fleet
        self.card = card
    }

    struct Pins: Encodable {
        var client_id = "lasso-desktop"
        var agent_pins: [String: Bool]
    }

    public func pin(_ pane: HostPane, _ on: Bool) async throws {
        let state: UIStateSlice = try await client.post("/api/ui-state", json: Pins(agent_pins: [pane.key: on]))
        fleet.apply(uiState: state)
    }

    struct Focus: Encodable { var pane_id: String }
    struct Focused: Decodable { var ok: Bool? }

    public func focus(_ pane: HostPane) async throws {
        let _: Focused = try await client.post("/api/focus", query: ["host": pane.host], json: Focus(pane_id: pane.pane_id))
    }

    public func end(_ pane: HostPane) async throws -> String { try await card.kill(pane) }

    public func rename(_ pane: HostPane, to name: String) async throws { try await card.rename(pane, to: name) }
}

/// The fixtures' list actions: recorded no-ops.
@MainActor
public final class FixtureFleetActions: FleetActions {
    public private(set) var calls: [String] = []
    public init() {}
    public func pin(_ pane: HostPane, _ on: Bool) async throws { calls.append("pin") }
    public func focus(_ pane: HostPane) async throws { calls.append("focus") }
    public func rename(_ pane: HostPane, to name: String) async throws { calls.append("rename:\(name)") }
    public func end(_ pane: HostPane) async throws -> String {
        calls.append("end")
        return "fixture"
    }
}
