import AppKit
import Foundation
import Testing

@testable import LassoDesktopKit

private let png = Data([0x89, 0x50, 0x4E, 0x47])
private let tiff = Data([0x4D, 0x4D, 0x00, 0x2A])

/// A paste decides files > image > text, so a local path never goes as text.
@Test func pastePlanPrefersFilesThenImagesThenText() {
    let shot = URL(fileURLWithPath: "/var/folders/ht/x/T/BetterShot-Clipboard/1/BetterShot_2026-10-11-19-16-44.png")
    // BetterShot: the file, its path as text, and an image of it.
    #expect(Attachments.plan(PasteContents(fileURLs: [shot], png: png, tiff: tiff, text: shot.path)) == .files([shot]))
    // Finder's Copy: the files, their names as text, an icon as TIFF.
    let a = URL(fileURLWithPath: "/Users/s/a.pdf"), b = URL(fileURLWithPath: "/Users/s/b.txt")
    #expect(Attachments.plan(PasteContents(fileURLs: [a, b], tiff: tiff, text: "a.pdf\rb.txt")) == .files([a, b]))
    // A screenshot to the clipboard: image data alone; PNG before TIFF.
    #expect(Attachments.plan(PasteContents(png: png, tiff: tiff)) == .image(png, isPNG: true))
    #expect(Attachments.plan(PasteContents(tiff: tiff)) == .image(tiff, isPNG: false))
    // A browser's Copy Image: the image with its address as text.
    #expect(Attachments.plan(PasteContents(tiff: tiff, text: "https://example.com/cat.png")) == .image(tiff, isPNG: false))
    #expect(Attachments.plan(PasteContents(png: png, text: "/tmp/cat.png")) == .image(png, isPNG: true))
    // Words copied from Word or Excel come with a rendering: they paste as text.
    #expect(Attachments.plan(PasteContents(png: png, text: "Q3 revenue\t12")) == .text)
    #expect(Attachments.plan(PasteContents(png: png, text: "hello")) == .text)
    // Plain text, and nothing at all.
    #expect(Attachments.plan(PasteContents(text: "/var/folders/x.png what's this")) == .text)
    #expect(Attachments.plan(PasteContents()) == .text)
}

@Test func uploadedPathsGoAtTheCaret() {
    #expect(Attachments.insert(["/r/a.png"], into: "what's this", at: 0) == ("/r/a.png what's this", 9))
    #expect(Attachments.insert(["/r/a.png"], into: "see  now", at: 4) == ("see /r/a.png now", 12))
    #expect(Attachments.insert(["/r/a.png", "/r/b.png"], into: "x", at: nil) == ("x /r/a.png /r/b.png", 19))
    #expect(Attachments.insert(["/r/a.png"], into: "", at: 99) == ("/r/a.png", 8))
}

private func scratchFile(_ name: String, bytes: Int) throws -> URL {
    let dir = FileManager.default.temporaryDirectory.appendingPathComponent("lasso-attach-\(UUID().uuidString)")
    try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    let f = dir.appendingPathComponent(name)
    try Data(repeating: 7, count: bytes).write(to: f)
    return f
}

@Test func foldersEmptyAndOversizedFilesAreRefused() throws {
    let ok = try scratchFile("ok.txt", bytes: 10)
    #expect(Attachments.refusal(ok) == nil)
    #expect(Attachments.refusal(ok, maxBytes: 5)?.contains("too large") == true)
    #expect(Attachments.refusal(try scratchFile("none.txt", bytes: 0))?.contains("empty") == true)
    #expect(Attachments.refusal(ok.deletingLastPathComponent())?.contains("folder") == true)
    #expect(Attachments.maxBytes == 1 << 30)
}

/// A pasted TIFF becomes pasted-<time>.png in a scratch folder that goes
/// once it is uploaded.
@Test func pastedImageIsWrittenAsPNGAndDiscarded() throws {
    let rep = NSBitmapImageRep(
        bitmapDataPlanes: nil, pixelsWide: 2, pixelsHigh: 2, bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true,
        isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
    let f = try Attachments.writeImage(rep.tiffRepresentation!, isPNG: false, now: Date(timeIntervalSince1970: 0))
    #expect(f.lastPathComponent.hasPrefix("pasted-") && f.pathExtension == "png")
    let head = try Data(contentsOf: f).prefix(4)
    #expect(head == png)
    Attachments.discardIfScratch(f)
    #expect(!FileManager.default.fileExists(atPath: f.deletingLastPathComponent().path))
    // A file the user chose is never removed.
    let mine = try scratchFile("mine.txt", bytes: 3)
    Attachments.discardIfScratch(mine)
    #expect(FileManager.default.fileExists(atPath: mine.path))
}

private let pane = HostPane(
    host: "titan", host_label: "titan", pane_id: "w1:p1", workspace_id: "w1", workspace_label: "Agents",
    tab_id: "w1:t1", tab_label: "stub", cwd: "/", agent: "claude", agent_status: "idle", has_agent: true, focused: false)

/// Pasting a file into a card's composer uploads it through the card's
/// actions and puts the uploaded path at the caret, not the Mac's path.
@MainActor @Test func pastedFileIsUploadedAndItsRemotePathInserted() async throws {
    let actions = FixtureCardActions(chats: [:], canAcknowledge: true)
    let m = CardModel(pane: pane, fleet: FleetModel(), actions: actions)
    let shot = try scratchFile("BetterShot_2026-10-11-19-16-44.png", bytes: 64)
    m.composer = "what's this"
    m.attach([shot], at: 0)
    try await Task.sleep(nanoseconds: 100_000_000)
    #expect(actions.calls == ["attach"])
    #expect(m.composer == "/fixture/BetterShot_2026-10-11-19-16-44.png what's this")
    #expect(!m.composer.contains(shot.deletingLastPathComponent().path))
    #expect(m.notice == "Attached BetterShot_2026-10-11-19-16-44.png")

    // Several at once: each uploaded, each path inserted; a refused one is never sent.
    let a = try scratchFile("a.txt", bytes: 1), empty = try scratchFile("b.txt", bytes: 0)
    m.composer = ""
    m.attach([a, empty], at: nil)
    try await Task.sleep(nanoseconds: 100_000_000)
    #expect(actions.calls == ["attach", "attach"])
    #expect(m.composer == "/fixture/a.txt")
    #expect(m.notice == "Attached a.txt; b.txt is empty")
}

@MainActor @Test func botChatPasteUploadsToTheBot() async throws {
    let actions = FixtureBotChatActions(payload: nil)
    let bot = try JSONDecoder().decode(BotView.self, from: Data(#"{"id":1,"name":"stub","host":"titan","runtime":"omp","stopped":false,"state":"idle"}"#.utf8))
    let m = ChatModel(bot: bot, actions: actions)
    m.attach([try scratchFile("x.png", bytes: 8)], at: nil)
    try await Task.sleep(nanoseconds: 100_000_000)
    #expect(actions.calls == ["attach"] && m.composer == "/fixture/x.png")
}

/// The New Agent prompt has no agent yet: a paste goes to the chosen machine.
@MainActor @Test func newAgentPasteUploadsToTheChosenHost() async throws {
    let source = FixtureNewAgentSource()
    let m = NewAgentModel(kind: .scratch, source: source)
    await m.load()
    m.host = "minime"
    m.prompt = "look at"
    m.attach([try scratchFile("y.png", bytes: 8)], at: nil)
    #expect(!m.canCreate)
    try await Task.sleep(nanoseconds: 100_000_000)
    #expect(source.uploads == ["minime:y.png"])
    #expect(m.prompt == "look at /fixture/minime/y.png" && m.notice == "Attached y.png")
    m.prompt += "!"
    #expect(m.notice == nil)
}
