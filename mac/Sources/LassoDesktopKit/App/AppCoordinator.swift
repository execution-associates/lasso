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
    /// The arrival sound and macOS's notifications (PLAN 17).
    let alerts: InboxAlerts
    private let settingsModel: SettingsModel
    let settings: SidePanel
    private var chat: (model: ChatModel, panel: SidePanel)?
    private var subs: Set<AnyCancellable> = []
    private var hotKey: HotKey?
    /// The hover preview beside the agents list, and each agent's own window.
    private(set) var preview: AgentPreviewController!
    private(set) var windows: [String: AgentWindowController] = [:]
    /// The Bots column and each bot's own window (PLAN 23).
    let bots: BotsController
    private let botActions2: BotActions
    private let avatars: BotAvatars
    private var botWindows: [String: BotWindowController] = [:]
    private let cardActions: CardActions
    private let media: MediaSource?
    private var shortcuts: [ShortcutSlot: InboxShortcut] = [:]
    private var newAgent: NewAgentController?
    /// Saves the New form's prompt as it is typed (`NewAgentDraft`).
    private var newAgentDraft: AnyCancellable?
    /// lasso's herdr in a window of its own; made on first use, live lassos only.
    private var terminal: HerdrTerminalController?
    /// lasso's browsers in a window of their own; made on first use, live
    /// lassos only. An agent's page waits in `browserAttention` (PLAN 24).
    private var browser: BrowserWindowController?
    let browserAttention = BrowserAttention()
    private(set) var banner: BrowserBanner!
    /// The files agents open for the human, each in a window (PLAN 26);
    /// made on first use, live lassos only.
    private(set) var files: FileWindows?
    /// Whether the person is at this Mac (tests stand one in).
    var filePresence: UserPresence?
    /// What an agent's file needs of lasso (tests stand one in).
    var fileSource: FileSource?
    /// The first-run panel: connect, sign in, three tips.
    private var firstRun: FirstRunController?
    private var presence: AppPresence?
    private var commandO: Any?
    /// Settings opened by a hover and not clicked yet, and since when the
    /// pointer has been away from both rail and panel.
    private var settingsGlance = false
    private var settingsAway: Date?
    /// The rail's pop-outs under the pointer (PLAN 16): nil while none is open.
    private var glance: Glance?
    /// A shortcut-opened pop-out also goes on a click elsewhere.
    private let clickAway = ClickAwayWatch()
    /// Anonymous usage analytics (PLAN 25); only a live lasso's app sends.
    var analytics: Analytics = .shared

    /// `notifier` posts macOS notifications: the live one by default, for a
    /// live lasso in the signed app; tests pass a fake, fixtures none.
    public init(fleet: FleetModel, sync: FleetSync?, fixture: Fixture?, notifier: Notifier? = nil) {
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
                chats: fixture?.chats ?? [:], canAcknowledge: fixture?.server.ui_state.dismissed_cards != nil)
            fleetActions = FixtureFleetActions()
            botActions = FixtureBotChatActions(payload: nil)
            permissions = FixedPermissions(fixture?.permissions ?? [:])
        }
        self.cardActions = cardActions
        media = client.map { LiveMediaSource(client: $0) }
        rail = RailController(model: RailModel(fleet: fleet))
        botActions2 = client.map { LiveBotActions(client: $0) } ?? FixtureBotActions()
        avatars = BotAvatars(actions: botActions2)
        bots = BotsController(model: BotsListModel(fleet: fleet), avatars: avatars, media: client.map { LiveMediaSource(client: $0) })
        card = CardController(fleet: fleet, actions: cardActions)
        list = ListController(model: ListModel(fleet: fleet, actions: fleetActions))
        inboxModel = InboxModel(fleet: fleet)
        if sync != nil {
            InboxMemory.load(into: fleet)
            inboxModel.persist = true
        }
        inbox = SidePanel(size: Theme.List.size.size, content: InboxView(model: inboxModel))
        alerts = InboxAlerts(
            fleet: fleet, inbox: inboxModel, actions: cardActions,
            notifier: notifier ?? (sync != nil && SystemNotifier.available ? SystemNotifier() : nil))
        let baseURL = (client ?? .configured()).baseURL
        settingsModel = SettingsModel(
            permissions: permissions, baseURL: { [weak client] in (client?.baseURL ?? baseURL).absoluteString },
            version: { [weak sync] in sync?.version }, usableHosts: { nil })
        settings = SidePanel(size: Theme.Card.size.size, content: SettingsView(model: settingsModel))
        preview = AgentPreviewController(media: media)
        wire()
    }

    public func start() {
        rail.show()
        let p = AppPresence(fleet: fleet)
        p.openInbox = { [weak self] in self?.showInbox() }
        p.newAgent = { [weak self] in self?.openNewAgent(.git) }
        p.openSettings = { [weak self] in self?.openSettings() }
        p.apply()
        presence = p
        settingsModel.onPresence = { [weak self] in self?.presence?.apply() }
        hotKey = HotKey(token: Theme.summonKey) { [weak self] in
            self?.feature(.shortcut_used)
            self?.rail.summon()
        }
        for slot in ShortcutSlot.allCases {
            let sc = InboxShortcut { [weak self] in self?.shortcutPressed(slot) }
            sc.apply(InboxShortcutChoice.stored(slot))
            shortcuts[slot] = sc
        }
        // ⌘O / ⇧⌘O / ⌘I, as in the web app, while a Lasso window has the
        // keyboard (never system-wide: every app's File › Open is ⌘O).
        commandO = NSEvent.addLocalMonitorForEvents(matching: .keyDown) { [weak self] e in
            let mods = e.modifierFlags.intersection([.command, .shift, .option, .control])
            let kind: NewAgentModel.Kind? =
                switch (e.keyCode, mods) {
                case (31, .command): .git
                case (31, [.command, .shift]): .scratch
                case (ShortcutSlot.i, .command): .terminal
                default: nil
                }
            guard let kind else { return e }
            MainActor.assumeIsolated {
                self?.feature(.shortcut_used)
                self?.openNewAgent(kind)
            }
            return nil
        }
        startFirstRun()
    }

    // MARK: analytics

    /// A surface opened, counted (throttled per view) from a live lasso only:
    /// fixtures, the parity harness and tests send nothing.
    private func viewed(_ v: AnalyticsView) {
        if sync != nil { analytics.view(v) }
    }

    private func feature(_ f: AnalyticsFeature) {
        if sync != nil { analytics.track(.featureUsed(f)) }
    }

    /// `install_active` once a day: how many machines, agents and bots, in
    /// buckets. Asked whenever the fleet changes; Analytics keeps it daily.
    private func noteActive() {
        guard sync?.version != nil, !fleet.offline else { return }
        analytics.dailyActive(hosts: max(1, Set(fleet.panes.map(\.host)).count), agents: fleet.agents.count, bots: fleet.bots.count)
    }

    // MARK: first run

    /// The first launch shows the first-run panel. Later launches show it
    /// only while this Mac has never reached a lasso and the configured one
    /// does not answer, asked quietly first.
    private func startFirstRun() {
        guard sync != nil else { return }
        let c = firstRunController()
        if !c.model.finished {
            c.show()
            Task { await c.model.begin() }
        } else if !c.model.everConnected {
            Task {
                await c.model.begin()
                if c.model.step != .connected { c.show() }
            }
        }
    }

    /// Open the first-run panel (the inbox does, while no lasso answers).
    /// Shown from step 3 earlier, it starts over with a quiet check.
    func openFirstRun(takeKey: Bool = true) {
        guard sync != nil else { return }
        let c = firstRunController()
        if !c.isOpen, c.model.step == .connected || c.model.step == .checking { Task { await c.model.begin() } }
        c.show(takeKey: takeKey)
        Trace.event("first run opened")
    }

    private func firstRunController() -> FirstRunController {
        if let c = firstRun { return c }
        let connector = LassoConnector.live { [weak self] in self?.firstRun?.panel }
        wire(connector)
        let m = FirstRunModel(connector: connector)
        m.configured = { [weak self] in self?.client?.baseURL ?? LassoClient.configuredURL }
        m.answers = { [weak self] in await self?.sync?.answers() ?? false }
        m.describe = { [weak self] url in
            await self?.describe(url) ?? FirstRunModel.Connected(name: "lasso", version: nil, address: LassoConnector.shown(url))
        }
        m.onClose = { [weak self] in self?.firstRun?.close() }
        m.onOpenSettings = { [weak self] in self?.openSettings() }
        // The rail is meant to be always there: add the login item once, now.
        m.onFinished = { [weak self] in self?.settingsModel.autoRegisterLoginItem() }
        m.analytics = analytics
        let c = FirstRunController(model: m)
        firstRun = c
        return c
    }

    /// Step 3's words for the lasso just reached: the machine it runs on,
    /// as Settings names it, its release and its address.
    private func describe(_ url: URL) async -> FirstRunModel.Connected {
        if let sync, sync.version == nil || fleet.panes.isEmpty { _ = await sync.answers() }
        var name = fleet.panes.first { $0.host == "local" }?.host_label
        if name == nil, let client, let h = try? await client.get("/api/hosts", as: HostsPayload.self) { name = h.local.hostname }
        return FirstRunModel.Connected(
            name: name ?? LassoConnector.shown(url), version: self.sync?.version.map(LassoChoice.release),
            address: LassoConnector.shown(url))
    }

    /// What a connector does once a lasso lets this Mac in: use it.
    private func wire(_ c: LassoConnector) {
        c.onUse = { [weak self] url in self?.use(url) }
        // A sign-in to the lasso in use keeps everything on screen; one to
        // another lasso switches to it (its tokens are in the Keychain).
        c.onSignedIn = { [weak self] url, grant in
            guard let self, let client = self.client else { return }
            if url == client.baseURL { client.signedIn(grant) } else { self.use(url) }
        }
        c.onPassword = { [weak self] url, pair in
            self?.client?.setBasicAuth(pair)
            self?.use(url)
        }
    }

    private func wire() {
        let railFrame = { [weak self] in self?.rail.panel.frame ?? .zero }
        card.railFrame = railFrame
        list.railFrame = railFrame
        inbox.railFrame = railFrame
        settings.railFrame = railFrame
        rail.keepOpen = { [weak self] in
            guard let self else { return false }
            return self.openPopOuts.contains { $0.isOpen && Self.onScreen($0.window) } || self.settings.isOpen && Self.onScreen(self.settings.panel)
        }

        clickAway.active = { [weak self] in (self?.railPanelOpen ?? false) && self?.glance?.byShortcut == true }
        clickAway.onClick = { [weak self] point, window in self?.clicked(at: point, in: window) }

        card.inbox = inboxModel
        // The last entry resolved: the card gives way to the empty inbox.
        card.onInboxEmpty = { [weak self] in
            guard let self else { return }
            let key = self.card.isKey
            self.card.close()
            self.openInbox(takeKey: key)
        }
        rail.onMoved = { [weak self] in
            guard let self else { return }
            self.card.follow()
            self.list.follow()
            self.inbox.follow()
            self.settings.follow()
            self.chat?.panel.follow()
            self.bots.follow()
            self.banner.follow()
        }
        rail.model.onInbox = { [weak self] in self?.railActivated(.inbox) }
        rail.model.onHoverOpen = { [weak self] item in self?.hoverOpen(item) }
        rail.onPointer = { [weak self] p in self?.hoverTick(p) }
        rail.model.onAgents = { [weak self] in self?.railActivated(.agents) }
        rail.model.onBots = { [weak self] in self?.railActivated(.bots) }
        bots.railFrame = railFrame
        bots.model.onOpen = { [weak self] b in self?.openBotWindow(b) }
        bots.makePreview = { [weak self] b in self?.botModel(b) }
        preview.make = { [weak self] key in
            guard let self, let p = self.fleet.agents.first(where: { $0.key == key }) else { return nil }
            return self.cardModel(p)
        }
        bots.model.onManage = { [weak self] in self?.openWeb("/bots/manage") }
        fleet.$bots.sink { [weak self] b in self?.avatars.refresh(b) }.store(in: &subs)
        rail.model.onSettings = { [weak self] in self?.openSettings() }
        rail.model.onNewAgent = { [weak self] in self?.openNewAgent(.git) }
        rail.model.onTerminal = { [weak self] in self?.openTerminal() }
        rail.model.onBrowser = { [weak self] in self?.openBrowser() }
        banner = BrowserBanner(
            attention: browserAttention, onOpen: { [weak self] in self?.openBrowser() },
            onDismiss: { [weak self] in self?.dismissBrowserOffer() })
        banner.railFrame = railFrame
        browserAttention.$request
            .map { $0 != nil }
            .removeDuplicates()
            .sink { [weak self] on in self?.rail.model.browserPulse = on }
            .store(in: &subs)
        rail.model.$expanded
            .removeDuplicates()
            .sink { [weak self] on in self?.browserAttention.rail(expanded: on) }
            .store(in: &subs)
        sync?.onBrowserOpen = { [weak self] o in self?.browserOpened(o) }
        sync?.onOpenFile = { [weak self] o in self?.fileOpened(o) }
        if client != nil { card.showInHerdr = { [weak self] p in self?.showInHerdr(p) } }
        card.openWindow = { [weak self] p in self?.openWindowFromCard(p) }

        list.model.onOpen = { [weak self] p in self?.openWindow(p) }
        list.hover.onShow = { [weak self] key, row in
            guard let self else { return }
            self.preview.show(key, beside: self.list.panel.frame, row: row)
        }
        list.hover.onHide = { [weak self] in self?.preview.close() }
        list.hover.showing = { [weak self] in self?.preview.showing }
        list.hover.holds = { [weak self] in
            guard let self, self.preview.isOpen else { return false }
            return self.preview.panel.frame.contains(self.list.hover.pointer())
        }
        inboxModel.onOpen = { [weak self] p in self?.openCard(p) }
        inboxModel.onClose = { [weak self] in self?.inbox.close() }
        inbox.onKey = { [weak self] e in
            guard Keys.esc.matches(e) else { return false }
            self?.inbox.close()
            return true
        }
        settingsModel.onClose = { [weak self] in self?.settings.close() }
        settingsModel.isRailCentred = { [weak self] in self?.rail.isCentred ?? true }
        settingsModel.centreRail = { [weak self] in self?.rail.centre() }
        if client != nil {
            let connector = LassoConnector.live { [weak self] in self?.settings.panel }
            wire(connector)
            settingsModel.connector = connector
        }
        settingsModel.onSignOut = { [weak self] in self?.client?.signOut() }
        settingsModel.lassoName = { [weak self] in self?.fleet.panes.first { $0.host == "local" }?.host_label }
        AuthState.shared.onExpired = { [weak self] in
            self?.settingsModel.showConnection()
            self?.openSettings(takeKey: false)
        }
        settingsModel.onShortcut = { [weak self] slot, c in self?.shortcuts[slot]?.apply(c) }
        list.model.onNew = { [weak self] in self?.openNewAgent(.git) }
        settingsModel.notify = { [weak fleet] in fleet?.notify }
        if let client {
            settingsModel.chooseNotify = { [weak sync] kind, on in
                try await NotifyDevice.choose(kind, on, client: client)
                await sync?.refreshNotify()
            }
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

        // Something new in the inbox makes the arrival sound and a macOS
        // notification. The first reading once lasso has answered is the
        // baseline; a live lasso only (fixtures make no sound and post nothing).
        wireAlerts()
        if sync != nil || alerts.notifier != nil {
            Publishers.CombineLatest4(fleet.$panes, fleet.$bots, fleet.$uiState, fleet.$notices)
                .debounce(for: .milliseconds(300), scheduler: RunLoop.main)
                .sink { [weak self] _ in self?.alerts.note() }
                .store(in: &subs)
            fleet.$panes
                .debounce(for: .seconds(5), scheduler: RunLoop.main)
                .sink { [weak self] _ in self?.noteActive() }
                .store(in: &subs)
        }
        settingsModel.analytics = analytics
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
        if keep !== bots { bots.close() }
        if keep !== inbox { inbox.close() }
        if keep !== settings { settings.close() }
        if let c = chat, keep !== c.panel { c.panel.close() }
    }

    func openCard(_ p: HostPane, takeKey: Bool = true) {
        closeAll(except: card)
        card.open(p, takeKey: takeKey)
        viewed(.inbox_card)
    }

    /// One of the panels that open off the rail is up (not Settings, which
    /// the user works in, nor the windows they arrange).
    var railPanelOpen: Bool {
        card.isOpen || list.isOpen || inbox.isOpen || preview.isOpen || bots.isOpen || chat?.panel.isOpen == true
    }

    /// The rail and the panels off it: a click on one keeps them.
    var railWindows: [NSWindow] {
        [rail.panel, card.panel, list.panel, inbox.panel, preview.panel, bots.panel, bots.preview] + (chat.map { [$0.panel.panel] } ?? [])
    }

    /// A mouse-down while a rail panel is open (ClickAwayWatch): anywhere
    /// but the rail and its panels puts them away.
    func clicked(at point: NSPoint, in window: NSWindow?) {
        guard railPanelOpen, ClickAway.closes(point: point, window: window, kept: railWindows) else { return }
        Trace.event("rail panels closed (a click elsewhere)")
        closeRailPanels()
    }

    /// Settings' panel (tests).
    var isSettingsOpen: Bool { settings.isOpen }
    var settingsFrame: NSRect { settings.panel.frame }
    var settingsWindow: NSWindow { settings.panel }
    func closeSettings() { settings.close() }

    /// An agent's own window, if open (tests).
    func agentWindow(_ key: String) -> AgentWindowController? { windows[key] }

    /// Put away the panels that open off the rail, by their own close paths
    /// (their keyboard, lent or not, goes with them). Agent windows, the
    /// herdr window, Settings and the New Agent form stay. A card's unsent
    /// reply is kept for that agent's next card (CardController.drafts), and
    /// a bot chat's stays in its model.
    func closeRailPanels() {
        glance = nil
        clickAway.stop()
        preview.close()
        card.close()
        list.close()
        bots.close()
        inbox.close()
        chat?.panel.close()
    }

    /// The agents column: opened, or given the keyboard when it is open.
    /// Nothing closes it but the pointer leaving (or esc).
    func openList(takeKey: Bool = true) {
        if list.isOpen {
            if takeKey, !list.isKey { list.takeKey() }
            return
        }
        closeAll(except: list)
        list.open(takeKey: takeKey)
        viewed(.agents_column)
    }

    /// The Bots column: opened, or given the keyboard (J/K, ⏎) when open.
    func openBots(takeKey: Bool = true) {
        if bots.isOpen {
            if takeKey, !bots.isKey { bots.takeKey() }
            return
        }
        closeAll(except: bots)
        bots.open(takeKey: takeKey)
        viewed(.bots_column)
    }

    /// A bot's own window: one per bot, brought forward if open. Like an
    /// agent's, it floats free of the rail and outlives the column.
    func openBotWindow(_ b: BotView) {
        bots.hidePreview()
        viewed(.bot_window)
        if let w = botWindows[b.name] {
            w.show()
            return
        }
        let w = BotWindowController(model: botModel(b), near: bots.isOpen ? bots.panel.frame : rail.panel.frame, media: media)
        w.onClosed = { [weak self] in self?.botWindows[b.name] = nil }
        botWindows[b.name] = w
        w.show()
        Trace.event("bot window opened")
    }

    /// A bot as its window and its preview draw it: its chat, its routes,
    /// its picture as the column has it, and its pane in herdr's window.
    func botModel(_ b: BotView) -> BotWindowModel {
        let m = BotWindowModel(chat: ChatModel(bot: b, actions: botActions), actions: botActions2, fleet: fleet)
        m.avatar = avatars.images[b.name]
        m.openWeb = { [weak self] path in self?.openWeb(path) }
        if let client {
            m.suggestions = { bot in
                let cfg: AgentConfig? = try? await client.get("/api/agent-config", query: ["host": bot.host])
                return cfg?.harnesses?.first { $0.id == bot.runtime }?.model_suggestions ?? []
            }
        }
        if herdrWindows { m.onShowInHerdr = { [weak self] p in self?.showInHerdr(p) } }
        return m
    }

    /// An agent as its window and its preview draw it.
    func cardModel(_ p: HostPane) -> CardModel {
        let m = CardModel(pane: p, fleet: fleet, actions: cardActions)
        // Its terminal button (and T) opens the herdr window on its pane.
        if herdrWindows { m.onShowInHerdr = { [weak self] p in self?.showInHerdr(p) } }
        return m
    }

    /// Whether there is a herdr window to show a pane in: a live lasso's
    /// (tests turn it on to see what the button asks for).
    var herdrWindows: Bool {
        get { herdrWindowsOverride ?? (client != nil) }
        set { herdrWindowsOverride = newValue }
    }
    private var herdrWindowsOverride: Bool?
    /// What the terminal buttons asked herdr's window for (tests).
    private(set) var herdrShown: [HostPane] = []

    /// A bot's window, if open (tests).
    func botWindow(_ name: String) -> BotWindowController? { botWindows[name] }

    /// One of the web app's pages for this lasso, in the default browser.
    func openWeb(_ path: String) {
        guard let base = client?.baseURL, var c = URLComponents(url: base, resolvingAgainstBaseURL: false) else { return }
        let prefix = c.percentEncodedPath.hasSuffix("/") ? String(c.percentEncodedPath.dropLast()) : c.percentEncodedPath
        c.percentEncodedPath = prefix + path
        if let u = c.url { NSWorkspace.shared.open(u) }
    }

    /// A click on the inbox or agents button, or ⏎ on it in the summoned
    /// rail: its pop-out opens with the keyboard, or an open one takes it.
    /// It pins nothing: the pointer leaving closes it as after a hover. With
    /// the pointer away from the rail (the summoned rail's ⏎) it waits as a
    /// shortcut's does.
    func railActivated(_ item: RailModel.Item, byPointer: Bool? = nil) {
        let byPointer = byPointer ?? rail.panel.frame.contains(NSEvent.mouseLocation)
        switch item {
        case .inbox: openInbox(takeKey: true)
        case .agents: openList(takeKey: true)
        case .bots: openBots(takeKey: true)
        default: return
        }
        if byPointer { glance = glance ?? Glance() } else { waitForThePointer() }
    }

    /// A pop-out opened with the pointer elsewhere (a shortcut, the Dock,
    /// the menu-bar icon) waits: it goes on esc, on a click elsewhere, or once
    /// the pointer has come onto it and left.
    private func waitForThePointer() {
        guard railPanelOpen else { return }
        glance = Glance(byShortcut: true)
        clickAway.start()
    }

    /// Open the inbox from somewhere the pointer is not: the Dock icon, the
    /// menu-bar icon's Open Inbox.
    func showInbox() {
        hideSettingsGlance()
        rail.model.setExpanded(true)
        openInbox()
        waitForThePointer()
    }

    /// The inbox drops straight into its newest message, a card that J/K
    /// page through the rest; with nothing waiting it says so. Asked again
    /// while open, it gives the open one the keyboard.
    func openInbox(takeKey: Bool = true) {
        // No lasso answers: there is no inbox to show, so connect instead.
        if fleet.offline, sync != nil {
            closeAll()
            openFirstRun(takeKey: takeKey)
            return
        }
        if card.isOpen || inbox.isOpen {
            if takeKey, card.isOpen, !card.isKey { card.takeKey() }
            else if takeKey, inbox.isOpen, !inbox.isKey { inbox.takeKey() }
            return
        }
        Trace.event("inbox opened (\(inboxModel.panes.isEmpty ? "panel" : "card"), keyboard \(takeKey ? "taken" : "left"))")
        if let first = inboxModel.panes.first {
            openCard(first, takeKey: takeKey)
        } else {
            closeAll(except: inbox)
            inbox.open(takeKey: takeKey)
            viewed(.inbox_card)
            inboxModel.markNoticesSeen()
        }
    }

    func openSettings(takeKey: Bool = true) {
        if settings.isOpen {
            if takeKey { settings.takeKey() }
            return
        }
        closeAll(except: settings)
        // Nothing answers, or the lasso signed this Mac out: open on the connection.
        if fleet.offline || AuthState.shared.expired != nil { settingsModel.showConnection() }
        settingsModel.refresh()
        Task { await sync?.refreshNotify() }
        settings.open(takeKey: takeKey)
        viewed(.settings)
    }

    func shortcutPressed(_ slot: ShortcutSlot) {
        feature(.shortcut_used)
        switch slot {
        case .inbox: inboxShortcut()
        case .newAgent: openNewAgent(.git)
        case .newScratch: openNewAgent(.scratch)
        case .newTerminal: openNewAgent(.terminal)
        }
    }

    /// The New Agent form; on create, it closes and the agent's own window
    /// opens on the pane it boots in.
    func openNewAgent(_ kind: NewAgentModel.Kind) {
        guard let client else { return }
        viewed(.new_form)
        if let n = newAgent {
            if n.model.kind != kind {
                n.model.kind = kind
                Task { await n.model.load() }
            }
            n.show()
            return
        }
        let m = NewAgentModel(kind: kind, source: LiveNewAgentSource(client: client))
        m.prompt = NewAgentDraft.text
        newAgentDraft = m.$prompt.dropFirst().removeDuplicates().sink { NewAgentDraft.text = $0 }
        let c = NewAgentController(model: m)
        m.onClose = { [weak self] in
            self?.newAgent?.close()
            self?.newAgent = nil
        }
        m.onCreated = { [weak self, weak m] pane in
            guard let self else { return }
            if let m, self.sync != nil, let kind = AnalyticsAgentKind(rawValue: m.kind.rawValue) {
                self.analytics.track(.agentCreated(AnalyticsHarness(id: m.agent), kind))
            }
            let frame = c.panel.frame
            NewAgentDraft.text = ""
            self.newAgent?.close()
            self.newAgent = nil
            Trace.event("agent created")
            // The form has let go of the keyboard; the window takes it with
            // the cursor in its composer, to write to the agent at once.
            self.openWindow(pane, near: frame, focusReply: true)
        }
        // A terminal: the herdr window on its host, on its pane.
        m.onTerminal = { [weak self] pane, note in
            guard let self else { return }
            NewAgentDraft.text = ""
            self.newAgent?.close()
            self.newAgent = nil
            Trace.event("terminal created")
            self.showInHerdr(pane, note: note)
        }
        newAgent = c
        closeAll()
        c.show()
    }

    /// The inbox shortcut: open the inbox with the keyboard in it, or close
    /// it when it already has the keyboard.
    func inboxShortcut() {
        Trace.event("inbox shortcut")
        if (card.isOpen && card.isKey) || (inbox.isOpen && inbox.isKey) {
            closeAll()
            return
        }
        hideSettingsGlance()
        rail.model.setExpanded(true)
        if fleet.offline, sync != nil { return openFirstRun() }
        if card.isOpen { card.takeKey() } else if inbox.isOpen { inbox.takeKey() } else { openInbox() }
        waitForThePointer()
    }

    private func wireAlerts() {
        let fleet = fleet
        if sync != nil {
            alerts.playSound = { InboxSound.play() }
            alerts.ready = { fleet.ready }
            alerts.mayAsk = { UserDefaults.standard.bool(forKey: FirstRunModel.doneKey) }
        }
        alerts.showing = { [weak self] a in
            guard let self else { return false }
            switch a.item {
            case .entry(let e): return self.card.shown?.pane.key == e.pane.key
            case .notice: return self.inbox.isOpen
            }
        }
        alerts.picture = { [weak self] name in self?.avatars.images[name] }
        let actions = cardActions
        // Acknowledge from a notification is the card's own when the card
        // shows that entry, so the card moves on as after an A.
        alerts.acknowledgeEntry = { [weak self] e in
            if let m = self?.card.shown, m.pane.key == e.pane.key {
                m.acknowledge()
                return
            }
            try await actions.acknowledge(e.pane)
            fleet.markAcknowledged(e)
        }
        alerts.openEntry = { [weak self] p in self?.openFromNotification(p) }
        alerts.openNotices = { [weak self] in self?.showNotices() }
        alerts.openInbox = { [weak self] in self?.showInbox() }
        inboxModel.onSeen = { [weak self] p in self?.alerts.seen(p) }
        inboxModel.onNoticesSeen = { [weak self] in self?.alerts.noticesSeen() }
        settingsModel.notifier = alerts.notifier
    }

    /// A notification clicked: the inbox card on its entry, opened as the
    /// inbox shortcut opens it (an explicit open: it stays until esc, a
    /// click elsewhere, or the pointer coming onto it and leaving).
    func openFromNotification(_ p: HostPane) {
        Trace.event("inbox opened from a notification")
        hideSettingsGlance()
        rail.model.setExpanded(true)
        if fleet.offline, sync != nil { return openFirstRun() }
        openCard(p)
        waitForThePointer()
    }

    /// A notice's notification clicked: the inbox panel, where notices list.
    func showNotices() {
        Trace.event("notices opened from a notification")
        hideSettingsGlance()
        rail.model.setExpanded(true)
        if !inbox.isOpen {
            closeAll(except: inbox)
            inbox.open(takeKey: true)
        } else if !inbox.isKey {
            inbox.takeKey()
        }
        inboxModel.markNoticesSeen()
        waitForThePointer()
    }

    /// Resting on a rail control opens it, without the keyboard: the user may
    /// be typing in another app, and the card's keys are single letters. A
    /// click gives the panel the keyboard. The plus only lights up: the form
    /// takes the keyboard, so it opens on a click. So do the terminal and the
    /// browser, for the same reason. Resting on one of those still moves on
    /// from what was open: the columns, the inbox, the previews and a
    /// hover's Settings close, as they do when another control opens.
    func hoverOpen(_ item: RailModel.Item) {
        switch item {
        case .inbox:
            guard !card.isOpen, !inbox.isOpen else { return }
            openInbox(takeKey: false)
        case .agents:
            guard !list.isOpen else { return }
            openList(takeKey: false)
        case .bots:
            guard !bots.isOpen else { return }
            openBots(takeKey: false)
        case .settings:
            guard !settings.isOpen else { return }
            openSettings(takeKey: false)
            settingsGlance = true
            settingsAway = nil
            return
        case .newAgent, .terminal, .browser:
            moveOffPopOuts()
            return
        }
        if glance?.byShortcut != true { glance = Glance() }
    }

    /// The pointer rested on a control with nothing to show on hover: the
    /// pop-outs it left close. Settings stays when it was clicked open (the
    /// user works in it); the windows the user opened stay.
    private func moveOffPopOuts() {
        preview.close()
        bots.hidePreview()
        card.close()
        list.close()
        bots.close()
        inbox.close()
        chat?.panel.close()
        if settingsGlance {
            settings.close()
            hideSettingsGlance()
        }
        glance = nil
    }

    private func hideSettingsGlance() {
        settingsGlance = false
        settingsAway = nil
    }

    /// The rail's pop-outs and the rail: the pointer on any of them is on Lasso's rail.
    private func onRail(_ pointer: NSPoint) -> Bool {
        var frames = [rail.panel.frame]
        if card.isOpen { frames.append(card.panel.frame) }
        if inbox.isOpen { frames.append(inbox.panel.frame) }
        if list.isOpen { frames.append(list.panel.frame) }
        if bots.isOpen { frames.append(bots.panel.frame) }
        if bots.previewOpen { frames.append(bots.preview.frame) }
        if preview.isOpen { frames.append(preview.panel.frame) }
        if let c = chat, c.panel.isOpen { frames.append(c.panel.panel.frame) }
        return frames.contains { $0.contains(pointer) }
    }

    private func hoverTick(_ pointer: NSPoint) { pointerTick(pointer, now: Date()) }

    /// The rail's pop-outs: whether each counts as open, its window, and
    /// how it closes.
    private var openPopOuts: [(isOpen: Bool, window: NSWindow, close: () -> Void)] {
        var out: [(isOpen: Bool, window: NSWindow, close: () -> Void)] = [
            (card.isOpen, card.panel, { [card] in card.close() }),
            (inbox.isOpen, inbox.panel, { [inbox] in inbox.close() }),
            (list.isOpen, list.panel, { [list] in list.close() }),
            (bots.isOpen, bots.panel, { [bots] in bots.close() }),
            (preview.isOpen, preview.panel, { [preview] in preview?.close() }),
            (bots.previewOpen, bots.preview, { [bots] in bots.hidePreview() }),
        ]
        if let c = chat { out.append((c.panel.isOpen, c.panel.panel, { c.panel.close() })) }
        return out
    }

    /// A window the user can see now: ordered in, on the Space they are on.
    static func onScreen(_ w: NSWindow) -> Bool { w.isVisible && RailPanel.onActiveSpace(w) }

    /// A pop-out that counts as open with no window the user can see (one
    /// macOS left on another Space, or ordered out under it) holds nothing:
    /// it closes, so the rail and the glance never wait on it.
    private func closeUnseenPopOuts() {
        for p in openPopOuts where p.isOpen && !Self.onScreen(p.window) {
            p.close()
            Trace.event("a pop-out open out of sight: closed")
        }
    }

    /// Every rail poll (20 Hz). The rail's pop-outs live under the pointer
    /// (`Glance`): however they opened, a click on a rail button included,
    /// they close rail.hover's panel_close_ms after the pointer has left the
    /// rail and all of them. One a shortcut opened waits for the pointer to
    /// come first. Settings a hover opened is a glance until clicked.
    func pointerTick(_ pointer: NSPoint, now: Date) {
        settingsTick(pointer, now: now)
        closeUnseenPopOuts()
        guard railPanelOpen else {
            glance = nil
            return
        }
        // The inbox is the rail and its card: a pointer on either lends the
        // card the keyboard, so j/k work the moment it opens under the pointer.
        if card.isOpen {
            if card.panel.frame.contains(pointer) || rail.panel.frame.contains(pointer) {
                card.takeKeyForPointer()
            } else {
                card.pointerLeft()
            }
        }
        var g = glance ?? Glance()
        let close = g.tick(inside: onRail(pointer), now: now, grace: Theme.Rail.hover.number("panel_close_ms") / 1000)
        glance = g
        guard close else { return }
        closeRailPanels()
        Trace.event("rail pop-outs closed (pointer left)")
    }

    private func settingsTick(_ pointer: NSPoint, now: Date) {
        guard settingsGlance else { return }
        guard settings.isOpen, !settings.isKey else { return hideSettingsGlance() }
        if settings.panel.frame.contains(pointer) || rail.panel.frame.contains(pointer) {
            settingsAway = nil
            return
        }
        if settingsAway == nil { settingsAway = now }
        guard now.timeIntervalSince(settingsAway!) * 1000 >= Theme.Rail.hover.number("panel_close_ms") else { return }
        hideSettingsGlance()
        settings.close()
        Trace.event("settings closed (pointer left)")
    }

    /// Switch to another lasso: whatever was open belonged to the old one.
    func use(_ url: URL) {
        closeAll(except: settings)
        terminal?.close()
        terminal = nil
        browser?.close()
        browser = nil
        files?.closeAll()
        files = nil
        browserAttention.clear()
        banner.hide()
        chat?.model.stopPolling()
        chat = nil
        alerts.reset()
        sync?.retarget(url)
        Trace.event("lasso \(url.absoluteString)")
    }

    /// An agent's own window: one per agent, brought forward if open. It
    /// floats free of the rail, so opening one closes nothing. `focusReply`
    /// puts the cursor in its composer (a new agent's window).
    func openWindow(_ p: HostPane, near anchor: NSRect? = nil, focusReply: Bool = false) {
        preview.close()
        viewed(.agent_window)
        if let w = windows[p.key] {
            w.show(focusReply: focusReply)
            return
        }
        let model = cardModel(p)
        let w = AgentWindowController(
            model: model, near: anchor ?? (list.isOpen ? list.panel.frame : rail.panel.frame), media: media)
        w.onClosed = { [weak self] in self?.windows[p.key] = nil }
        windows[p.key] = w
        w.show(focusReply: focusReply)
        Trace.event("agent window opened")
    }

    /// The herdr window, made on first use (a fixture app has none).
    private func herdrWindow() -> HerdrTerminalController? {
        if let t = terminal { return t }
        guard let client else { return nil }
        let t = HerdrTerminalController(source: LiveHerdrTerminalSource(client: client), client: client, fleet: fleet)
        terminal = t
        return t
    }

    /// The rail's terminal button: lasso's herdr in its window, alone: the
    /// rail's panels go first (agent windows stay).
    func openTerminal() {
        closeRailPanels()
        viewed(.terminal_window)
        herdrWindow()?.open()
    }

    /// The Browser window, made on first use (a fixture app has none).
    private func browserWindow() -> BrowserWindowController? {
        if let b = browser { return b }
        guard let client else { return nil }
        let b = BrowserWindowController(source: LiveBrowserSource(client: client), client: client)
        browser = b
        return b
    }

    /// The rail's Browser button, or the banner's Open: the window, on the
    /// page an agent offered when one waits. The rail's panels go first.
    func openBrowser() {
        closeRailPanels()
        viewed(.browser_window)
        let offer = browserAttention.request
        browserAttention.clear()
        banner.hide()
        if let offer { browserWindow()?.show(offer) } else { browserWindow()?.open() }
    }

    /// The banner's Dismiss: the page stays open in the browser, unshown.
    func dismissBrowserOffer() {
        browserAttention.clear()
        banner.hide()
        Trace.event("browser offer dismissed")
    }

    /// An agent opened a page for the human. With the Browser window in view
    /// it shows the page there, as the web app's Browser tab does; otherwise
    /// the rail's button pulses and the banner offers it. Nothing opens by
    /// itself.
    func browserOpened(_ o: BrowserOpen) {
        if let b = browser, b.isOnScreen {
            b.show(o, raise: false)
            return
        }
        browserAttention.arrived(o)
        banner.show()
        Trace.event("browser offer from an agent")
    }

    /// The file windows, made on first use (a fixture app has none).
    func fileWindows() -> FileWindows? {
        if let f = files { return f }
        guard let client else { return nil }
        let f = FileWindows(
            client: client, source: fileSource ?? LiveFileSource(client: client), presence: filePresence ?? SystemPresence())
        f.hostLabel = { [weak self] h in self?.fleet.panes.first { $0.host == h }?.host_label }
        files = f
        return f
    }

    /// An agent opened a file for the human (`open-file`). Every Mac on this
    /// lasso hears it; one whose person is at it opens it in a window, which
    /// never takes the keyboard. Nothing opens with the setting off, for a
    /// folder, or from a lasso too old to watch and save the file safely.
    func fileOpened(_ o: FileOpen) {
        guard let f = fileWindows() else { return }
        Task {
            if let skip = await f.opened(o) {
                Trace.event("agent's file not opened: \(skip)")
            } else {
                viewed(.file_window)
            }
        }
    }

    /// The inbox card's chat button: the inbox goes, as it does for T, and
    /// the agent's own window opens (or comes forward) where the card was.
    func openWindowFromCard(_ p: HostPane) {
        let at = card.panel.frame
        closeRailPanels()
        openWindow(p, near: at)
    }

    /// An agent's header button, or T: herdr's window focused on its pane.
    func showInHerdr(_ p: HostPane, note: String? = nil) {
        herdrShown.append(p)
        closeRailPanels()
        viewed(.terminal_window)
        herdrWindow()?.show(p, note: note)
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
        viewed(.chat)
        closeAll(except: c.panel)
        c.model.startPolling()
        c.panel.open()
    }

    private func chatKey(_ e: NSEvent, _ m: ChatModel) -> Bool {
        let editing = m.composerHasKeys || chat?.panel.panel.editingText == true
        switch CardKeys.route(e, editing: editing, among: CardKeys.chatSet) {
        case .stop?:
            guard m.isWorking else { return false }
            m.stop()
        case .close?: chat?.panel.close()
        case .digit(let n)?: m.highlight(n)
        case .attach?: m.attachFile()
        case .send?: if m.composerIsEmpty { m.focusComposer() } else { m.send() }
        case .reply?: m.focusComposer()
        case .editStep?: m.editStep()
        case .takeStep?: m.takeStep()
        default: return false
        }
        return true
    }
}
