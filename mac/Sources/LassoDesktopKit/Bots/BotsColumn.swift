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
        .onPreferenceChange(BotRowFrames.self) { frames in MainActor.assumeIsolated { model.rowFrames = frames } }
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
        let selected = model.selected == b.name
        let hovered = model.hovered == b.name
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
                    .fill(selected ? Theme.Row.selected.color : (hovered ? Theme.Row.hover.color : .clear))
                    .padding(.horizontal, Theme.space("m")))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .background(GeometryReader { g in Color.clear.preference(key: BotRowFrames.self, value: [b.name: g.frame(in: .global)]) })
        .accessibilityLabel("\(b.name), \(b.standing.label), \(b.runtime)\(b.lastLine.map { ": \($0)" } ?? "")")
        .parityAnchor("bots.row[\(b.name)]")
    }
}

private func parseISO(_ s: String) -> Date? { ISO8601DateFormatter().date(from: s) }

/// The rows' frames in the window, for the column's hover poll.
struct BotRowFrames: PreferenceKey {
    static let defaultValue: [String: CGRect] = [:]
    static func reduce(value: inout [String: CGRect], nextValue: () -> [String: CGRect]) {
        value.merge(nextValue()) { $1 }
    }
}

/// The hover preview of a bot's latest exchange, left of the column.
struct BotPreviewView: View {
    @ObservedObject var model: ChatModel
    var avatar: NSImage?

    var body: some View {
        let b = model.bot
        let w = Theme.Agent.window
        VStack(spacing: 0) {
            HStack(spacing: Theme.space("l")) {
                BotAvatarView(name: b.name, image: avatar, standing: b.standing, size: w.number("avatar"))
                VStack(alignment: .leading, spacing: Theme.space("2xs")) {
                    Text(b.name).font(Theme.Fonts.title.font).foregroundStyle(Theme.Text.primary.color).lineLimit(1)
                    Text([b.standing.label, b.runtime, b.modelName].compactMap { $0 }.joined(separator: " · "))
                        .font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color).lineLimit(1)
                }
                Spacer()
            }
            .padding(.horizontal, Theme.Panel.inset.pt)
            .frame(height: w.number("header_h"))
            Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair"))
            AgentConversation(
                entries: AgentPreviewView.latestTurn(model.chat?.items ?? [], cwd: model.chat?.cwd), pending: model.pending,
                phase: model.chat == nil ? .loading : .idle, host: b.host)
            BotChoices(model: model)
        }
        .panelChrome(Theme.Agent.preview.size2("size"), anchor: "bots.preview")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("\(b.name), preview")
    }
}

@MainActor
final class BotPreviewHolder: ObservableObject {
    @Published var model: ChatModel?
}

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

struct BotPreviewHost: View {
    @ObservedObject var presence: CardPresence
    @ObservedObject var holder: BotPreviewHolder
    @ObservedObject var avatars: BotAvatars
    let media: MediaSource?

    var body: some View {
        Group {
            if let m = holder.model {
                BotPreviewView(model: m, avatar: avatars.images[m.bot.name]).environment(\.mediaSource, media)
            }
        }
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
/// agents column's pattern (a 20 Hz pointer poll, `PreviewHover`).
@MainActor
public final class BotsController {
    let panel: RailPanel
    let preview: RailPanel
    public let model: BotsListModel
    private let presence = CardPresence()
    private let previewPresence = CardPresence()
    private let previewHolder = BotPreviewHolder()
    var railFrame: () -> NSRect = { .zero }
    /// A ChatModel for a previewed bot.
    var makeChat: (BotView) -> ChatModel
    private var hoverTimer: Timer?
    private var hover = PreviewHover()

    init(model: BotsListModel, avatars: BotAvatars, media: MediaSource?, makeChat: @escaping (BotView) -> ChatModel) {
        self.model = model
        self.makeChat = makeChat
        let size = Theme.List.size.size
        let host = NSHostingView(rootView: BotsHost(presence: presence, avatars: avatars, model: model))
        host.frame = NSRect(origin: .zero, size: size)
        panel = RailPanel(content: host, size: size)
        panel.hasShadow = true
        let psize = Theme.Agent.preview.size2("size")
        let phost = NSHostingView(rootView: BotPreviewHost(presence: previewPresence, holder: previewHolder, avatars: avatars, media: media))
        phost.frame = NSRect(origin: .zero, size: psize)
        preview = RailPanel(content: phost, size: psize)
        preview.hasShadow = true
        panel.onKey = { [weak self] e in self?.handle(e) ?? false }
        model.onClose = { [weak self] in self?.close() }
    }

    public var isOpen: Bool { presence.shown }
    var previewOpen: Bool { previewPresence.shown }
    var isKey: Bool { panel.isKeyWindow }

    func open(takeKey: Bool) {
        panel.setFrameOrigin(originBesideRail(railFrame(), size: Theme.List.size.size))
        if takeKey { panel.makeKeyAndOrderFront(nil) } else { panel.orderFrontRegardless() }
        withAnimationToken(Theme.Motion.cardOpen) { presence.shown = true }
        hoverTimer?.invalidate()
        hoverTimer = Timer.scheduledTimer(withTimeInterval: 1.0 / 20, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.pollHover() }
        }
    }

    func takeKey() { panel.makeKeyAndOrderFront(nil) }

    func close() {
        if panel.isKeyWindow { panel.resignKey() }
        hoverTimer?.invalidate()
        hoverTimer = nil
        hidePreview()
        hover = PreviewHover()
        model.hovered = nil
        withAnimationToken(Theme.Motion.cardClose) { presence.shown = false }
        let delay = Theme.Motion.cardClose.value["duration"]?.double ?? 0
        Task { @MainActor [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(delay * 1e9) + 20_000_000)
            guard let self, !self.presence.shown else { return }
            self.panel.orderOut(nil)
        }
    }

    func follow() { if isOpen { panel.setFrameOrigin(originBesideRail(railFrame(), size: Theme.List.size.size)) } }

    private func rowUnderPointer() -> (key: String, rect: NSRect)? {
        let f = panel.frame
        let m = NSEvent.mouseLocation
        guard f.contains(m) else { return nil }
        let p = CGPoint(x: m.x - f.minX, y: f.maxY - m.y)
        guard let (name, r) = model.rowFrames.first(where: { $0.value.contains(p) }) else { return nil }
        return (name, NSRect(x: f.minX + r.minX, y: f.maxY - r.maxY, width: r.width, height: r.height))
    }

    private func pollHover() {
        let h = Theme.Rail.hover
        let row = rowUnderPointer()
        if model.hovered != row?.key { model.hovered = row?.key }
        let showing = previewOpen ? previewHolder.model?.bot.name : nil
        let action = hover.tick(
            row: row?.key, showing: showing, holds: previewOpen && preview.frame.contains(NSEvent.mouseLocation), now: Date(),
            openAfter: h.number("item_open_ms") / 1000, closeAfter: h.number("panel_close_ms") / 1000)
        switch action {
        case .none: break
        case .hide: hidePreview()
        case .show(let name):
            guard let row, let bot = model.bots.first(where: { $0.name == name }) else { return }
            showPreview(bot, row: row.rect)
        }
    }

    private func showPreview(_ bot: BotView, row: NSRect) {
        if previewHolder.model?.bot.name != bot.name {
            previewHolder.model?.stopPolling()
            let m = makeChat(bot)
            previewHolder.model = m
            m.startPolling()
        }
        let size = Theme.Agent.preview.size2("size")
        let vis = NSScreen.main?.visibleFrame ?? .zero
        let x = max(vis.minX, panel.frame.minX - Theme.Agent.preview.number("gap") - size.width)
        let y = min(max(vis.minY, row.maxY - size.height), vis.maxY - size.height)
        preview.setFrameOrigin(NSPoint(x: x.rounded(), y: y.rounded()))
        preview.orderFrontRegardless()
        if !previewPresence.shown { withAnimationToken(Theme.Motion.cardOpen) { previewPresence.shown = true } }
    }

    func hidePreview() {
        guard previewPresence.shown else { return }
        previewHolder.model?.stopPolling()
        withAnimationToken(Theme.Motion.cardClose) { previewPresence.shown = false }
        let delay = Theme.Motion.cardClose.value["duration"]?.double ?? 0
        Task { @MainActor [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(delay * 1e9) + 20_000_000)
            guard let self, !self.previewPresence.shown else { return }
            self.preview.orderOut(nil)
            self.previewHolder.model = nil
        }
    }

    /// J/K or ↑/↓ move, ⏎ opens, esc closes.
    private func handle(_ e: NSEvent) -> Bool {
        let mods = e.modifierFlags.intersection([.command, .control, .option])
        guard mods.isEmpty else { return false }
        switch e.keyCode {
        case KeyCode.escape: close()
        case KeyCode.up: model.move(-1)
        case KeyCode.down: model.move(1)
        case KeyCode.returnKey, KeyCode.keypadEnter: model.openSelected()
        default:
            if Keys.next.matches(e) { model.move(1) } else if Keys.previous.matches(e) { model.move(-1) } else { return false }
        }
        return true
    }
}
