import AppKit
import Foundation
import Testing

@testable import LassoDesktopKit

// The Files column (PLAN 27): what is listed and kept, the column's rows and
// keys, Delete's care with what lasso would remove, the hover preview's
// reading, and a row reopening its file whoever is at the Mac.

private let t0 = Date(timeIntervalSince1970: 1_791_745_200)  // 2026-10-11T19:00:00Z

private func suite() -> UserDefaults {
    let name = "recent-files-tests-" + UUID().uuidString
    let d = UserDefaults(suiteName: name)!
    d.removePersistentDomain(forName: name)
    return d
}

private func tempDir() -> URL {
    let u = FileManager.default.temporaryDirectory.appendingPathComponent("lasso-recent-tests-" + UUID().uuidString, isDirectory: true)
    try? FileManager.default.createDirectory(at: u, withIntermediateDirectories: true)
    return u
}

// MARK: the list

@MainActor @Test func whatAnAgentOpenedIsListedOpenedOrNot() {
    let r = RecentFiles(scope: "a", defaults: suite())
    r.note(FileOpen(path: "/r/a.md", from: "olivia"), skip: nil, at: t0)
    #expect(!r.unseen, "a file that opened lights nothing")
    r.note(FileOpen(path: "/r/b.png", host: "minime", from: "codex"), skip: .away, at: t0.addingTimeInterval(1))
    r.note(FileOpen(path: "/r/c.txt"), skip: .off, at: t0.addingTimeInterval(2))
    for skip: FileWindows.Skip in [.directory, .missing, .unsupported] {
        r.note(FileOpen(path: "/r/\(skip)"), skip: skip, at: t0)
    }
    r.note(FileOpen(path: "/r/web.md", app: "web"), skip: nil, at: t0)
    r.note(FileOpen(path: "/r/dir", dir: true), skip: .away, at: t0)
    #expect(r.items.map(\.name) == ["c.txt", "b.png", "a.md"], "newest first; folders, missing files, web-only opens are not listed")
    #expect(r.items.map(\.opened) == [false, false, true])
    #expect(r.items[1].host == "minime" && r.items[1].from == "codex")
    #expect(r.unseen, "one came while nobody was at this Mac")
    r.markSeen()
    #expect(!r.unseen)
}

@MainActor @Test func aFileOpenedAgainGoesToTheTopAndStaysOpened() {
    let r = RecentFiles(scope: "a", defaults: suite())
    r.note(FileOpen(path: "/r/a.md", from: "olivia"), skip: nil, at: t0)
    r.note(FileOpen(path: "/r/b.md"), skip: nil, at: t0.addingTimeInterval(1))
    r.note(FileOpen(path: "/r/a.md", host: "", from: ""), skip: .away, at: t0.addingTimeInterval(2))
    #expect(r.items.map(\.name) == ["a.md", "b.md"], "one row per file per machine")
    #expect(r.items[0].opened, "once opened it stays opened")
    #expect(r.items[0].from == "olivia", "an event without a name keeps the last one")
    #expect(r.items[0].at == t0.addingTimeInterval(2))
    r.note(FileOpen(path: "/r/a.md", host: "minime"), skip: nil, at: t0.addingTimeInterval(3))
    #expect(r.items.count == 3, "the same path on another machine is another file")
}

@MainActor @Test func theListIsCappedAndKeptPerLasso() {
    let d = suite()
    let r = RecentFiles(scope: "http://titan:8090", defaults: d)
    for i in 0..<(RecentFiles.cap + 5) { r.note(FileOpen(path: "/r/\(i).txt"), skip: nil, at: t0.addingTimeInterval(Double(i))) }
    #expect(r.items.count == RecentFiles.cap)
    #expect(r.items.first?.name == "\(RecentFiles.cap + 4).txt" && r.items.last?.name == "5.txt", "the oldest go")
    r.note(FileOpen(path: "/r/away.txt"), skip: .away, at: t0)

    let again = RecentFiles(scope: "http://titan:8090", defaults: d)
    #expect(again.items == r.items && again.unseen, "kept across launches")
    again.use(scope: "http://other:8090")
    #expect(again.items.isEmpty && !again.unseen, "another lasso has its own list")
    again.note(FileOpen(path: "/r/x.txt"), skip: nil, at: t0)
    again.use(scope: "http://titan:8090")
    #expect(again.items.count == RecentFiles.cap)

    again.remove(again.items[0].id)
    #expect(again.items.count == RecentFiles.cap - 1)
    again.clear()
    #expect(RecentFiles(scope: "http://titan:8090", defaults: d).items.isEmpty)
    #expect(RecentFiles(scope: "http://other:8090", defaults: d).items.map(\.name) == ["x.txt"])

    let fixture = RecentFiles(scope: "p", defaults: d, persist: false)
    fixture.note(FileOpen(path: "/r/y.txt"), skip: nil, at: t0)
    #expect(RecentFiles(scope: "p", defaults: d).items.isEmpty, "a fixture's list is never written")
}

@Test func aHomeFolderIsWrittenTilde() {
    #expect(RecentFile(path: "/home/stephan/projects/lasso/mac/PLAN.md", at: t0, opened: true).folder == "~/projects/lasso/mac")
    #expect(RecentFile(path: "/Users/stephan/Desktop/a.png", at: t0, opened: true).folder == "~/Desktop")
    #expect(RecentFile(path: "/home/stephan/a.txt", at: t0, opened: true).folder == "~")
    #expect(RecentFile(path: "/etc/hosts", at: t0, opened: true).folder == "/etc")
    #expect(RecentFile(path: "/home", at: t0, opened: true).folder == "/")
}

// MARK: the column

@MainActor private func column(_ files: [(String, String, TimeInterval)]) -> FilesListModel {
    let r = RecentFiles(scope: "c", persist: false)
    for (path, from, ago) in files.reversed() {
        r.add(FileOpen(path: path, from: from), opened: true, at: t0.addingTimeInterval(-ago))
    }
    return FilesListModel(recent: r, now: { t0 })
}

@MainActor @Test func rowsAreUnderTodayYesterdayAndEarlier() {
    let m = column([("/r/a.md", "olivia", 60), ("/r/b.png", "codex", 3600), ("/r/c.pdf", "jessica", 86400), ("/r/d.txt", "stub", 4 * 86400)])
    let s = m.sections
    #expect(s.map(\.title).first == "Today")
    #expect(s.flatMap(\.files).map(\.name) == ["a.md", "b.png", "c.pdf", "d.txt"])
    #expect(s.last?.title == "Earlier" && s.last?.files.map(\.name) == ["d.txt"])
}

@MainActor @Test func typingFiltersByNamePathMachineAndAgent() {
    let m = column([("/home/s/lasso/PLAN.md", "reviewer", 60), ("/home/s/q3/report.html", "analyst", 120)])
    m.hostLabel = { $0 == "local" ? "titan" : $0 }
    m.filter = "plan"
    #expect(m.files.map(\.name) == ["PLAN.md"])
    m.filter = "titan analyst"
    #expect(m.files.map(\.name) == ["report.html"], "every word must match somewhere")
    m.filter = "q3 reviewer"
    #expect(m.files.isEmpty && m.sections.isEmpty)
    m.reset()
    #expect(m.files.count == 2, "a column that closes drops its filter")
}

@MainActor @Test func rowsHoldStillWhileTheColumnIsOpen() {
    let m = column([("/r/a.md", "o", 60), ("/r/b.md", "o", 120)])
    m.freezeOrder()
    m.recent.add(FileOpen(path: "/r/new.md"), opened: true, at: t0)
    m.recent.add(FileOpen(path: "/r/b.md"), opened: true, at: t0)
    #expect(m.files.map(\.name) == ["a.md", "b.md", "new.md"], "nothing moves under a resting pointer; a newcomer goes last")
    m.thawOrder()
    #expect(m.files.map(\.name) == ["b.md", "new.md", "a.md"])
}

@MainActor @Test func arrowsMoveAndReturnOpens() {
    let m = column([("/r/a.md", "o", 60), ("/r/b.md", "o", 120), ("/r/c.md", "o", 3 * 86400)])
    var opened: [String] = []
    m.onOpen = { opened.append($0.name) }
    m.openSelected()
    #expect(opened == ["a.md"], "⏎ with nothing picked opens the first")
    m.move(1)
    m.move(1)
    m.move(1)
    #expect(m.selected == m.files[2].id, "across sections, stopping at the end")
    m.openSelected()
    #expect(opened == ["a.md", "c.md"])
}

@MainActor @Test func hideTakesTheRowAndLeavesTheFile() async {
    let m = column([("/r/a.md", "o", 60)])
    var forgot: [String] = []
    var deleted = false
    m.onForget = { forgot.append($0.name) }
    m.delete = { _ in deleted = true }
    m.hide(m.files[0])
    #expect(m.files.isEmpty && forgot == ["a.md"] && !deleted)
}

@MainActor @Test func deleteAsksThenSaysWhatHappened() async throws {
    let m = column([("/r/a.md", "o", 60), ("/r/b.md", "o", 120)])
    var asked: [String] = []
    m.delete = { f in
        asked.append(f.name)
        if f.name == "b.md" { throw RecentFileError.folder }
    }
    let a = m.files[0]
    let b = m.files[1]
    m.askDelete(a)
    #expect(m.confirmDelete == a.id && asked.isEmpty, "nothing goes before the confirm")
    m.cancelDelete()
    #expect(m.confirmDelete == nil)
    m.askDelete(a)
    m.deleteFile(a)
    m.deleteFile(b)
    try await waitUntil { asked.count == 2 && m.notes[b.id]?.isFailure == true }
    #expect(m.files.map(\.name) == ["b.md"], "a refused delete keeps the row")
    #expect(m.notes[b.id] == .failed("Couldn't delete: it is a folder now; delete it yourself"))
}

@MainActor @Test func downloadSaysWhereItWent() async throws {
    let m = column([("/r/a.md", "o", 60)])
    m.download = { _ in URL(fileURLWithPath: "/Users/me/Downloads/a 2.md") }
    let a = m.files[0]
    m.downloadFile(a)
    #expect(m.notes[a.id] == .busy("Downloading…"))
    try await waitUntil { m.notes[a.id] == .done("In Downloads as a 2.md") }
}

// MARK: delete, against lasso

@MainActor private func app(_ src: FakeFileSource) -> AppCoordinator {
    let a = AppCoordinator(fleet: FleetModel(), sync: nil, fixture: nil)
    a.fileSource = src
    a.filesColumn.hover.timerEnabled = false
    a.bots.hover.timerEnabled = false
    return a
}

@MainActor @Test func deleteRemovesOnlyAFile() async throws {
    let src = FakeFileSource()
    src.put("/r/a.md")
    src.put("/r/now-a-folder", dir: true)
    let a = app(src)
    try await a.deleteRecentFile(RecentFile(path: "/r/a.md", at: t0, opened: true))
    #expect(src.deleted.map(\.path) == ["/r/a.md"])

    await #expect(throws: RecentFileError.self, "lasso would remove a folder and all; it is refused") {
        try await a.deleteRecentFile(RecentFile(path: "/r/now-a-folder", at: t0, opened: true))
    }
    try await a.deleteRecentFile(RecentFile(path: "/r/already-gone.md", at: t0, opened: true))
    #expect(src.deleted.map(\.path) == ["/r/a.md"], "a file already gone is only taken off the list")
}

// MARK: the rail and the coordinator

@MainActor @Test func theFilesButtonOpensItsColumnOnAHover() {
    let a = app(FakeFileSource())
    defer { a.closeRailPanels() }
    a.rail.model.setExpanded(true, animated: false)
    #expect(a.rail.model.items.contains(.files), "always on the rail, like Browser")
    a.hoverOpen(.bots)
    a.hoverOpen(.files)
    #expect(a.filesColumn.isOpen && !a.bots.isOpen, "one column at a time")
    a.hoverOpen(.terminal)
    #expect(!a.filesColumn.isOpen, "resting on a control without a column moves on")
}

@MainActor @Test func theDotGoesOnceTheColumnIsOpened() {
    let a = app(FakeFileSource())
    defer { a.closeRailPanels() }
    a.recentFiles.add(FileOpen(path: "/r/a.md"), opened: false, at: t0)
    #expect(a.rail.model.filesDot)
    a.openFiles(takeKey: false)
    #expect(!a.rail.model.filesDot && !a.recentFiles.unseen)
}

// MARK: reopening

@MainActor @Test func aRowReopensItsFileWhoeverIsAtTheMac() async {
    let src = FakeFileSource()
    src.put("/r/a.md")
    src.put("/r/dir", dir: true)
    let f = FileWindows(
        client: LassoClient(baseURL: URL(string: "http://127.0.0.1:9")!, protocolClasses: [FilePageStub.self]), source: src,
        presence: FixedPresence(false))
    f.enabled = { false }
    f.cache = FileCache(root: tempDir())
    defer { f.closeAll() }
    var changes = 0
    f.onChange = { changes += 1 }
    let o = FileOpen(path: "/r/a.md", from: "olivia")
    #expect(await f.opened(o) == .off, "an agent's open is gated")
    #expect(await f.reopen(o) == nil, "the person's pick is not")
    #expect(f.openKeys == [o.key] && changes == 1)
    #expect(f.window(o.key)?.panel.isKeyWindow == false)
    #expect(await f.reopen(FileOpen(path: "/r/gone.md")) == .missing)
    #expect(await f.reopen(FileOpen(path: "/r/dir")) == .directory)
}

// MARK: the preview

@MainActor private func loader(_ src: FakeFileSource) -> FilePreviewLoader {
    FilePreviewLoader(source: { src }, cache: FileCache(root: tempDir()))
}

@MainActor private func file(_ src: FakeFileSource, _ path: String, _ text: String) throws {
    let u = tempDir().appendingPathComponent((path as NSString).lastPathComponent)
    try Data(text.utf8).write(to: u)
    src.bytes[path] = u
    src.put(path, size: Int64(text.utf8.count))
}

@MainActor @Test func aPreviewShowsTheFirstLinesOfText() async throws {
    let src = FakeFileSource()
    let long = (1...(FilePreviewLoader.lineCap + 10)).map { "line \($0)" }.joined(separator: "\n")
    try file(src, "/r/a.swift", long)
    try file(src, "/r/short.txt", "hello\nworld")
    try file(src, "/r/README.md", "# Title\n\nSome *text*.")
    let l = loader(src)
    guard case .text(let s, let cut) = await l.load(RecentFile(path: "/r/a.swift", at: t0, opened: true), hostLabel: "titan") else {
        Issue.record("not text")
        return
    }
    #expect(cut && s.split(separator: "\n").count == FilePreviewLoader.lineCap && s.hasPrefix("line 1\n"))
    #expect(await l.load(RecentFile(path: "/r/short.txt", at: t0, opened: true), hostLabel: "titan") == .text("hello\nworld", cut: false))
    #expect(await l.load(RecentFile(path: "/r/README.md", at: t0, opened: true), hostLabel: "titan") == .markdown("# Title\n\nSome *text*.", cut: false))
}

@MainActor @Test func aPreviewNeverFetchesPastItsCap() async throws {
    let src = FakeFileSource()
    src.put("/r/big.log", size: FilePreviewLoader.textCap + 1)
    src.put("/r/huge.mov", size: FilePreviewLoader.fileCap + 1)
    src.put("/r/dir", dir: true)
    try file(src, "/r/bin.txt", "a\u{0}b")
    let l = loader(src)
    #expect(await l.load(RecentFile(path: "/r/big.log", at: t0, opened: true), hostLabel: "titan")
        == .note("Too large to preview", "\(FilePreviewLoader.size(FilePreviewLoader.textCap + 1)). Click to open it."))
    if case .note(let title, _) = await l.load(RecentFile(path: "/r/huge.mov", at: t0, opened: true), hostLabel: "titan") {
        #expect(title == "Too large to preview")
    } else {
        Issue.record("a big video was fetched")
    }
    #expect(src.downloads.isEmpty, "nothing past the cap comes down")
    if case .note(let title, _) = await l.load(RecentFile(path: "/r/gone.md", at: t0, opened: true), hostLabel: "titan") {
        #expect(title == "No longer on titan")
    } else {
        Issue.record("a missing file previewed")
    }
    if case .note(let title, _) = await l.load(RecentFile(path: "/r/dir", at: t0, opened: true), hostLabel: "titan") {
        #expect(title == "dir is a folder")
    } else {
        Issue.record("a folder previewed")
    }
    if case .note(let title, _) = await l.load(RecentFile(path: "/r/bin.txt", at: t0, opened: true), hostLabel: "titan") {
        #expect(title == "No preview", "not text")
    } else {
        Issue.record("binary shown as text")
    }
}

@MainActor @Test func aPreviewAsksAgainOnlyWhetherTheFileChanged() async throws {
    let src = FakeFileSource()
    try file(src, "/r/a.txt", "one")
    let l = loader(src)
    let f = RecentFile(path: "/r/a.txt", at: t0, opened: true)
    _ = await l.load(f, hostLabel: "titan")
    _ = await l.load(f, hostLabel: "titan")
    #expect(src.downloads == ["/r/a.txt"] && src.stats.count == 2, "the same version is read once")
    try file(src, "/r/a.txt", "two!")
    #expect(await l.load(f, hostLabel: "titan") == .text("two!", cut: false), "a new version is read again")
    #expect(src.downloads.count == 2)
}

/// Polls `cond` on the main actor until it holds (a second at most).
@MainActor private func waitUntil(_ cond: @MainActor () -> Bool) async throws {
    for _ in 0..<100 {
        if cond() { return }
        try await Task.sleep(nanoseconds: 10_000_000)
    }
    #expect(cond())
}
