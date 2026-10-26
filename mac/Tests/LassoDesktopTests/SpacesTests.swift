import AppKit
import Foundation
import Testing

@testable import LassoDesktopKit

// The rail's pop-outs and the app's windows show on the Space the user is
// on, another app's full screen included (PLAN 16, "Spaces"). macOS pins
// every Lasso window that is off screen when a full-screen Space of the
// app's own goes to one ordinary Space, `.canJoinAllSpaces` or not, and
// setting the behaviour again does not undo it. Such a panel is replaced.
// No Lasso window goes full screen (the herdr and Browser windows are agent
// windows' panels), so the tests stand one in.

@MainActor private func coordinator() -> AppCoordinator {
    let fleet = FleetModel()
    let p = HostPane(
        host: "local", host_label: "titan", pane_id: "w1:p1", workspace_id: "w1", workspace_label: "Agents",
        tab_id: "w1:t1", tab_label: "stub", cwd: "/", agent: "claude", agent_status: "blocked", has_agent: true, focused: false)
    fleet.apply(panes: AllPanes(panes: [p]))
    fleet.apply(bots: BotsPayload(bots: []))
    return AppCoordinator(fleet: fleet, sync: nil, fixture: nil)
}

/// The herdr window and the Browser window on a lasso that answers nothing
/// (their pages never load: these tests are about their windows).
@MainActor private func pageWindows() -> (HerdrTerminalController, BrowserWindowController, FileWindowController) {
    let client = LassoClient(baseURL: URL(string: "http://127.0.0.1:9")!, protocolClasses: [RightClickStub.self])
    let source = FakeFileSource()
    source.put("/r/notes.md")
    return (
        HerdrTerminalController(source: SpacesTermSource(), client: client, fleet: FleetModel()),
        BrowserWindowController(source: FixtureBrowserSource(nil), client: client),
        FileWindowController(
            open: FileOpen(path: "/r/notes.md", from: "olivia"), kind: .markdown, stat: source.files["/r/notes.md"], hostLabel: "titan",
            client: client, source: source)
    )
}

@MainActor private final class SpacesTermSource: HerdrTerminalSource {
    func attach(_ host: String) async throws -> String { "local" }
    func documentQuery(_ slug: String, palette: String) async throws -> String { "" }
    func syncPalette(_ palette: String) async throws {}
    func focus(_ pane: HostPane) async throws -> Bool { false }
    func claim(_ host: String, client: String) async throws -> String { client }
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
    let (term, browser, file) = pageWindows()
    check(term.hostWindow, "the herdr window", level: .floating)
    check(browser.hostWindow, "the Browser window", level: .floating)
    check(file.panel, "a file window", level: .floating)
    file.close()
}

/// The herdr window and the Browser window are agent windows' panels: the
/// same class, style, level and Spaces behaviour, the keyboard without the
/// app's activation, and no full screen (a panel on every Space cannot have
/// one): asked to go full screen, they stay as they are.
@MainActor @Test func theHerdrAndBrowserWindowsAreAgentWindows() async {
    let app = coordinator()
    let p = HostPane(
        host: "local", host_label: "titan", pane_id: "w1:p9", workspace_id: "w1", workspace_label: "Agents",
        tab_id: "w1:t9", tab_label: "spaces", cwd: "/", agent: "claude", agent_status: "idle", has_agent: true, focused: false)
    let agent = AgentWindowController(model: app.cardModel(p), near: app.rail.panel.frame, media: nil).panel
    let (term, browser, file) = pageWindows()
    defer {
        term.close()
        browser.close()
        file.close()
    }
    // A file window is the same panel, and an agent's file opens without the keyboard.
    file.show()
    for w in [file.panel] {
        #expect(w.styleMask == agent.styleMask && w.collectionBehavior == agent.collectionBehavior, "a file window")
        #expect(w.level == agent.level && w.hidesOnDeactivate == agent.hidesOnDeactivate, "a file window")
        #expect(w.collectionBehavior.isDisjoint(with: [.fullScreenPrimary, .fullScreenAllowsTiling, .moveToActiveSpace]))
        #expect(w.isVisible && !w.isKeyWindow, "a file window opens without the keyboard")
        w.toggleFullScreen(nil)
    }
    for (c, name) in [(term as AnyObject, "the herdr window"), (browser as AnyObject, "the Browser window")] {
        let w: NSWindow
        if let t = c as? HerdrTerminalController {
            t.open()
            w = t.hostWindow
        } else {
            let b = c as! BrowserWindowController
            b.open()
            w = b.hostWindow
        }
        #expect(w is AgentWindowPanel, "\(name)")
        #expect(w.styleMask == agent.styleMask, "\(name)")
        #expect(w.collectionBehavior == agent.collectionBehavior, "\(name)")
        #expect(w.level == agent.level && w.hasShadow == agent.hasShadow, "\(name)")
        #expect(w.hidesOnDeactivate == agent.hidesOnDeactivate, "\(name)")
        #expect(w.canBecomeKey && !w.canBecomeMain, "\(name)")
        #expect(!w.styleMask.contains(.titled) && !w.styleMask.contains(.resizable), "\(name)")
        #expect(w.collectionBehavior.isDisjoint(with: [.fullScreenPrimary, .fullScreenAllowsTiling, .moveToActiveSpace]), "\(name)")
        #expect(w.isVisible && w.isKeyWindow, "\(name) opens with the keyboard")
        w.toggleFullScreen(nil)
    }
    await wait(1.5)
    #expect(!term.hostWindow.styleMask.contains(.fullScreen) && !browser.hostWindow.styleMask.contains(.fullScreen))
    #expect(!file.panel.styleMask.contains(.fullScreen))
    #expect(term.hostWindow.isOnActiveSpace && browser.hostWindow.isOnActiveSpace && file.panel.isOnActiveSpace)
}

/// The user's report: the herdr and Browser windows opened in front of a
/// maximized browser, but did not come along to another desktop. Opened,
/// then another app going full screen (a Space of its own): both are still
/// shown, on the Space the user is now on, and a click there gives the
/// window the keyboard with that app in front. Opened there, they open
/// there, with the keyboard. Needs a window server that does full screen
/// (minime's Aqua session); where full screen cannot be reached it checks
/// nothing.
@MainActor @Test func theHerdrAndBrowserWindowsFollowTheUserAcrossSpaces() async throws {
    let policy = NSApp.activationPolicy()
    NSApp.setActivationPolicy(.accessory)
    defer { NSApp.setActivationPolicy(policy) }
    let (term, browser, file) = pageWindows()
    term.open()
    browser.open()
    file.show()
    defer {
        term.close()
        browser.close()
        file.close()
    }
    guard let other = FullScreenApp() else { return }
    guard await other.fullScreen(within: 10) else {
        await other.quit()
        return
    }
    await wait(5) { NSWorkspace.shared.frontmostApplication?.processIdentifier == other.process.processIdentifier }
    await wait(1.5)
    for (c, name) in [(term.hostWindow, "the herdr window"), (browser.hostWindow, "the Browser window"), (file.panel, "a file window")] {
        #expect(c.isVisible && c.isOnActiveSpace, "\(name) came along")
    }

    // A click in the terminal, that app in front.
    let w = term.hostWindow
    let web = try #require(term.pageView)
    let at = web.convert(NSPoint(x: web.bounds.midX, y: web.bounds.midY), to: nil)
    for t in [NSEvent.EventType.leftMouseDown, .leftMouseUp] {
        let e = NSEvent.mouseEvent(
            with: t, location: at, modifierFlags: [], timestamp: ProcessInfo.processInfo.systemUptime,
            windowNumber: w.windowNumber, context: nil, eventNumber: 0, clickCount: 1, pressure: 1)
        w.sendEvent(try #require(e))
    }
    #expect(w.isKeyWindow && w.firstResponder === web, "the click gives the herdr window the keyboard")

    // Opened on that Space.
    browser.close()
    browser.open()
    #expect(browser.hostWindow.isOnActiveSpace && browser.hostWindow.isKeyWindow, "the Browser window opens there")
    #expect(browser.hostWindow.firstResponder === browser.pageView)
    await other.quit()
}

/// A panel macOS has pinned to another Space is replaced when it is shown:
/// a new panel takes its content, settings, frame and handlers, the old one
/// goes, and the controller holds the new one from then on (its keyboard
/// notices and the column's hover included). One on the active Space is
/// shown as it is.
@MainActor @Test func aPanelPinnedToAnotherSpaceIsReplacedWhenShown() async throws {
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
    // Once laid out: a first open lays the column out on the next turn.
    let middle = NSPoint(x: app.list.panel.frame.midX, y: app.list.panel.frame.midY)
    await settle { app.list.hover.contentPoint(middle) != nil }
    #expect(app.list.hover.contentPoint(middle) != nil)
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

    // The herdr window and the Browser window: their page and header move
    // over, the keyboard is the page's, and ⌘W still closes them.
    let (term, browser, file) = pageWindows()
    // A file window, pinned, is replaced as it is shown, without the keyboard.
    let oldFile = file.panel
    let fileContent = oldFile.contentView
    pinned.insert(ObjectIdentifier(oldFile))
    file.show()
    expectReplaced(oldFile, by: file.panel, content: fileContent, keys: false, "a file window")
    #expect(file.panel.level == .floating && !file.panel.isKeyWindow)
    file.panel.onCloseKey()
    #expect(!file.isOpen, "a file window: ⌘W")
    for (name, panel, open, page, isOpen) in [
        ("the herdr window", { term.hostWindow }, { term.open() }, { term.pageView as NSView? }, { term.isOpen }),
        ("the Browser window", { browser.hostWindow }, { browser.open() }, { browser.pageView as NSView? }, { browser.isOpen }),
    ] as [(String, () -> NSWindow, () -> Void, () -> NSView?, () -> Bool)] {
        let old = try #require(panel() as? RailPanel)
        let content = old.contentView
        pinned.insert(ObjectIdentifier(old))
        open()
        let new = try #require(panel() as? AgentWindowPanel)
        expectReplaced(old, by: new, content: content, keys: false, name)
        #expect(new.level == .floating && new.isKeyWindow, "\(name)")
        #expect(new.firstResponder === page() && page() != nil, "\(name): the page has the keyboard")
        new.onCloseKey()
        #expect(!isOpen() && !new.isVisible, "\(name): ⌘W")
    }
}
