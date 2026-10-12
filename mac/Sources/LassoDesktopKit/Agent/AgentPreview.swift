import AppKit
import Combine
import SwiftUI

/// The hover preview: resting on an agent's row shows its latest turn to the
/// left of the agents list. It stays put (a click on the row opens the agent's
/// own window to move around), and it answers like a card: the composer and
/// the open question's choices work once it is clicked.
struct AgentPreviewView: View {
    @ObservedObject var model: CardModel
    /// The header and the phase read the fleet's live row (`model.current`),
    /// which changes without the card publishing anything.
    @ObservedObject var fleet: FleetModel

    init(model: CardModel) {
        self.model = model
        fleet = model.fleet
    }

    var body: some View {
        let p = model.current
        VStack(spacing: 0) {
            AgentHeader(pane: p, model: model) {
                Circle()
                    .fill(StateStyle.color(p.state))
                    .frame(width: Theme.space("l"), height: Theme.space("l"))
                    .parityAnchor("preview.state")
                    .accessibilityLabel(p.state.label)
            }
            AgentConversation(entries: Self.latestTurn(model.chat?.items ?? [], cwd: model.chat?.cwd), pending: model.pending, phase: model.phase, outgoing: model.outgoing, host: model.mediaHost)
            StepList(model: model)
            AgentComposer(model: model)
        }
        .panelChrome(Theme.Agent.preview.size2("size"), anchor: "preview")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("\(p.displayName), preview")
    }

    /// The agent's side of the latest turn: everything after the last
    /// message you sent, folded.
    static func latestTurn(_ items: [ChatItem], cwd: String? = nil) -> [ConversationEntry] {
        let start = items.lastIndex { $0.kind == "user" }.map { $0 + 1 } ?? 0
        let turn = Array(items[min(start, items.count)...])
        return ConversationEntry.fold(turn.isEmpty ? Array(items.suffix(1)) : turn, cwd: cwd)
    }
}

@MainActor
final class PreviewHolder: ObservableObject {
    @Published var model: CardModel?
}

struct PreviewHost: View {
    @ObservedObject var presence: CardPresence
    @ObservedObject var holder: PreviewHolder
    let media: MediaSource?

    var body: some View {
        Group { if let m = holder.model { AgentPreviewView(model: m).environment(\.mediaSource, media) } }
            .opacity(presence.shown ? 1 : 0)
            .offset(x: presence.shown ? 0 : Theme.Motion.cardOpen.number("slide"))
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }
}

/// The preview's panel, left of the agents list with its top on the hovered row.
@MainActor
final class AgentPreviewController {
    let panel: RailPanel
    private let presence = CardPresence()
    private let holder = PreviewHolder()
    private let make: (HostPane) -> CardModel

    init(media: MediaSource?, make: @escaping (HostPane) -> CardModel) {
        self.make = make
        let size = Theme.Agent.preview.size2("size")
        let host = NSHostingView(rootView: PreviewHost(presence: presence, holder: holder, media: media))
        host.frame = NSRect(origin: .zero, size: size)
        panel = RailPanel(content: host, size: size)
        panel.hasShadow = true
        panel.onKey = { [weak self] e in self?.handle(e) ?? false }
    }

    var isOpen: Bool { presence.shown }
    var pane: HostPane? { holder.model?.pane }
    /// A click in it made it the user's: it no longer closes when the pointer leaves.
    var engaged: Bool { panel.isKeyWindow }

    /// Show `pane`'s preview beside `list`, top level with `row` (screen rects).
    func show(_ pane: HostPane, beside list: NSRect, row: NSRect) {
        if holder.model?.pane.key != pane.key {
            holder.model?.stopPolling()
            let m = make(pane)
            holder.model = m
            m.startPolling()
        }
        let size = Theme.Agent.preview.size2("size")
        let vis = NSScreen.main?.visibleFrame ?? .zero
        let x = max(vis.minX, list.minX - Theme.Agent.preview.number("gap") - size.width)
        let y = min(max(vis.minY, row.maxY - size.height), vis.maxY - size.height)
        panel.setFrameOrigin(NSPoint(x: x.rounded(), y: y.rounded()))
        // Shown, not focused: the pointer brought it, not a click.
        panel.orderFrontRegardless()
        if !presence.shown { withAnimationToken(Theme.Motion.cardOpen) { presence.shown = true } }
    }

    func close() {
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

    /// A window's keys (CardKeys.previewSet) once a click made it the
    /// user's: no Kill, and esc closes it.
    private func handle(_ e: NSEvent) -> Bool {
        guard let m = holder.model,
              let a = CardKeys.route(e, editing: m.composerFocused || panel.editingText, among: CardKeys.previewSet)
        else { return false }
        if a == .close {
            close()
            return true
        }
        return m.perform(a)
    }
}
