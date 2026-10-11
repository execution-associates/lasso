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
    private let inboxModel: InboxModel
    private let inbox: SidePanel
    private let settingsModel: SettingsModel
    private let settings: SidePanel
    private var chat: (model: ChatModel, panel: SidePanel)?
    private var subs: Set<AnyCancellable> = []
    private var hotKey: HotKey?
    /// The hover preview beside the agents list, and each agent's own window.
    private var preview: AgentPreviewController!
    private var windows: [String: AgentWindowController] = [:]
    private let cardActions: CardActions
    private let media: MediaSource?
    private var shortcut: InboxShortcut?
    /// What was in the inbox last time it changed, to hear what is new.
    private var inboxSeen: Set<String>?
    /// The control whose panel a hover opened and nobody has clicked yet,
    /// and since when the pointer has been away from both rail and panel.
    private var hoverShown: RailModel.Item?
    private var hoverAway: Date?

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
        self.cardActions = cardActions
        media = client.map { LiveMediaSource(client: $0) }
        rail = RailController(model: RailModel(fleet: fleet))
        card = CardController(fleet: fleet, actions: cardActions)
        list = ListController(model: ListModel(fleet: fleet, actions: fleetActions))
        inboxModel = InboxModel(fleet: fleet)
        if sync != nil {
            InboxMemory.load(into: fleet)
            inboxModel.persist = true
        }
        inbox = SidePanel(size: Theme.List.size.size, content: InboxView(model: inboxModel))
        let baseURL = (client ?? .configured()).baseURL
        settingsModel = SettingsModel(
            permissions: permissions, baseURL: { [weak client] in (client?.baseURL ?? baseURL).absoluteString },
            version: { [weak sync] in sync?.version }, usableHosts: { nil })
        if let client {
            settingsModel.probe = { url in try? await client.probe(url).get("/api/version", as: VersionInfo.self) }
        }
        settings = SidePanel(size: Theme.Card.size.size, content: SettingsView(model: settingsModel))
        preview = AgentPreviewController(media: media) { [fleet, cardActions] p in CardModel(pane: p, fleet: fleet, actions: cardActions) }
        wire()
    }

    public func start() {
        rail.show()
        KeyHints.shared.start()
        hotKey = HotKey(token: Theme.summonKey) { [weak self] in self?.rail.summon() }
        let sc = InboxShortcut { [weak self] in self?.inboxShortcut() }
        sc.apply(InboxShortcutChoice.stored)
        shortcut = sc
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
            return self.card.isOpen || self.list.isOpen || self.inbox.isOpen || self.settings.isOpen || self.preview.isOpen
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
        rail.onPointer = { [weak self] p in self?.hoverTick(p) }
        rail.model.onAgents = { [weak self] in self?.toggleList() }
        rail.model.onSettings = { [weak self] in self?.openSettings() }

        list.model.onOpen = { [weak self] p in self?.openWindow(p) }
        list.onPreview = { [weak self] p, listFrame, row in
            guard let self else { return }
            if let p { self.preview.show(p, beside: listFrame, row: row) } else { self.preview.close() }
        }
        list.previewHolds = { [weak self] in
            guard let self, self.preview.isOpen else { return false }
            return self.preview.engaged || self.preview.panel.frame.contains(NSEvent.mouseLocation)
        }
        inboxModel.onOpen = { [weak self] p in self?.openCard(p) }
        inboxModel.onClose = { [weak self] in self?.inbox.close() }
        inbox.onKey = { [weak self] e in
            guard Keys.esc.matches(e) else { return false }
            self?.inbox.close()
            return true
        }
        settingsModel.onClose = { [weak self] in self?.settings.close() }
        settingsModel.onUse = { [weak self] url in self?.use(url) }
        settingsModel.onShortcut = { [weak self] c in self?.shortcut?.apply(c) }
        if let client {
            settingsModel.customSound = { [weak fleet] in fleet?.uiState.desktop_sound }
            settingsModel.upload = { [weak sync] url in
                _ = try await CustomSound.upload(url, client: client)
                await sync?.refreshUIState()
            }
            settingsModel.removeUpload = { [weak sync] in
                try await CustomSound.remove(client: client)
                await sync?.refreshUIState()
            }
            // lasso's sound record moved: bring this Mac's copy in line.
            fleet.$uiState
                .map(\.desktop_sound)
                .removeDuplicates()
                .sink { meta in Task { await CustomSound.sync(meta, client: client) } }
                .store(in: &subs)
        }
        settings.onKey = { [weak self] e in
            if self?.settingsModel.record(e) == true { return true }
            guard Keys.esc.matches(e) else { return false }
            self?.settingsModel.close()
            return true
        }

        // Something new in the inbox makes the arrival sound. The first
        // reading is a baseline; a live lasso only (fixtures make no sound).
        if sync != nil {
            Publishers.CombineLatest4(fleet.$panes, fleet.$bots, fleet.$uiState, fleet.$notices)
                .debounce(for: .milliseconds(300), scheduler: RunLoop.main)
                .sink { [weak self] _ in self?.noteArrivals() }
                .store(in: &subs)
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

    func openCard(_ p: HostPane, takeKey: Bool = true) {
        closeAll(except: card)
        card.open(p, takeKey: takeKey)
    }

    /// A click on the agents capsule: open the list, give an open list that
    /// a hover showed the keyboard, or close one that has it.
    func toggleList(takeKey: Bool = true) {
        if list.isOpen, takeKey, !list.isKey {
            list.takeKey()
            return
        }
        closeAll(except: list)
        if list.isOpen { list.close() } else { list.open(takeKey: takeKey) }
    }

    /// The inbox drops straight into its newest message, a card that J/K
    /// page through the rest; with nothing waiting it says so. A click while
    /// it is open gives it the keyboard if a hover showed it, else closes it.
    func openInbox(takeKey: Bool = true) {
        if card.isOpen || inbox.isOpen {
            if takeKey, card.isOpen, !card.isKey { card.takeKey() }
            else if takeKey, inbox.isOpen, !inbox.isKey { inbox.takeKey() }
            else if takeKey { closeAll() }
            return
        }
        Trace.event("inbox opened (\(inboxModel.panes.isEmpty ? "panel" : "card"), keyboard \(takeKey ? "taken" : "left"))")
        if let first = inboxModel.panes.first {
            openCard(first, takeKey: takeKey)
        } else {
            closeAll(except: inbox)
            inbox.open(takeKey: takeKey)
            inboxModel.markNoticesSeen()
        }
    }

    func openSettings(takeKey: Bool = true) {
        if settings.isOpen {
            if takeKey { settings.takeKey() }
            return
        }
        closeAll(except: settings)
        settingsModel.refresh()
        settings.open(takeKey: takeKey)
    }

    /// The inbox shortcut: open the inbox with the keyboard in it, or close
    /// it when it already has the keyboard.
    func inboxShortcut() {
        Trace.event("inbox shortcut")
        if (card.isOpen && card.isKey) || (inbox.isOpen && inbox.isKey) {
            closeAll()
            return
        }
        hoverShown = nil
        rail.model.setExpanded(true)
        if card.isOpen { card.takeKey() } else if inbox.isOpen { inbox.takeKey() } else { openInbox() }
    }

    /// Play the arrival sound when the inbox holds something it did not.
    private func noteArrivals() {
        let now = inboxModel.arrivalKeys
        defer { inboxSeen = now }
        guard let before = inboxSeen, !now.subtracting(before).isEmpty else { return }
        InboxSound.play()
    }

    /// Resting on a rail control opens it, without the keyboard: the user may
    /// be typing in another app, and the card's keys are single letters. A
    /// click or the summon key gives the panel the keyboard.
    func hoverOpen(_ item: RailModel.Item) {
        guard shown(item) == nil else { return }
        switch item {
        case .inbox: openInbox(takeKey: false)
        case .agents: toggleList(takeKey: false)
        case .settings: openSettings(takeKey: false)
        }
        hoverShown = item
        hoverAway = nil
    }

    /// What `item` has open beside the rail: whether it has the keyboard,
    /// and where it is.
    private func shown(_ item: RailModel.Item) -> (isKey: Bool, frame: NSRect)? {
        switch item {
        case .inbox:
            if card.isOpen { return (card.isKey, card.panel.frame) }
            if inbox.isOpen { return (inbox.isKey, inbox.panel.frame) }
        case .agents:
            if list.isOpen { return (list.isKey, list.panel.frame) }
        case .settings:
            if settings.isOpen { return (settings.isKey, settings.panel.frame) }
        }
        return nil
    }

    /// A panel a hover opened is a glance: it closes rail.hover's
    /// panel_close_ms after the pointer leaves both it and the rail, unless a
    /// click (which gives it the keyboard) made it the user's.
    private func hoverTick(_ pointer: NSPoint) {
        guard let item = hoverShown else { return }
        // A keyboard the pointer lent the inbox's card is not a click: the
        // card stays a glance, and the keyboard goes back as the pointer leaves it.
        if item == .inbox, card.isOpen {
            if card.panel.frame.contains(pointer) { card.takeKeyForPointer() } else { card.pointerLeft() }
        }
        guard let s = shown(item), !s.isKey || (item == .inbox && card.pointerKeyed) else {
            hoverShown = nil
            return
        }
        let previewHas = preview.isOpen && preview.panel.frame.contains(pointer)
        if s.frame.contains(pointer) || rail.panel.frame.contains(pointer) || previewHas || preview.engaged {
            hoverAway = nil
            return
        }
        let now = Date()
        if hoverAway == nil { hoverAway = now }
        guard now.timeIntervalSince(hoverAway!) * 1000 >= Theme.Rail.hover.number("panel_close_ms") else { return }
        hoverShown = nil
        hoverAway = nil
        closeAll()
        Trace.event("hover panel closed (pointer left)")
    }

    /// Switch to another lasso: whatever was open belonged to the old one.
    func use(_ url: URL) {
        closeAll(except: settings)
        chat?.model.stopPolling()
        chat = nil
        sync?.retarget(url)
        Trace.event("lasso \(url.absoluteString)")
    }

    /// An agent's own window: one per agent, brought forward if open. It
    /// floats free of the rail, so opening one closes nothing.
    func openWindow(_ p: HostPane) {
        preview.close()
        if let w = windows[p.key] {
            w.show()
            return
        }
        let w = AgentWindowController(
            model: CardModel(pane: p, fleet: fleet, actions: cardActions), near: list.isOpen ? list.panel.frame : rail.panel.frame,
            media: media)
        w.onClosed = { [weak self] in self?.windows[p.key] = nil }
        windows[p.key] = w
        w.show()
        Trace.event("agent window opened")
    }

    /// A bot's chat.
    func openChat(_ bot: BotView) {
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
        if m.composerFocused || chat?.panel.panel.editingText == true { return false }
        if Keys.esc.matches(e) {
            chat?.panel.close()
            return true
        }
        if let n = Keys.digit(e) {
            m.highlight(n)
            return true
        }
        switch true {
        case Keys.attach.matches(e): m.attachFile()
        case Keys.send.matches(e): if m.composer.isEmpty { m.focusComposer() } else { m.send() }
        case Keys.editStep.matches(e): m.editStep()
        case Keys.takeStep.matches(e): m.takeStep()
        default: return false
        }
        return true
    }
}
