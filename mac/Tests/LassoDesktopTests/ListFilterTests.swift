import AppKit
import Foundation
import Testing

@testable import LassoDesktopKit

// The agents column's filter-as-you-type: the web's agentMatches (every
// word somewhere in the row), typed from anywhere in the column.

private func pane(_ i: Int, host: String = "local", label: String = "titan", agent: String = "claude", cwd: String = "/")
    -> HostPane
{
    HostPane(
        host: host, host_label: label, pane_id: "w1:p\(i)", workspace_id: "w1", workspace_label: "Agents",
        tab_id: "w1:t\(i)", tab_label: "agent\(i)", cwd: cwd, agent: agent, agent_status: "idle", has_agent: true,
        focused: false)
}

private func key(_ chars: String, code: UInt16, mods: NSEvent.ModifierFlags = []) -> NSEvent {
    NSEvent.keyEvent(
        with: .keyDown, location: .zero, modifierFlags: mods, timestamp: 0, windowNumber: 0, context: nil,
        characters: chars, charactersIgnoringModifiers: chars, isARepeat: false, keyCode: code)!
}

/// Every word must match, whichever field it came from.
@Test func filterWordsMatchAcrossFields() {
    var p = pane(1, host: "norm", label: "norm", agent: "codex", cwd: "/home/dev/worktrees/lasso/chat-mode")
    p.prompt = "Fix the flaky hover test"
    let all = { (s: String) in ListModel.matches(p, terms: ListModel.terms(s)) }
    #expect(all(""))
    #expect(all("norm codex"))
    #expect(all("CODEX  Norm"))
    #expect(all("chat-mode"))
    #expect(all("flaky"))
    #expect(all("agent1"))
    #expect(!all("norm claude"))
}

/// The pins, the groups and the keyboard order all follow the filter, and
/// the first match is lit for ⏎.
@MainActor @Test func filterNarrowsEverySectionAndLightsTheFirstMatch() {
    let fleet = FleetModel()
    let ps = [pane(1, agent: "claude"), pane(2, agent: "codex"), pane(3, agent: "codex")]
    fleet.apply(panes: AllPanes(panes: ps))
    var ui = UIStateSlice()
    ui.pinned_agents = [ps[0].key, ps[1].key]
    fleet.apply(uiState: ui)
    let m = ListModel(fleet: fleet, actions: FixtureFleetActions())
    #expect(m.pinned.count == 2 && m.shown.count == 3)
    m.filter = "codex"
    #expect(m.pinned.map(\.key) == [ps[1].key])
    #expect(m.groups.flatMap(\.agents).map(\.key) == [ps[1].key, ps[2].key])
    #expect(m.selected == ps[1].key && m.keyboardLit)
    m.selected = ps[2].key
    m.filter = "codex agent"
    #expect(m.selected == ps[2].key)
    m.filter = "nothing like it"
    #expect(m.shown.isEmpty && m.order.isEmpty)
}

/// A typed letter goes into the field from anywhere in the column; esc
/// clears the filter before it closes the column, and a closed column
/// opens unfiltered.
@MainActor @Test func typingFiltersAndEscClearsFirst() async throws {
    let fleet = FleetModel()
    fleet.apply(panes: AllPanes(panes: [pane(1), pane(2, agent: "codex")]))
    let c = ListController(model: ListModel(fleet: fleet, actions: FixtureFleetActions()))
    c.hover.timerEnabled = false
    c.open(takeKey: true)
    await settle { c.model.filterField?.window != nil }
    let field = try #require(c.model.filterField)
    #expect(c.panel.onKey?(key("c", code: 8)) == false)
    #expect(c.panel.firstResponder === field.currentEditor())
    // ⌘-keys stay shortcuts.
    c.panel.makeFirstResponder(nil)
    _ = c.panel.onKey?(key("n", code: 45, mods: .command))
    #expect(c.panel.firstResponder !== field.currentEditor())
    c.model.filter = "codex"
    #expect(c.panel.onKey?(key("\u{1b}", code: KeyCode.escape)) == true)
    #expect(c.model.filter == "" && c.isOpen)
    c.model.filter = "codex"
    c.close()
    try await Task.sleep(nanoseconds: 400_000_000)
    #expect(c.model.filter == "")
}
