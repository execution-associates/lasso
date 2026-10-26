import AppKit
import Foundation
import Testing
import WebKit

@testable import LassoDesktopKit

// The herdr window and the Browser window as agent windows' panels (PLAN 19,
// 22, 24): they take the keyboard without activating the app (this test
// process never is the active app, as Lasso is not while another app is in
// front), their page takes typing, ⌘C ⌘V ⌘W and the terminal's text size
// keys reach them, and they collapse, resize and remember their frame as an
// agent's window does. Spaces are SpacesTests'.

/// A page with one text field that has the keyboard, standing in for xterm's
/// and for the browser's live view.
private let fieldPage = #"""
<html><body style="margin:0"><textarea id="t" style="width:100vw;height:100vh;font:16px monospace"></textarea><script>
document.getElementById("t").focus();
if (window.webkit.messageHandlers.lassoPage) webkit.messageHandlers.lassoPage.postMessage({ op: "ready" });
</script></body></html>
"""#

/// lasso's HTTP for both windows: the field page as the terminal's document
/// and as the Browser window's.
final class FieldPageStub: URLProtocol, @unchecked Sendable {
    override class func canInit(with request: URLRequest) -> Bool { request.url?.scheme == "http" }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let url = request.url!
        let page = url.path == "/desktop-browser.html" || (url.path.hasPrefix("/terminal/") && !url.path.hasSuffix("/token"))
        let body = page ? fieldPage : url.path.hasSuffix("/token") ? #"{"token":""}"# : #"{"available":true,"profiles":[]}"#
        let resp = HTTPURLResponse(
            url: url, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": page ? "text/html" : "application/json"])!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

@MainActor private final class FieldTermSource: HerdrTerminalSource {
    func attach(_ host: String) async throws -> String { "local" }
    func documentQuery(_ slug: String, palette: String) async throws -> String { "" }
    func syncPalette(_ palette: String) async throws {}
    func focus(_ pane: HostPane) async throws -> Bool { false }
    func claim(_ host: String, client: String) async throws -> String { client }
}

@MainActor private func fieldClient() -> LassoClient {
    LassoClient(baseURL: URL(string: "http://127.0.0.1:9")!, protocolClasses: [FieldPageStub.self])
}

/// A key's press and release as the window server hands them to the app.
@MainActor private func press(_ w: NSWindow, _ chars: String, _ code: UInt16, _ flags: NSEvent.ModifierFlags = []) {
    for t in [NSEvent.EventType.keyDown, .keyUp] {
        let e = NSEvent.keyEvent(
            with: t, location: .zero, modifierFlags: flags, timestamp: ProcessInfo.processInfo.systemUptime,
            windowNumber: w.windowNumber, context: nil, characters: chars, charactersIgnoringModifiers: chars, isARepeat: false,
            keyCode: code)!
        NSApp.sendEvent(e)
    }
}

@MainActor private func field(_ web: WKWebView) async -> String {
    ((try? await web.evaluateJavaScript("document.getElementById('t').value")) as? String) ?? "?"
}

/// Whether the page's field has the keyboard, as the page sees it.
@MainActor private func focused(_ web: WKWebView) async -> Bool {
    ((try? await web.evaluateJavaScript("document.hasFocus() && document.activeElement && document.activeElement.id === 't'"))
        as? Bool) == true
}

/// The app's main menu (the Edit menu's ⌘C ⌘V), for as long as `body` runs.
@MainActor private func withAppMenu(_ body: () async throws -> Void) async rethrows {
    let saved = NSApp.mainMenu
    NSApp.mainMenu = AppPresence(fleet: FleetModel()).mainMenu()
    defer { NSApp.mainMenu = saved }
    try await body()
}

/// The clipboard as it was, put back after `body` (this is a real Mac's).
@MainActor private func keepingClipboard(_ body: () async throws -> Void) async rethrows {
    let pb = NSPasteboard.general
    let saved = pb.string(forType: .string)
    defer {
        pb.clearContents()
        if let saved { pb.setString(saved, forType: .string) }
    }
    try await body()
}

/// Opened, the Browser window has the keyboard and its page the field:
/// typing lands there, ⌘V pastes and ⌘C copies through the app's Edit menu,
/// a click gives the keyboard back after it went elsewhere, and ⌘W closes it.
@MainActor @Test func theBrowserWindowTakesTheKeyboard() async throws {
    let c = BrowserWindowController(source: FixtureBrowserSource(BrowserStatusPayload(profiles: [])), client: fieldClient())
    c.open()
    defer { c.close() }
    let w = c.hostWindow
    let web = try #require(c.pageView)
    await until(10) { c.model.phase == .live }
    #expect(w.isKeyWindow && w.firstResponder === web, "opened with the keyboard in its page")
    await until(3) { await focused(web) }
    #expect(await focused(web), "the page's field has it")

    try await withAppMenu {
        try await keepingClipboard {
            press(w, "h", 4)
            press(w, "i", 34)
            await until(3) { await field(web) == "hi" }
            #expect(await field(web) == "hi")
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setString(" there", forType: .string)
            press(w, "v", 9, .command)
            await until(3) { await field(web) == "hi there" }
            #expect(await field(web) == "hi there", "⌘V")
            NSPasteboard.general.clearContents()
            press(w, "a", 0, .command)
            press(w, "c", 8, .command)
            await until(3) { NSPasteboard.general.string(forType: .string) == "hi there" }
            #expect(NSPasteboard.general.string(forType: .string) == "hi there", "⌘C")

            // The keyboard went elsewhere; a click on the page brings it back.
            w.resignKey()
            #expect(!w.isKeyWindow)
            let at = web.convert(NSPoint(x: web.bounds.midX, y: web.bounds.midY), to: nil)
            for t in [NSEvent.EventType.leftMouseDown, .leftMouseUp] {
                w.sendEvent(
                    NSEvent.mouseEvent(
                        with: t, location: at, modifierFlags: [], timestamp: ProcessInfo.processInfo.systemUptime,
                        windowNumber: w.windowNumber, context: nil, eventNumber: 0, clickCount: 1, pressure: 1)!)
            }
            #expect(w.isKeyWindow && w.firstResponder === web, "a click gives it the keyboard")
            press(w, "!", 18, .shift)
            await until(3) { (await field(web)).hasSuffix("!") }
            #expect((await field(web)).hasSuffix("!"))
        }
    }

    press(w, "w", 13, .command)
    #expect(!c.isOpen && !w.isVisible, "⌘W")
    #expect(c.model.phase == .closed)
}

/// Opened, the herdr window has the keyboard: typing reaches the terminal's
/// page, ⌘= ⌘- ⌘0 size its text (and never reach the page), and ⌘W closes
/// it and its herdr client.
@MainActor @Test func theHerdrWindowTakesTheKeyboard() async throws {
    let saved = UserDefaults.standard.object(forKey: TerminalZoom.defaultsKey)
    UserDefaults.standard.removeObject(forKey: TerminalZoom.defaultsKey)
    defer { UserDefaults.standard.set(saved, forKey: TerminalZoom.defaultsKey) }
    let c = HerdrTerminalController(source: FieldTermSource(), client: fieldClient(), fleet: FleetModel())
    c.open()
    defer { c.close() }
    let w = c.hostWindow
    let web = try #require(c.pageView)
    await until(10) { c.model.phase == .live }
    #expect(w.isKeyWindow && w.firstResponder === web, "opened with the keyboard in the terminal")
    await until(3) { await focused(web) }
    #expect(await focused(web))

    press(w, "l", 37)
    press(w, "s", 1)
    await until(3) { await field(web) == "ls" }
    #expect(await field(web) == "ls")

    let start = TerminalZoom.stored()
    press(w, "=", 24, .command)
    #expect(TerminalZoom.stored() == start + 1, "⌘=")
    press(w, "-", 27, .command)
    press(w, "-", 27, .command)
    #expect(TerminalZoom.stored() == start - 1, "⌘-")
    press(w, "0", 29, .command)
    #expect(TerminalZoom.stored() == start, "⌘0")
    #expect(await field(web) == "ls", "the size keys never reach the page")

    press(w, "w", 13, .command)
    #expect(!c.isOpen && !w.isVisible, "⌘W")
    #expect(c.model.phase == .closed)
}

/// Minimize collapses the window to its header and hides its page, top edge
/// fixed; expand puts it back. The corner's double-click puts the default
/// size back and forgets the frame, so the next open is a fresh window's;
/// one moved and closed opens where it was.
@MainActor @Test func aPageWindowCollapsesResizesAndRemembers() async throws {
    let keys = [PageWindow.frameKey(HerdrTerminalController.frameName), "NSWindow Frame " + HerdrTerminalController.frameName]
    let saved = keys.map { UserDefaults.standard.object(forKey: $0) }
    defer { for (k, v) in zip(keys, saved) { UserDefaults.standard.set(v, forKey: k) } }
    for k in keys { UserDefaults.standard.removeObject(forKey: k) }
    let t = Theme.Terminal.window

    let c = HerdrTerminalController(source: FieldTermSource(), client: fieldClient(), fleet: FleetModel())
    c.open()
    let pw = try #require(c.pageWindow)
    let vis = Screens.visibleFrame(for: pw.panel.frame)
    let placed = NSRect(x: vis.minX + 40, y: vis.maxY - 40 - 500, width: 700, height: 500)
    pw.panel.setFrame(placed, display: true)

    pw.toggle()
    #expect(pw.collapsed && pw.panel.frame.height == t.number("header") && pw.panel.frame.maxY == placed.maxY)
    #expect(c.pageView?.isHiddenOrHasHiddenAncestor == true, "collapsed, the page is hidden")
    pw.toggle()
    #expect(!pw.collapsed && pw.panel.frame == placed && c.pageView?.isHiddenOrHasHiddenAncestor == false)

    // Closed where it was put, it opens there again.
    c.close()
    #expect(PageWindow.storedFrame(HerdrTerminalController.frameName) == placed)
    let again = HerdrTerminalController(source: FieldTermSource(), client: fieldClient(), fleet: FleetModel())
    again.open()
    #expect(again.hostWindow.frame == placed)

    // The corner's double-click: the default size, top-left kept, forgotten.
    let pw2 = try #require(again.pageWindow)
    pw2.defaultSize(animate: false)
    #expect(pw2.panel.frame.size == t.size2("size"))
    #expect(pw2.panel.frame.minX == placed.minX && pw2.panel.frame.maxY == placed.maxY)
    #expect(PageWindow.storedFrame(HerdrTerminalController.frameName) == nil)
    again.close()
    #expect(PageWindow.storedFrame(HerdrTerminalController.frameName) == nil, "a close remembers nothing")
}

/// Collapsed to its header, the window's Close takes a real click all the
/// way to its trailing edge: the resize corner, hidden while collapsed, sits
/// over that edge of the button and must not take it.
@MainActor @Test func aCollapsedPageWindowClosesOnARealClick() async throws {
    guard canPostClicks() else { return }
    let keys = [PageWindow.frameKey(HerdrTerminalController.frameName), "NSWindow Frame " + HerdrTerminalController.frameName]
    let saved = keys.map { UserDefaults.standard.object(forKey: $0) }
    defer { for (k, v) in zip(keys, saved) { UserDefaults.standard.set(v, forKey: k) } }
    let c = HerdrTerminalController(source: FieldTermSource(), client: fieldClient(), fleet: FleetModel())
    c.open()
    defer { c.close() }
    let pw = try #require(c.pageWindow)
    pw.panel.setFrame(NSRect(x: 600, y: 450, width: 700, height: 500), display: true)
    pw.toggle()
    #expect(pw.collapsed)
    try? await Task.sleep(nanoseconds: 300_000_000)

    let root = try #require(pw.panel.contentView)
    let header = Theme.Terminal.window.number("header")
    let button = Theme.Agent.window.number("button")
    // Close's trailing edge, low in the header: inside the button, and
    // inside where the corner is (the window's bottom-right `button` square).
    let x = root.bounds.width - Theme.Panel.inset.pt - 2
    let y = (header - button) / 2 + 2
    try #require(x > root.bounds.width - button && y < button)
    await realClick(root, at: NSPoint(x: x, y: root.bounds.height - y))
    await until(2) { !c.isOpen }
    #expect(!c.isOpen, "the click went to \(hitView(root, at: NSPoint(x: x, y: root.bounds.height - y)).map { String(describing: type(of: $0)) } ?? "nothing")")
}

/// A file window an agent opened does not take the keyboard; a click on it
/// does, without activating the app, and typing then reaches its page.
/// ⌘W with unsaved edits asks first (in the window), and the second closes.
/// (⌘S, ⌘Z and ⌘F against lasso's real editor are FilePageTests'.)
@MainActor @Test func aFileWindowTakesTheKeyboardOnlyWhenClicked() async throws {
    let source = FakeFileSource()
    source.put("/r/notes.txt")
    let client = LassoClient(baseURL: URL(string: "http://127.0.0.1:9")!, protocolClasses: [FilePageStub.self])
    let c = FileWindowController(
        open: FileOpen(path: "/r/notes.txt", from: "olivia"), kind: .text, stat: source.files["/r/notes.txt"], hostLabel: "titan",
        client: client, source: source)
    defer { c.close() }
    c.show()
    let w = c.panel
    let web = try #require(c.pageView)
    await until(10) { c.model.page.loaded }
    #expect(w.isVisible && !w.isKeyWindow, "an agent's file never takes the keyboard")

    let at = web.convert(NSPoint(x: web.bounds.midX, y: web.bounds.midY), to: nil)
    for t in [NSEvent.EventType.leftMouseDown, .leftMouseUp] {
        w.sendEvent(
            NSEvent.mouseEvent(
                with: t, location: at, modifierFlags: [], timestamp: ProcessInfo.processInfo.systemUptime,
                windowNumber: w.windowNumber, context: nil, eventNumber: 0, clickCount: 1, pressure: 1)!)
    }
    #expect(w.isKeyWindow && w.firstResponder === web, "a click gives it the keyboard")
    await until(3) { await focused(web) }
    press(w, "h", 4)
    press(w, "i", 34)
    await until(3) { c.model.dirty }
    #expect(await field(web) == "hi" && c.model.dirty)

    press(w, "w", 13, .command)
    #expect(c.isOpen && c.model.banner == .confirmClose, "⌘W with edits asks first")
    press(w, "w", 13, .command)
    #expect(!c.isOpen && !w.isVisible, "the second closes")
}
