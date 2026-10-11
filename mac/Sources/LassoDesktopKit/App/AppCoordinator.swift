import AppKit
import Combine
import SwiftUI

/// Owns the rail and the panels beside it, one at a time: the inbox (the
/// rail's top button), the agents list (the capsule below it), settings (the
/// ellipsis), an agent's card and a bot's chat.
@MainActor
public final class AppCoordinator {
    public let fleet: FleetModel
    let sync: FleetSync?
    let client: LassoClient?
    let rail: RailController
    let card: CardController
    let list: ListController
    let permissions: Permissions
    let botActions: BotChatActions
    let reads = BotReads()
    private let inboxModel: InboxModel
    private let inbox: SidePanel
    private let settingsModel: SettingsModel
    private let settings: SidePanel
    private var chat: (model: ChatModel, panel: SidePanel)?
    private var subs: Set<AnyCancellable> = []
    private var hotKey: HotKey?

    public init(fleet: FleetModel, sync: FleetSync?, fixture: Fixture?) {
        self.fleet = fleet
        self.sync = sync
        client = sync?.client
        let cardActions: CardActions
        let fleetActions: FleetActions
        if let client {
            let live = LiveCardActions(client: client, fleet: fleet)
            live.refreshUIState = { [weak sync] in await sync?.refreshUIState() }
            cardActions = live
            fleetActions = LiveFleetActions(client: client, fleet: fleet, card: live)
            botActions = LiveBotChatActions(client: client, card: live)
            permissions = SystemPermissions()
        } else {
            cardActions = FixtureCardActions(
                chats: fixture?.chats ?? [:], canDismiss: fixture?.server.ui_state.dismissed_cards != nil)
            fleetActions = FixtureFleetActions()
            botActions = FixtureBotChatActions(payload: nil)
            permissions = FixedPermissions(fixture?.permissions ?? [:])
        }
        rail = RailController(model: RailModel(fleet: fleet))
        card = CardController(fleet: fleet, actions: cardActions)
        list = ListController(model: ListModel(fleet: fleet, actions: fleetActions))
        inboxModel = InboxModel(fleet: fleet, reads: reads)
        inbox = SidePanel(size: Theme.List.size.size, content: InboxView(model: inboxModel))
        let baseURL = (client ?? .configured()).baseURL
        settingsModel = SettingsModel(
            permissions: permissions, baseURL: { [weak client] in (client?.baseURL ?? baseURL).absoluteString },
            version: { [weak sync] in sync?.version }, usableHosts: { nil })
        if let client {
            settingsModel.probe = { url in try? await client.probe(url).get("/api/version", as: VersionInfo.self) }
        }
        settings = SidePanel(size: Theme.Card.size.size, content: SettingsView(model: settingsModel))
        wire()
    }

    public func start() {
        rail.show()
        KeyHints.shared.start()
        hotKey = HotKey(token: Theme.summonKey) { [weak self] in self?.rail.summon() }
        if sync != nil, !SettingsModel.seen { openSettings() }
    }

    private func wire() {
        let railFrame = { [weak self] in self?.rail.panel.frame ?? .zero }
        card.railFrame = railFrame
        list.railFrame = railFrame
        inbox.railFrame = railFrame
        settings.railFrame = railFrame
        rail.keepOpen = { [weak self] in
            guard let self else { return false }
            return self.card.isOpen || self.list.isOpen || self.inbox.isOpen || self.settings.isOpen
                || self.chat?.panel.isOpen == true
        }

        card.inbox = inboxModel
        rail.onMoved = { [weak self] in
            guard let self else { return }
            self.card.follow()
            self.list.follow()
            self.inbox.follow()
            self.settings.follow()
            self.chat?.panel.follow()
        }
        rail.model.onInbox = { [weak self] in self?.openInbox() }
        rail.model.onHoverOpen = { [weak self] item in self?.hoverOpen(item) }
        rail.model.onAgents = { [weak self] in self?.toggleList() }
        rail.model.onSettings = { [weak self] in self?.openSettings() }

        list.model.onOpen = { [weak self] p in self?.openCard(p) }
        list.model.onOpenBot = { [weak self] b in self?.openChat(b) }
        inboxModel.onOpenAgent = { [weak self] p in self?.openCard(p) }
        inboxModel.onOpenBot = { [weak self] b in self?.openChat(b) }
        inboxModel.onClose = { [weak self] in self?.inbox.close() }
        inbox.onKey = { [weak self] e in
            guard Keys.esc.matches(e) else { return false }
            self?.inbox.close()
            return true
        }
        settingsModel.onClose = { [weak self] in self?.settings.close() }
        settingsModel.onUse = { [weak self] url in self?.use(url) }
        settings.onKey = { [weak self] e in
            guard Keys.esc.matches(e) else { return false }
            self?.settingsModel.close()
            return true
        }

        fleet.$bots
            .sink { [weak self] bots in
                guard let self, let c = self.chat?.model, let b = bots.first(where: { $0.name == c.bot.name }) else { return }
                c.update(bot: b)
            }
            .store(in: &subs)
        sync?.onBotChat = { [weak self] name in
            guard let c = self?.chat?.model, c.bot.name == name else { return }
            Task { await c.load() }
        }
    }

    // MARK: panels (one beside the rail at a time)

    private func closeAll(except keep: AnyObject? = nil) {
        if keep !== card { card.close() }
        if keep !== list { list.close() }
        if keep !== inbox { inbox.close() }
        if keep !== settings { settings.close() }
        if let c = chat, keep !== c.panel { c.panel.close() }
    }

    func openCard(_ p: HostPane) {
        closeAll(except: card)
        card.open(p)
    }

    func toggleList() {
        closeAll(except: list)
        list.toggle()
    }

    /// The inbox drops straight into its newest message, a card that J/K
    /// page through the rest; with nothing waiting it says so. Pressed again
    /// while open, it closes.
    func openInbox() {
        if card.isOpen || inbox.isOpen {
            closeAll()
            return
        }
        if let first = inboxModel.panes.first {
            openCard(first)
        } else {
            closeAll(except: inbox)
            inbox.open()
        }
    }

    func openSettings() {
        closeAll(except: settings)
        settingsModel.refresh()
        settings.open()
    }

    /// Switch to another lasso: whatever was open belonged to the old one.
    func use(_ url: URL) {
        closeAll(except: settings)
        chat?.model.stopPolling()
        chat = nil
        sync?.retarget(url)
        Trace.event("lasso \(url.absoluteString)")
    }

    /// Resting on a rail control opens it; one already showing stays as is.
    func hoverOpen(_ item: RailModel.Item) {
        switch item {
        case .inbox: if !(card.isOpen || inbox.isOpen) { openInbox() }
        case .agents: if !list.isOpen { toggleList() }
        case .settings: if !settings.isOpen { openSettings() }
        }
    }

    /// A bot's chat.
    func openChat(_ bot: BotView) {
        reads.markRead(bot)
        if chat?.model.bot.name != bot.name {
            chat?.model.stopPolling()
            chat?.panel.close()
            let m = ChatModel(bot: bot, actions: botActions)
            let panel = SidePanel(size: Theme.Card.size.size, content: ChatView(model: m))
            panel.railFrame = { [weak self] in self?.rail.panel.frame ?? .zero }
            panel.onKey = { [weak self, weak m] e in
                guard let self, let m else { return false }
                return self.chatKey(e, m)
            }
            panel.onClosed = { [weak m] in m?.stopPolling() }
            m.onClose = { [weak panel] in panel?.close() }
            chat = (m, panel)
        }
        guard let c = chat else { return }
        closeAll(except: c.panel)
        c.model.startPolling()
        c.panel.open()
    }

    private func chatKey(_ e: NSEvent, _ m: ChatModel) -> Bool {
        if m.composerFocused { return false }
        if Keys.esc.matches(e) {
            chat?.panel.close()
            return true
        }
        if let n = Keys.digit(e) {
            m.highlight(n)
            return true
        }
        switch true {
        case Keys.attach.matches(e): m.attachScreenshot()
        case Keys.send.matches(e): if m.composer.isEmpty { m.focusComposer() } else { m.send() }
        case Keys.editStep.matches(e): m.editStep()
        case Keys.takeStep.matches(e): m.takeStep()
        default: return false
        }
        return true
    }
}
