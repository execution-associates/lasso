import AppKit
import Combine
import SwiftUI

/// Owns the rail and the panels beside it (card, list, chat, onboarding; one
/// at a time), the voice turn, and where a transcript goes.
@MainActor
public final class AppCoordinator {
    public let fleet: FleetModel
    let sync: FleetSync?
    let client: LassoClient?
    let rail: RailController
    let card: CardController
    let list: ListController
    let setup: AssistantSetup
    let permissions: Permissions
    let recording: RecordingModel
    let botActions: BotChatActions
    private let onboardingModel: OnboardingModel
    private let onboarding: SidePanel
    private var chat: (model: ChatModel, panel: SidePanel)?
    private var subs: Set<AnyCancellable> = []
    private var hotKey: HotKey?

    /// Where the next transcript goes: the assistant's chat (sent), or the
    /// open card's composer (left for the user to send).
    private enum VoiceTarget { case chat, card }
    private var voiceTarget = VoiceTarget.chat

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
        let railModel = RailModel(fleet: fleet)
        rail = RailController(model: railModel)
        card = CardController(fleet: fleet, actions: cardActions)
        list = ListController(model: ListModel(fleet: fleet, actions: fleetActions))
        setup = AssistantSetup(fleet: fleet, client: client)
        let engine = VoiceEngine.selected
        let liveClient = client ?? LassoClient.configured()
        recording = RecordingModel(permissions: permissions, engineName: engine.displayName) {
            VoiceEngine.selected.transcriber(client: liveClient)
        }
        onboardingModel = OnboardingModel(
            permissions: permissions, setup: setup, client: client,
            baseURL: (client ?? .configured()).baseURL.absoluteString, engine: engine,
            version: { [weak sync] in sync?.version },
            usableHosts: { nil })
        onboarding = SidePanel(size: Theme.Card.size.size, content: OnboardingView(model: onboardingModel))
        wire(railModel)
    }

    public func start() {
        rail.show()
        hotKey = HotKey(token: Theme.summonKey) { [weak self] in self?.rail.summon() }
        if sync != nil, !OnboardingModel.isDone { openOnboarding() }
    }

    private func wire(_ railModel: RailModel) {
        let railFrame = { [weak self] in self?.rail.panel.frame ?? .zero }
        card.railFrame = railFrame
        list.railFrame = railFrame
        onboarding.railFrame = railFrame

        railModel.onPickAgent = { [weak self] p in self?.openCard(p) }
        railModel.onInbox = { [weak self] in self?.toggleList() }
        railModel.onOverflow = { [weak self] in self?.toggleList() }
        railModel.onChat = { [weak self] in self?.openChat() }
        railModel.onRecord = { [weak self] in self?.recordFromRail() }
        railModel.onAttach = { [weak self] in
            self?.openChat()
            self?.chat?.model.attachScreenshot()
        }
        list.model.onOpen = { [weak self] p in self?.openCard(p) }
        card.onRecord = { [weak self] in
            self?.voiceTarget = .card
            self?.recording.keyDown()
        }
        card.panel.onKeyUp = { [weak self] e in self?.keyUp(e) ?? false }

        onboarding.onKey = { [weak self] e in
            guard let self else { return false }
            if Keys.esc.matches(e) || Keys.send.matches(e) {
                self.onboardingModel.finish()
                return true
            }
            return false
        }
        onboardingModel.onDone = { [weak self] in
            self?.onboarding.close()
        }
        onboardingModel.onEngineChange = { [weak self] e in self?.recording.setEngine(e.displayName) }

        recording.$phase
            .map { p -> Bool in
                switch p {
                case .recording, .transcribing: true
                default: false
                }
            }
            .removeDuplicates()
            .sink { [weak railModel] on in railModel?.recording = on }
            .store(in: &subs)
        recording.onTranscript = { [weak self] t in self?.deliver(t) }
        recording.onError = { [weak self] m in self?.say(m) }

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
        if let c = chat, keep !== c.panel { c.panel.close() }
        if keep !== onboarding { onboarding.close() }
    }

    func openCard(_ p: HostPane) {
        closeAll(except: card)
        card.open(p)
    }

    func toggleList() {
        closeAll(except: list)
        list.toggle()
    }

    func openOnboarding() {
        closeAll(except: onboarding)
        Task { await onboardingModel.refresh() }
        onboarding.open()
    }

    /// The assistant's chat; without an assistant yet, onboarding instead.
    func openChat() {
        guard let bot = setup.assistant else {
            openOnboarding()
            return
        }
        if chat?.model.bot.name != bot.name {
            chat?.model.stopPolling()
            chat?.panel.close()
            let m = ChatModel(bot: bot, actions: botActions)
            let panel = SidePanel(size: Theme.Card.size.size, content: ChatView(model: m, recording: recording))
            panel.railFrame = { [weak self] in self?.rail.panel.frame ?? .zero }
            panel.onKey = { [weak self, weak m] e in
                guard let self, let m else { return false }
                return self.chatKey(e, m)
            }
            panel.onKeyUp = { [weak self] e in self?.keyUp(e) ?? false }
            panel.onClosed = { [weak m] in m?.stopPolling() }
            m.onClose = { [weak panel] in panel?.close() }
            m.onRecord = { [weak self] in
                self?.voiceTarget = .chat
                self?.recording.toggle()
            }
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
            if recording.isRecording { recording.cancel() } else { chat?.panel.close() }
            return true
        }
        if let n = Keys.digit(e) {
            m.highlight(n)
            return true
        }
        switch true {
        case Keys.record.matches(e):
            if !e.isARepeat {
                voiceTarget = .chat
                recording.keyDown()
            }
        case Keys.attach.matches(e): m.attachScreenshot()
        case Keys.send.matches(e): if m.composer.isEmpty { m.focusComposer() } else { m.send() }
        case Keys.editStep.matches(e): m.editStep()
        case Keys.takeStep.matches(e): m.takeStep()
        default: return false
        }
        return true
    }

    private func keyUp(_ e: NSEvent) -> Bool {
        guard Keys.record.matches(e) else { return false }
        recording.keyUp()
        return true
    }

    /// The rail's record tool: open the chat and start a hands-free turn
    /// (or end the one running).
    private func recordFromRail() {
        voiceTarget = .chat
        if !recording.isRecording { openChat() }
        recording.toggle()
    }

    private func deliver(_ t: Transcript) {
        switch voiceTarget {
        case .chat:
            if chat == nil || chat?.panel.isOpen == false { openChat() }
            chat?.model.sendTranscript(t.text, meta: t.meta)
        case .card:
            card.appendToComposer(t.text, engine: t.engine)
        }
    }

    private func say(_ s: String) {
        switch voiceTarget {
        case .chat: chat?.model.say(s)
        case .card: card.say(s)
        }
    }
}
