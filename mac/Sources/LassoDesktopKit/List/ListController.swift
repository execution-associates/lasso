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
    /// Resting on a row asks for its preview (nil: put it away), with the
    /// list's and the row's screen rects.
    var onPreview: (HostPane?, NSRect, NSRect) -> Void = { _, _, _ in }
    /// The preview is under the pointer, or a click made it the user's.
    var previewHolds: () -> Bool = { false }
    private var hoverTimer: Timer?
    private var restingOn: (key: String, since: Date)?
    private var previewing: String?
    private var awaySince: Date?

    public init(model: ListModel) {
        self.model = model
        let size = Theme.List.size.size
        let host = NSHostingView(rootView: ListHost(presence: presence, model: model))
        host.frame = NSRect(origin: .zero, size: size)
        panel = RailPanel(content: host, size: size)
        panel.hasShadow = true
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
        hoverTimer?.invalidate()
        hoverTimer = Timer.scheduledTimer(withTimeInterval: 1.0 / 20, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.pollHover() }
        }
    }

    public func close() {
        hoverTimer?.invalidate()
        hoverTimer = nil
        if previewing != nil { onPreview(nil, .zero, .zero) }
        previewing = nil
        restingOn = nil
        model.hovered = nil
        withAnimationToken(Theme.Motion.cardClose) { presence.shown = false }
        model.confirmEnd = nil
        let delay = Theme.Motion.cardClose.value["duration"]?.double ?? 0
        Task { @MainActor [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(delay * 1e9) + 20_000_000)
            guard let self, !self.presence.shown else { return }
            self.panel.orderOut(nil)
        }
    }

    /// The row under the pointer, from the frames the rows report (a
    /// background app's panel gets no hover events, so the pointer is polled).
    private func rowUnderPointer() -> (key: String, rect: NSRect)? {
        let f = panel.frame
        let m = NSEvent.mouseLocation
        guard f.contains(m) else { return nil }
        let p = CGPoint(x: m.x - f.minX, y: f.maxY - m.y)
        guard let (id, r) = model.rowFrames.first(where: { $0.value.contains(p) }) else { return nil }
        let key = id.hasPrefix("pinned:") ? String(id.dropFirst(7)) : id
        return (key, NSRect(x: f.minX + r.minX, y: f.maxY - r.maxY, width: r.width, height: r.height))
    }

    /// Resting on a row for rail.hover's item_open_ms previews it; once the
    /// pointer has left the row and the preview for panel_close_ms, it goes.
    private func pollHover() {
        let h = Theme.Rail.hover
        let now = Date()
        guard let row = rowUnderPointer() else {
            if model.hovered != nil { model.hovered = nil }
            restingOn = nil
            guard previewing != nil else { return }
            if previewHolds() {
                awaySince = nil
                return
            }
            if awaySince == nil { awaySince = now }
            if now.timeIntervalSince(awaySince!) * 1000 >= h.number("panel_close_ms") {
                previewing = nil
                awaySince = nil
                onPreview(nil, .zero, .zero)
            }
            return
        }
        awaySince = nil
        if model.hovered != row.key { model.hovered = row.key }
        if restingOn?.key != row.key { restingOn = (row.key, now) }
        guard previewing != row.key, let since = restingOn?.since,
              now.timeIntervalSince(since) * 1000 >= h.number("item_open_ms"),
              let pane = model.fleet.agents.first(where: { $0.key == row.key })
        else { return }
        previewing = row.key
        onPreview(pane, panel.frame, row.rect)
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
        case KeyCode.up: model.move(-1)
        case KeyCode.down: model.move(1)
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
