import AppKit
import Foundation
import Testing

@testable import LassoDesktopKit

// An agent's and a bot's window and hover preview are one component
// (Conversation.swift): the same header buttons, ⌘W, a terminal button that
// shows the pane in herdr's window, and a preview that takes typing.

private func bots(_ json: String) -> [BotView] {
    try! JSONDecoder().decode(BotsPayload.self, from: Data(("{\"bots\":[" + json + "]}").utf8)).bots
}

private let running = bots(
    #"{"id":1,"host":"local","name":"homie","runtime":"omp","stopped":false,"state":"idle","pane_id":"w9:p1","dir_path":"/home/u/bots/homie"}"#)[0]
private let stopped = bots(#"{"id":2,"host":"local","name":"fitty","runtime":"claude","stopped":true,"state":"stopped"}"#)[0]
private let named = bots(#"{"id":3,"host":"local","name":"stub","runtime":"claude","stopped":false,"state":"idle"}"#)[0]

private func pane(_ id: String, label: String? = nil) -> HostPane {
    var p = HostPane(
        host: "local", host_label: "titan", pane_id: id, workspace_id: "w9", workspace_label: "Bots",
        tab_id: "w9:t1", tab_label: id, cwd: "/", agent: "omp", agent_status: "idle", has_agent: true, focused: false)
    p.agent_label = label
    return p
}

@MainActor private func botModel(_ b: BotView, fleet: FleetModel) -> BotWindowModel {
    BotWindowModel(chat: ChatModel(bot: b, actions: FixtureBotChatActions(payload: nil)), actions: FixtureBotActions(), fleet: fleet)
}

private func commandW() -> NSEvent {
    NSEvent.keyEvent(
        with: .keyDown, location: .zero, modifierFlags: .command, timestamp: 0, windowNumber: 0, context: nil,
        characters: "w", charactersIgnoringModifiers: "w", isARepeat: false, keyCode: 13)!
}

/// ⌘W closes an agent's window and a bot's, as the herdr window's does: by
/// the window's key equivalent and by its own key-down alike, with the
/// composer holding the keyboard too, menu bar or not.
@MainActor @Test func commandWClosesAgentAndBotWindows() throws {
    let fleet = FleetModel()
    let p = pane("w1:p1")
    fleet.apply(panes: AllPanes(panes: [p]))
    fleet.apply(bots: BotsPayload(bots: [running]))
    let near = NSRect(x: 900, y: 300, width: 10, height: 10)

    let card = CardModel(pane: p, fleet: fleet, actions: FixtureCardActions(chats: [:], canAcknowledge: true))
    let a = AgentWindowController(model: card, near: near, media: nil)
    var closed = 0
    a.onClosed = { closed += 1 }
    a.show(focusReply: true)
    spin { a.panel.editingText }
    #expect(a.panel.isVisible)
    #expect(a.panel.performKeyEquivalent(with: commandW()))
    #expect(!a.panel.isVisible && closed == 1)

    let m = botModel(running, fleet: fleet)
    let b = BotWindowController(model: m, near: near, media: nil)
    b.onClosed = { closed += 1 }
    b.show()
    defer { m.stopPolling() }
    #expect(b.panel.isVisible)
    b.panel.sendEvent(commandW())
    #expect(!b.panel.isVisible && closed == 2)
    // Plain w is text, not a close.
    #expect(!AgentWindowPanel.isCloseKey(key("w", 13)))
}

/// A bot's terminal button (and T) shows its herdr pane in herdr's window,
/// as an agent's does: the fleet's row for its pane_id, else the pane on
/// its host carrying its name. A stopped bot's is drawn dimmed and says why.
@MainActor @Test func aBotsTerminalButtonShowsItsPane() throws {
    let fleet = FleetModel()
    let homiePane = pane("w9:p1")
    let stubPane = pane("w9:p2", label: "stub")
    fleet.apply(panes: AllPanes(panes: [homiePane, stubPane]))
    fleet.apply(bots: BotsPayload(bots: [running, stopped, named]))
    let app = AppCoordinator(fleet: fleet, sync: nil, fixture: nil)
    defer { app.closeRailPanels() }
    app.herdrWindows = true

    let h = app.botModel(running)
    #expect(h.canShowInHerdr && h.herdrLabel == "Show homie in herdr")
    #expect(BotWindowModel.windowKeys.contains(Keys.herdr) && h.activeBindings.contains(Keys.herdr))
    #expect(h.perform(.herdr))
    #expect(app.herdrShown.last?.key == homiePane.key)

    let s = app.botModel(named)
    s.showInHerdr()
    #expect(app.herdrShown.last?.key == stubPane.key)

    let f = app.botModel(stopped)
    #expect(!f.canShowInHerdr && f.herdrLabel == "fitty isn't running: start it to see its terminal")
    #expect(!f.perform(.herdr) && !f.activeBindings.contains(Keys.herdr))
    f.showInHerdr()
    #expect(app.herdrShown.count == 2)

    // An agent's window has the same button, through the same path.
    let card = app.cardModel(homiePane)
    #expect(card.canShowInHerdr)
    card.showInHerdr()
    #expect(app.herdrShown.count == 3)

    // A fixture app has no herdr window: the button says so instead.
    app.herdrWindows = false
    let none = app.botModel(running)
    #expect(!none.canShowInHerdr && none.herdrLabel == "No herdr window for this lasso")
}

/// Resting on a bot's row previews it with the agents' preview: shown
/// without the keyboard, and once clicked its composer takes typing (a one-
/// letter key is text there, never the column's J), ⏎ sends, esc closes it.
@MainActor @Test func aBotsPreviewTakesTyping() async throws {
    let fleet = FleetModel()
    fleet.apply(bots: BotsPayload(bots: [running, named]))
    let app = AppCoordinator(fleet: fleet, sync: nil, fixture: nil)
    defer { app.closeRailPanels() }
    app.bots.hover.timerEnabled = false
    app.hoverOpen(.bots)
    await settle { app.bots.model.rowFrames.count >= 2 }
    let row = try #require(app.bots.model.rowFrames["homie"])
    let at = app.bots.hover.screenRect(row).center
    var t = Date(timeIntervalSince1970: 1000)
    app.bots.hover.pointer = { at }
    app.bots.hover.now = { t }
    for _ in 0..<4 {
        app.bots.hover.tick()
        t += 0.05
    }
    #expect(app.bots.previewOpen && app.bots.model.hovered == "homie")
    let m = try #require(app.bots.previews.model)
    #expect(m.bot.name == "homie" && !app.bots.preview.isKeyWindow)
    // Its composer is the agents' composer field.
    func field(_ v: NSView) -> ComposerTextField? {
        if let f = v as? ComposerTextField { return f }
        for sub in v.subviews { if let f = field(sub) { return f } }
        return nil
    }
    spin { app.bots.preview.contentView.flatMap(field) != nil }
    let f = try #require(app.bots.preview.contentView.flatMap(field))
    // The click: the panel takes the keyboard, the field the caret.
    app.bots.preview.makeKey()
    #expect(app.bots.preview.makeFirstResponder(f))
    spin { m.composerHasKeys }
    for (c, code) in [("j", UInt16(38)), ("o", 31)] { app.bots.preview.sendEvent(key(c, code)) }
    spin { m.composer == "jo" }
    #expect(m.composer == "jo" && m.chat.composer == "jo")
    #expect(app.bots.model.selected == nil, "j moved the column")
    // ⏎ sends through the bot's chat.
    app.bots.preview.sendEvent(key("\r", 36))
    await settle { (m.chat.actions as? FixtureBotChatActions)?.calls.contains("send") == true }
    #expect((m.chat.actions as? FixtureBotChatActions)?.calls.contains("send") == true)
    // esc leaves the field; a second esc closes the preview.
    app.bots.preview.makeFirstResponder(nil)
    #expect(app.bots.previews.handle(key("\u{1b}", 53)))
    #expect(!app.bots.previewOpen)
}

/// A double-click on the resize corner puts a window back to the size a
/// fresh one opens at, top-left kept, on its screen (one dragged small at
/// the screen's right edge comes back whole), and forgets the remembered
/// frame: closing it then remembers nothing, so the next open is a fresh
/// window's. An agent's and a bot's alike.
@MainActor @Test func theGripsDoubleClickRestoresTheDefaultSize() throws {
    let fleet = FleetModel()
    let p = pane("w1:p1")
    fleet.apply(panes: AllPanes(panes: [p]))
    fleet.apply(bots: BotsPayload(bots: [running]))
    let vis = try #require(NSScreen.screens.first?.visibleFrame)
    let size = Theme.Agent.window.size2("size")
    let small = NSRect(x: vis.maxX - 380, y: vis.maxY - 360, width: 370, height: 330)
    let near = NSRect(x: vis.midX, y: vis.midY, width: 10, height: 10)

    func check<M: Conversation>(_ w: ConversationWindowController<M>) {
        defer {
            w.model.stopPolling()
            w.panel.orderOut(nil)
        }
        #expect(w.panel.frame.size == small.size, "the remembered frame opened")
        w.defaultSize(animate: false)
        let f = w.panel.frame
        #expect(f.size == size)
        #expect(vis.contains(f), "\(f) on \(vis)")
        #expect(f.maxY == small.maxY, "the top edge stays")
        #expect(UserDefaults.standard.dictionary(forKey: M.framesKey)?[w.model.windowKey] == nil)
        w.close()
        #expect(UserDefaults.standard.dictionary(forKey: M.framesKey)?[w.model.windowKey] == nil, "a close after it remembers nothing")
    }

    let card = CardModel(pane: p, fleet: fleet, actions: FixtureCardActions(chats: [:], canAcknowledge: true))
    UserDefaults.standard.set([card.windowKey: NSStringFromRect(small)], forKey: CardModel.framesKey)
    let a = AgentWindowController(model: card, near: near, media: nil)
    a.show()
    check(a)
    // The next open is a fresh window's: the default size.
    let again = AgentWindowController(model: card, near: near, media: nil)
    #expect(again.panel.frame.size == size)

    let bot = botModel(running, fleet: fleet)
    UserDefaults.standard.set([bot.windowKey: NSStringFromRect(small)], forKey: BotWindowModel.framesKey)
    let b = BotWindowController(model: bot, near: near, media: nil)
    b.show()
    check(b)
}
