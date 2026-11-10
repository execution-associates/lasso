import AppKit
import Foundation
import Testing

@testable import LassoDesktopKit

private func pane(_ id: String) -> HostPane {
    HostPane(
        host: "local", host_label: "titan", pane_id: id, workspace_id: "w1", workspace_label: "Agents",
        tab_id: "w1:t1", tab_label: id, cwd: "/", agent: "claude", agent_status: "idle", has_agent: true, focused: false)
}

private func key(_ code: UInt16, _ flags: NSEvent.ModifierFlags) -> NSEvent {
    NSEvent.keyEvent(
        with: .keyDown, location: .zero, modifierFlags: flags, timestamp: 0, windowNumber: 0, context: nil,
        characters: "\r", charactersIgnoringModifiers: "\r", isARepeat: false, keyCode: code)!
}

/// ⌘⏎ pops out; ⏎ alone, or with another modifier, does not.
@Test func commandReturnIsPopOut() {
    #expect(PopOut.isKey(key(KeyCode.returnKey, .command)))
    #expect(PopOut.isKey(key(KeyCode.keypadEnter, .command)))
    #expect(!PopOut.isKey(key(KeyCode.returnKey, [])))
    #expect(!PopOut.isKey(key(KeyCode.returnKey, [.command, .shift])))
    #expect(!PopOut.isKey(key(KeyCode.returnKey, [.command, .option])))
}

/// A row's second line never runs past its column: the agent keeps its
/// whole name up to 40% of the line, or more while the folder needs less.
@Test func aLongAgentNameTruncatesInsteadOfWideningTheRow() {
    let split = { (lead: CGFloat, trail: CGFloat) in LeadTrail.split(width: 310, lead: lead, trail: trail, spacing: 10, cap: 0.4) }
    #expect(split(100, 60) == (240, 60), "both fit: the agent keeps its width")
    #expect(split(280, 200) == (180, 120), "both long: the agent is capped at 40%")
    #expect(split(80, 260) == (80, 220), "a short folder lends the agent its room")
    let (l, t) = split(900, 900)
    #expect(l + t + 10 == 310, "never wider than the line")
}

/// Pop out from the agents column: the agent's window opens and the column
/// (and every panel off the rail) goes. A plain open keeps the column.
@MainActor @Test func popOutOpensTheAgentsWindowAndPutsTheColumnAway() throws {
    let fleet = FleetModel()
    let a = pane("w1:p1"), b = pane("w1:p2")
    fleet.apply(panes: AllPanes(panes: [a, b]))
    let app = AppCoordinator(fleet: fleet, sync: nil, fixture: nil)
    defer { app.closeRailPanels() }
    app.hoverOpen(.agents)
    app.list.model.open(a)
    let wa = try #require(app.agentWindow(a.key))
    defer {
        wa.model.stopPolling()
        wa.panel.orderOut(nil)
    }
    #expect(app.list.isOpen, "a click keeps the column")
    app.list.model.selected = b.key
    app.list.model.popOutSelected()
    let wb = try #require(app.agentWindow(b.key))
    defer {
        wb.model.stopPolling()
        wb.panel.orderOut(nil)
    }
    #expect(!app.list.isOpen && !app.railPanelOpen && wb.panel.isVisible)
}

/// The agents' preview's header pops out the agent it shows.
@MainActor @Test func theAgentPreviewPopsOutItsAgent() throws {
    let fleet = FleetModel()
    let a = pane("w1:p1")
    fleet.apply(panes: AllPanes(panes: [a]))
    let app = AppCoordinator(fleet: fleet, sync: nil, fixture: nil)
    defer { app.closeRailPanels() }
    app.hoverOpen(.agents)
    app.preview.onPopOut(a.key)
    let w = try #require(app.agentWindow(a.key))
    defer {
        w.model.stopPolling()
        w.panel.orderOut(nil)
    }
    #expect(!app.list.isOpen && !app.railPanelOpen)
}

/// The Bots column's pop out: the bot's window, and the column goes.
@MainActor @Test func popOutOpensTheBotsWindowAndPutsTheColumnAway() throws {
    let fleet = FleetModel()
    let json = #"{"bots":[{"id":1,"host":"local","name":"olivia","runtime":"omp","stopped":false,"state":"idle"}]}"#
    fleet.apply(bots: try JSONDecoder().decode(BotsPayload.self, from: Data(json.utf8)))
    let app = AppCoordinator(fleet: fleet, sync: nil, fixture: nil)
    defer { app.closeRailPanels() }
    app.hoverOpen(.bots)
    #expect(app.bots.isOpen)
    app.bots.model.popOut(app.bots.model.bots[0])
    let w = try #require(app.botWindow("olivia"))
    defer {
        w.model.chat.stopPolling()
        w.panel.orderOut(nil)
    }
    #expect(!app.bots.isOpen && !app.railPanelOpen && w.panel.isVisible)
}

/// The Files column's ⌘⏎ pops out the selected file, else the first.
@MainActor @Test func filesPopOutTheSelectedFile() {
    let r = RecentFiles(scope: "p", persist: false)
    let t0 = Date(timeIntervalSince1970: 1_700_000_000)
    r.add(FileOpen(path: "/r/b.md", from: "o"), opened: true, at: t0.addingTimeInterval(-120))
    r.add(FileOpen(path: "/r/a.md", from: "o"), opened: true, at: t0.addingTimeInterval(-60))
    let m = FilesListModel(recent: r, now: { t0 })
    var opened: [String] = [], popped: [String] = []
    m.onOpen = { opened.append($0.name) }
    m.onPopOut = { popped.append($0.name) }
    m.popOutSelected()
    m.move(1)
    m.move(1)
    m.popOutSelected()
    #expect(popped == ["a.md", "b.md"] && opened.isEmpty)
}
