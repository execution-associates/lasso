import AppKit
import Foundation
import Testing

@testable import LassoDesktopKit

/// Terminal mode's body is the web terminal form's: the host's terminal
/// workspace by id when live (else by name), a bare shell's tab numbered
/// after its last, a command's left for lasso to name, the directory only
/// when given, and focus on.
@MainActor @Test func aTerminalIsCreatedAsTheWebCreatesIt() async {
    let source = FixtureNewAgentSource()
    let m = NewAgentModel(kind: .terminal, source: source)
    await m.load()
    let live = try! await source.workspaces(host: "local")
    var req = m.terminalRequest(workspaces: live)
    #expect(req == CreateTerminal(host: "local", command: "", cwd: nil, workspace_id: "w3", workspace_name: "Scratch", tab_name: "4", focus: true))
    #expect(m.canCreate)
    m.prompt = "git status\nbun test"
    m.cwd = "  ~/projects/exa/lasso "
    m.host = "minime"
    req = m.terminalRequest(workspaces: [])
    #expect(req == CreateTerminal(
        host: "minime", command: "git status\nbun test", cwd: "~/projects/exa/lasso", workspace_id: nil, workspace_name: "Scratch",
        tab_name: nil, focus: true))
    let json = String(decoding: try! JSONEncoder().encode(req), as: UTF8.self)
    #expect(json.contains("\"focus\":true") && !json.contains("tab_name"))
    m.prompt = String(repeating: "x", count: 513)
    #expect(!m.canCreate)
}

/// Run creates it, closes nothing itself, and hands the coordinator the new
/// pane on its host (with what did not take); a failure stays in the form.
@MainActor @Test func runShowsTheNewTerminalOrSaysWhyNot() async throws {
    let source = FixtureNewAgentSource()
    let m = NewAgentModel(kind: .terminal, source: source)
    await m.load()
    m.prompt = "htop"
    var shown: (HostPane, String?)?
    m.onTerminal = { shown = ($0, $1) }
    m.create()
    try await Task.sleep(nanoseconds: 100_000_000)
    #expect(source.terminals.count == 1 && source.terminals[0].command == "htop")
    let (pane, note) = try #require(shown)
    #expect(pane.host == "local" && pane.pane_id == "w3:p7" && pane.workspace_id == "w3" && note == nil)

    shown = nil
    source.terminalAnswer = .success(CreatedTerminal(workspace_id: "w3", root_pane: "w3:p8", command_error: "pane closed"))
    m.create()
    try await Task.sleep(nanoseconds: 100_000_000)
    #expect(shown?.1 == "The command was not typed in: pane closed")

    shown = nil
    source.terminalAnswer = .failure(.http(400, "cwd: no such directory"))
    m.create()
    try await Task.sleep(nanoseconds: 100_000_000)
    #expect(shown == nil && m.error?.contains("no such directory") == true && !m.busy)
}

/// The New shortcuts: ⌥⇧O an agent, ⌃⇧O a scratch one, ⌥⇧I a terminal (the
/// web's ⌘O and ⌘I); each is distinct, and none is a ⌘ combination an app
/// would already use.
@Test func theNewShortcutsAreDistinct() {
    let combos = ShortcutSlot.allCases.compactMap { slot -> (UInt16, UInt)? in
        if case .combo(let k, let m, _) = slot.fallback { return (k, m) }
        return nil
    }
    #expect(combos.count == ShortcutSlot.allCases.count)
    #expect(Set(combos.map { "\($0.0):\($0.1)" }).count == combos.count)
    #expect(combos.allSatisfy { !NSEvent.ModifierFlags(rawValue: $0.1).contains(.command) })
    #expect(ShortcutSlot.newTerminal.fallback == .combo(
        keyCode: 34, modifiers: NSEvent.ModifierFlags([.option, .shift]).rawValue, display: "⌥⇧I"))
    #expect(ShortcutSlot.newTerminal.title == "New terminal" && !ShortcutSlot.newTerminal.allowsDoubleTap)
}
