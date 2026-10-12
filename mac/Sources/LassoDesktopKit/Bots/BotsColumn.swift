import AppKit
import SwiftUI

/// The Bots column (PLAN 23): a header with the count, a row per bot
/// (picture, name, state, its last line) most urgent first, and Manage
/// bots ↗ at the foot. Opened by a hover on the rail's Bots button.
public struct BotsListView: View {
    @ObservedObject var model: BotsListModel
    @ObservedObject var fleet: FleetModel
    /// Pictures by bot name, as the coordinator loads them.
    var avatars: [String: NSImage] = [:]

    public init(model: BotsListModel, avatars: [String: NSImage] = [:]) {
        self.model = model
        fleet = model.fleet
        self.avatars = avatars
    }

    private var row: Token { Theme.List.row }

    public var body: some View {
        let bots = model.bots
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                Text("Bots").font(Theme.Fonts.title.font).foregroundStyle(Theme.Text.primary.color)
                Spacer()
                Text("\(bots.count)").font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color)
            }
            .padding(.horizontal, row.number("pad_h"))
            .padding(.top, Theme.space("xl"))
            .padding(.bottom, Theme.space("m"))
            .parityAnchor("bots.header")
            ScrollView(.vertical) {
                VStack(alignment: .leading, spacing: 0) {
                    ForEach(bots) { rowView($0) }
                    if bots.isEmpty {
                        Text("No bots yet: make one in the web app.")
                            .font(Theme.Fonts.body.font)
                            .foregroundStyle(Theme.Text.secondary.color)
                            .padding(row.number("pad_h"))
                    }
                }
                .coordinateSpace(name: ColumnRows.space)
            }
            Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair"))
            HStack {
                Spacer()
                Button { model.onManage() } label: {
                    Text("Manage bots ↗").font(Theme.Fonts.label.font).foregroundStyle(Theme.Text.secondary.color)
                        .padding(.vertical, Theme.space("l"))
                        .padding(.horizontal, row.number("pad_h"))
                        .contentShape(Rectangle())
                }
                .buttonStyle(RailPress())
                .accessibilityLabel("Manage bots in the web app")
                .parityAnchor("bots.manage")
            }
        }
        .onPreferenceChange(ColumnRowFrames.self) { frames in MainActor.assumeIsolated { model.rowFrames = frames } }
        .panelChrome(Theme.List.size.size, anchor: "bots")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Bots")
    }

    private func subtitle(_ b: BotView) -> String {
        var parts = [b.standing.label]
        if b.standing == .working { parts.append(b.runtime) } else if let at = b.last_at.flatMap(parseISO), b.standing != .stopped,
            let age = relativeAge(Int64(at.timeIntervalSince1970 * 1000), now: model.now())
        {
            parts.append(age.replacingOccurrences(of: " ago", with: ""))
        }
        return parts.joined(separator: " · ")
    }

    private func rowView(_ b: BotView) -> some View {
        let lit = model.keyboardLit ? model.selected == b.name : model.hovered == b.name
        return Button { model.open(b) } label: {
            HStack(alignment: .top, spacing: Theme.space("l")) {
                BotAvatarView(name: b.name, image: avatars[b.name], standing: b.standing)
                VStack(alignment: .leading, spacing: Theme.space("2xs")) {
                    HStack(spacing: Theme.space("m")) {
                        Text(b.name).font(Theme.Fonts.name.font).foregroundStyle(Theme.Text.primary.color).lineLimit(1)
                        Spacer(minLength: Theme.space("m"))
                        Text(subtitle(b)).font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color).lineLimit(1)
                    }
                    if let line = b.lastLine {
                        Text(line).font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color).lineLimit(1)
                    }
                }
            }
            .padding(.horizontal, row.number("pad_h"))
            .padding(.vertical, Theme.space("m"))
            .background(
                RoundedRectangle(cornerRadius: Theme.Row.radius.pt)
                    .fill(lit ? (model.keyboardLit ? Theme.Row.selected.color : Theme.Row.hover.color) : .clear)
                    .padding(.horizontal, Theme.space("m")))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .columnRow(b.name)
        .accessibilityLabel("\(b.name), \(b.standing.label), \(b.runtime)\(b.lastLine.map { ": \($0)" } ?? "")")
        .parityAnchor("bots.row[\(b.name)]")
    }
}

private func parseISO(_ s: String) -> Date? { ISO8601DateFormatter().date(from: s) }

struct BotsHost: View {
    @ObservedObject var presence: CardPresence
    @ObservedObject var avatars: BotAvatars
    let model: BotsListModel

    var body: some View {
        BotsListView(model: model, avatars: avatars.images)
            .opacity(presence.shown ? 1 : 0)
            .offset(x: presence.shown ? 0 : Theme.Motion.cardOpen.number("slide"))
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }
}

/// The bots' pictures, loaded once per picture revision.
@MainActor
public final class BotAvatars: ObservableObject {
    @Published public private(set) var images: [String: NSImage] = [:]
    private var loaded: [String: String] = [:]
    let actions: BotActions

    public init(actions: BotActions) { self.actions = actions }

    public func refresh(_ bots: [BotView]) {
        for b in bots {
            let rev = b.avatar_image ?? ""
            guard loaded[b.name] != rev else { continue }
            loaded[b.name] = rev
            Task {
                let img = await actions.avatar(b)
                images[b.name] = img
            }
        }
    }
}

/// The Bots column's panel beside the rail, and its row preview: the
/// agents column's hover (`ColumnHover`) and the agents' preview, drawn for
/// a bot (`ConversationPreviewView`), which takes typing once clicked.
@MainActor
public final class BotsController {
    private(set) var panel: RailPanel
    let previews: BotPreviewController
    var preview: RailPanel { previews.panel }
    public let model: BotsListModel
    private let presence = CardPresence()
    var railFrame: () -> NSRect = { .zero }
    let hover: ColumnHover

    /// A bot's model for its preview (the coordinator's, as for its window).
    var makePreview: (BotView) -> BotWindowModel? = { _ in nil }

    init(model: BotsListModel, avatars: BotAvatars, media: MediaSource?) {
        self.model = model
        let size = Theme.List.size.size
        let host = NSHostingView(rootView: BotsHost(presence: presence, avatars: avatars, model: model))
        host.frame = NSRect(origin: .zero, size: size)
        panel = RailPanel(content: host, size: size)
        panel.hasShadow = true
        previews = BotPreviewController(media: media)
        hover = ColumnHover(panel: panel, column: model)
        previews.make = { [weak self] name in
            guard let self, let b = self.model.bots.first(where: { $0.name == name }) else { return nil }
            return self.makePreview(b)
        }
        panel.onKey = { [weak self] e in self?.handle(e) ?? false }
        model.onClose = { [weak self] in self?.close() }
        hover.showing = { [weak self] in self?.previews.showing }
        hover.holds = { [weak self] in
            guard let self, self.previews.isOpen else { return false }
            return self.preview.frame.contains(self.hover.pointer())
        }
        hover.onShow = { [weak self] name, row in
            guard let self else { return }
            self.previews.show(name, beside: self.panel.frame, row: row)
        }
        hover.onHide = { [weak self] in self?.previews.close() }
    }

    public var isOpen: Bool { presence.shown }
    var previewOpen: Bool { previews.isOpen }
    var isKey: Bool { panel.isKeyWindow }

    func open(takeKey: Bool) {
        if !presence.shown { model.freezeOrder() }
        panel.setFrameOrigin(originBesideRail(railFrame(), size: Theme.List.size.size))
        front(takeKey)
        withAnimationToken(Theme.Motion.cardOpen) { presence.shown = true }
        hover.start()
    }

    func takeKey() { front(true) }

    /// On the active Space (`RailPanel.orderIn`); the hover maps the pointer
    /// through whichever panel that is.
    private func front(_ takeKey: Bool) {
        panel.orderIn(takeKey: takeKey) {
            panel = $0
            hover.panel = $0
        }
    }

    func close() {
        if panel.isKeyWindow { panel.resignKey() }
        hover.stop()
        hidePreview()
        withAnimationToken(Theme.Motion.cardClose) { presence.shown = false }
        let delay = Theme.Motion.cardClose.value["duration"]?.double ?? 0
        Task { @MainActor [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(delay * 1e9) + 20_000_000)
            guard let self, !self.presence.shown else { return }
            self.panel.orderOut(nil)
            self.model.thawOrder()
        }
    }

    func follow() { if isOpen { panel.setFrameOrigin(originBesideRail(railFrame(), size: Theme.List.size.size)) } }

    func hidePreview() { previews.close() }

    /// J/K or ↑/↓ move, ⏎ opens, esc closes.
    private func handle(_ e: NSEvent) -> Bool {
        let mods = e.modifierFlags.intersection([.command, .control, .option])
        guard mods.isEmpty else { return false }
        switch e.keyCode {
        case KeyCode.escape: close()
        case KeyCode.up: move(-1)
        case KeyCode.down: move(1)
        case KeyCode.returnKey, KeyCode.keypadEnter: model.openSelected()
        default:
            if Keys.next.matches(e) { move(1) } else if Keys.previous.matches(e) { move(-1) } else { return false }
        }
        return true
    }

    private func move(_ d: Int) {
        model.move(d)
        hover.keyboardMoved()
    }
}
