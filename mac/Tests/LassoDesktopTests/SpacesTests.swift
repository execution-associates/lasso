import AppKit
import Foundation
import Testing

@testable import LassoDesktopKit

// The rail's pop-outs show on the Space the user is on, another app's full
// screen included, after a Lasso window has been full screen (PLAN 16,
// "Spaces"). macOS pins every Lasso window that is off screen when Lasso's
// own full-screen Space goes (the herdr window leaving full screen, or
// closing in it) to one ordinary Space, `.canJoinAllSpaces` or not, and
// setting the behaviour again does not undo it. Such a panel is replaced.

@MainActor private func coordinator() -> AppCoordinator {
    let fleet = FleetModel()
    let p = HostPane(
        host: "local", host_label: "titan", pane_id: "w1:p1", workspace_id: "w1", workspace_label: "Agents",
        tab_id: "w1:t1", tab_label: "stub", cwd: "/", agent: "claude", agent_status: "blocked", has_agent: true, focused: false)
    fleet.apply(panes: AllPanes(panes: [p]))
    fleet.apply(bots: BotsPayload(bots: []))
    return AppCoordinator(fleet: fleet, sync: nil, fixture: nil)
}

/// A page an agent opened for the human: the browser banner's offer.
private let pageOffer = BrowserOpen(profile: "default", tab_id: "T1", url: "https://example.com/", from: "olivia")

/// Run the main run loop for `seconds`, or until `done`.
@MainActor private func wait(_ seconds: Double, until done: () -> Bool = { false }) async {
    let end = Date().addingTimeInterval(seconds)
    while !done(), Date() < end { try? await Task.sleep(nanoseconds: 50_000_000) }
}

/// Another app with a window in full screen: the browser the user had
/// maximized. A tiny AppKit program, compiled with the toolchain's swiftc
/// into a temporary folder; it says `fullscreen` once there, and leaves full
/// screen and quits when its stdin closes.
@MainActor private final class FullScreenApp {
    static let source = """
        import AppKit
        let app = NSApplication.shared
        app.setActivationPolicy(.regular)
        let w = NSWindow(contentRect: NSRect(x: 100, y: 100, width: 640, height: 480),
                         styleMask: [.titled, .closable, .resizable], backing: .buffered, defer: false)
        w.collectionBehavior = [.fullScreenPrimary]
        NotificationCenter.default.addObserver(forName: NSWindow.didEnterFullScreenNotification, object: w, queue: .main) { _ in
            FileHandle.standardOutput.write(Data("fullscreen\\n".utf8))
        }
        DispatchQueue.main.async {
            app.activate(ignoringOtherApps: true)
            w.makeKeyAndOrderFront(nil)
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.5) { w.toggleFullScreen(nil) }
        }
        DispatchQueue.global().async {
            _ = FileHandle.standardInput.readDataToEndOfFile()
            DispatchQueue.main.async {
                if w.styleMask.contains(.fullScreen) { w.toggleFullScreen(nil) }
                DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) { exit(0) }
            }
        }
        DispatchQueue.main.asyncAfter(deadline: .now() + 60) { exit(0) }
        app.run()
        """

    let process = Process()
    private let stdin = Pipe()
    private let stdout = Pipe()
    private let dir: URL
    /// What it has said on stdout, read off the pipe's own queue.
    private final class Said: @unchecked Sendable {
        let lock = NSLock()
        var text = ""
        func append(_ d: Data) { lock.withLock { text += String(decoding: d, as: UTF8.self) } }
        var value: String { lock.withLock { text } }
    }
    private let said = Said()

    /// Nil when it cannot be built or run here.
    init?() {
        dir = FileManager.default.temporaryDirectory.appendingPathComponent("lasso-spaces-\(UUID().uuidString)")
        do {
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            let src = dir.appendingPathComponent("main.swift")
            try Self.source.write(to: src, atomically: true, encoding: .utf8)
            let swiftc = Process()
            swiftc.executableURL = URL(fileURLWithPath: "/usr/bin/swiftc")
            swiftc.arguments = [src.path, "-o", dir.appendingPathComponent("FullScreenApp").path]
            swiftc.standardOutput = FileHandle.nullDevice
            swiftc.standardError = FileHandle.nullDevice
            try swiftc.run()
            swiftc.waitUntilExit()
            guard swiftc.terminationStatus == 0 else {
                try? FileManager.default.removeItem(at: dir)
                return nil
            }
            process.executableURL = dir.appendingPathComponent("FullScreenApp")
            process.standardInput = stdin
            process.standardOutput = stdout
            process.standardError = FileHandle.nullDevice
            let said = said
            stdout.fileHandleForReading.readabilityHandler = { h in said.append(h.availableData) }
            try process.run()
        } catch {
            try? FileManager.default.removeItem(at: dir)
            return nil
        }
    }

    var app: NSRunningApplication? { NSRunningApplication(processIdentifier: process.processIdentifier) }

    /// Whether it reached full screen within `seconds`.
    func fullScreen(within seconds: Double) async -> Bool {
        await wait(seconds) { self.said.value.contains("fullscreen") || !self.process.isRunning }
        return said.value.contains("fullscreen")
    }

    /// Out of full screen and gone, and its folder with it.
    func quit() async {
        stdout.fileHandleForReading.readabilityHandler = nil
        try? stdin.fileHandleForWriting.close()
        await wait(4) { !self.process.isRunning }
        if process.isRunning { process.terminate() }
        try? FileManager.default.removeItem(at: dir)
    }
}

/// The user's report: the browser full screen, the herdr window made full
/// screen and closed, then back in the browser, nothing the rail opened was
/// seen. Each pop-out that had been shown before must now open on the
/// browser's Space. Needs a window server that does full screen (minime's
/// Aqua session); where full screen cannot be reached it checks nothing.
@MainActor @Test func popOutsShowOnAnotherAppsFullScreenAfterOursClosed() async throws {
    let policy = NSApp.activationPolicy()
    // Lasso is an accessory app (no Dock icon) unless the user asks.
    NSApp.setActivationPolicy(.accessory)
    defer { NSApp.setActivationPolicy(policy) }
    guard let browser = FullScreenApp() else { return }
    guard await browser.fullScreen(within: 10) else {
        await browser.quit()
        return
    }
    let app = coordinator()
    app.rail.show()

    // Each pop-out has been out once and put away: ordered out, as they all
    // are while the herdr window is up.
    app.hoverOpen(.agents)
    app.railActivated(.inbox, byPointer: true)
    app.openSettings()
    app.browserOpened(pageOffer)
    await wait(0.3)
    app.closeRailPanels()
    app.closeSettings()
    app.dismissBrowserOffer()
    await wait(1) {
        !app.card.panel.isVisible && !app.list.panel.isVisible && !app.settingsWindow.isVisible && !app.banner.panel.isVisible
    }

    // The herdr window: full screen, then closed in it.
    let herdr = NSWindow(
        contentRect: NSRect(x: 200, y: 200, width: 640, height: 480), styleMask: [.titled, .closable, .resizable],
        backing: .buffered, defer: false)
    herdr.isReleasedWhenClosed = false
    herdr.collectionBehavior = [.fullScreenPrimary]
    NSApp.activate()
    herdr.makeKeyAndOrderFront(nil)
    await wait(0.7)
    herdr.toggleFullScreen(nil)
    await wait(6) { herdr.styleMask.contains(.fullScreen) }
    let wentFullScreen = herdr.styleMask.contains(.fullScreen)
    await wait(0.7)
    herdr.performClose(nil)
    await wait(2.5)

    // Back to the browser's full-screen Space.
    browser.app?.activate()
    await wait(5) { NSWorkspace.shared.frontmostApplication?.processIdentifier == browser.process.processIdentifier }
    await wait(2)

    defer {
        app.closeRailPanels()
        app.closeSettings()
        app.dismissBrowserOffer()
        app.rail.panel.orderOut(nil)
    }
    if wentFullScreen {
        app.hoverOpen(.agents)
        #expect(app.list.panel.isOnActiveSpace, "the agents column")
        app.closeRailPanels()
        app.railActivated(.inbox, byPointer: true)
        #expect(app.card.panel.isOnActiveSpace, "the inbox card")
        app.closeRailPanels()
        app.hoverOpen(.settings)
        #expect(app.settingsWindow.isOnActiveSpace, "settings")
        app.browserOpened(pageOffer)
        #expect(app.banner.panel.isOnActiveSpace, "the browser banner")
        #expect(app.rail.panel.isOnActiveSpace, "the rail")
    }
    await browser.quit()
}

/// Every panel the app shows floats over full-screen apps and is on every
/// Space from its first order-in: the behaviour is set when it is made,
/// before it is ever shown (a later change applies only from the next
/// order-in), and never `.moveToActiveSpace`, which would carry it off.
@MainActor @Test func everyPanelIsOnEverySpaceFromTheStart() {
    let app = coordinator()
    let form = NewAgentController(model: NewAgentModel(kind: .git, source: FixtureNewAgentSource()))
    func check(_ w: NSWindow, _ name: String, level: NSWindow.Level) {
        #expect(!w.isVisible, "\(name) is checked before it is first shown")
        #expect(w.collectionBehavior.isSuperset(of: [.canJoinAllSpaces, .fullScreenAuxiliary]), "\(name)")
        #expect(!w.collectionBehavior.contains(.moveToActiveSpace), "\(name)")
        #expect(w.styleMask.contains(.nonactivatingPanel), "\(name)")
        #expect(!w.hidesOnDeactivate, "\(name)")
        #expect(w.level == level, "\(name)")
    }
    check(app.rail.panel, "the rail", level: .statusBar)
    // The rail's pop-outs: the card, the agents column, the inbox panel, the
    // agents' preview, the Bots column and its preview, and Settings.
    for (i, w) in app.railWindows.dropFirst().enumerated() { check(w, "pop-out \(i)", level: .statusBar) }
    #expect(app.railWindows.count >= 7)
    check(app.settingsWindow, "settings", level: .statusBar)
    check(app.banner.panel, "the browser banner", level: .statusBar)
    // Windows the user arranges float at the floating level.
    check(form.panel, "the New form", level: .floating)
    let p = HostPane(
        host: "local", host_label: "titan", pane_id: "w1:p9", workspace_id: "w1", workspace_label: "Agents",
        tab_id: "w1:t9", tab_label: "spaces", cwd: "/", agent: "claude", agent_status: "idle", has_agent: true, focused: false)
    let w = AgentWindowController(model: app.cardModel(p), near: app.rail.panel.frame, media: nil)
    check(w.panel, "an agent's window", level: .floating)
}

/// A panel macOS has pinned to another Space is replaced when it is shown:
/// a new panel takes its content, settings, frame and handlers, the old one
/// goes, and the controller holds the new one from then on (its keyboard
/// notices and the column's hover included). One on the active Space is
/// shown as it is.
@MainActor @Test func aPanelPinnedToAnotherSpaceIsReplacedWhenShown() async {
    let app = coordinator()
    var pinned: Set<ObjectIdentifier> = []
    let seam = RailPanel.onActiveSpace
    RailPanel.onActiveSpace = { !pinned.contains(ObjectIdentifier($0)) }
    defer {
        RailPanel.onActiveSpace = seam
        app.closeRailPanels()
        app.closeSettings()
        app.dismissBrowserOffer()
    }
    func expectReplaced(_ old: RailPanel, by new: RailPanel, content: NSView?, keys: Bool = true, _ name: String) {
        #expect(new !== old && !old.isVisible && new.isVisible, "\(name)")
        #expect(new.contentView === content && content != nil, "\(name): its content moved over")
        #expect(new.level == old.level && new.collectionBehavior == old.collectionBehavior, "\(name)")
        #expect(new.frame == old.frame && new.hasShadow == old.hasShadow, "\(name)")
        if keys { #expect(new.onKey != nil, "\(name): its keys came along") }
    }

    // The agents column, by hover: the hover maps the pointer through the new panel.
    let list = app.list.panel
    let listContent = list.contentView
    pinned.insert(ObjectIdentifier(list))
    app.hoverOpen(.agents)
    expectReplaced(list, by: app.list.panel, content: listContent, "the agents column")
    #expect(app.list.hover.panel === app.list.panel)
    #expect(app.list.hover.contentPoint(NSPoint(x: app.list.panel.frame.midX, y: app.list.panel.frame.midY)) != nil)
    app.closeRailPanels()
    await settle { !app.list.panel.isVisible }

    // Shown again on the active Space: kept.
    let kept = app.list.panel
    app.hoverOpen(.agents)
    #expect(app.list.panel === kept && kept.isVisible)
    app.closeRailPanels()

    // The inbox card, with the keyboard: its key notices follow the new panel.
    let card = app.card.panel
    let cardContent = card.contentView
    pinned.insert(ObjectIdentifier(card))
    app.railActivated(.inbox, byPointer: true)
    expectReplaced(card, by: app.card.panel, content: cardContent, "the inbox card")
    #expect(app.card.isKey && app.card.model?.keyMode == .full)
    app.card.panel.resignKey()
    #expect(app.card.model?.keyMode == CardModel.KeyMode.none)
    app.closeRailPanels()

    // Settings (a SidePanel).
    let settings = app.settingsWindow as! RailPanel
    let settingsContent = settings.contentView
    pinned.insert(ObjectIdentifier(settings))
    app.openSettings(takeKey: false)
    expectReplaced(settings, by: app.settingsWindow as! RailPanel, content: settingsContent, "settings")
    app.closeSettings()

    // The browser banner (no keys of its own): still never key.
    let banner = app.banner.panel
    let bannerContent = banner.contentView
    pinned.insert(ObjectIdentifier(banner))
    app.browserOpened(pageOffer)
    expectReplaced(banner, by: app.banner.panel, content: bannerContent, keys: false, "the browser banner")
    #expect(!app.banner.panel.canBecomeKey && !app.banner.panel.isKeyWindow)
    app.dismissBrowserOffer()
    #expect(!app.banner.panel.isVisible)

    // An agent's window keeps its kind, its level and ⌘W.
    let p = HostPane(
        host: "local", host_label: "titan", pane_id: "w1:p9", workspace_id: "w1", workspace_label: "Agents",
        tab_id: "w1:t9", tab_label: "spaces", cwd: "/", agent: "claude", agent_status: "idle", has_agent: true, focused: false)
    let w = AgentWindowController(model: app.cardModel(p), near: app.rail.panel.frame, media: nil)
    var closed = 0
    w.onClosed = { closed += 1 }
    let old = w.panel
    pinned.insert(ObjectIdentifier(old))
    w.show()
    expectReplaced(old, by: w.panel, content: w.panel.contentView, "an agent's window")
    #expect(w.panel.level == .floating && w.panel.isKeyWindow)
    w.panel.onCloseKey()
    #expect(closed == 1 && !w.panel.isVisible)
}
