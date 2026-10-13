import AppKit
import Foundation
import SwiftUI
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
/// actions and attaches it: the text is untouched, the attachment holds the
/// uploaded path (never the Mac's), and a send puts that path in the message.
@MainActor @Test func pastedFileIsUploadedAsAnAttachment() async throws {
    let actions = FixtureCardActions(chats: [:], canAcknowledge: true)
    let m = CardModel(pane: pane, fleet: FleetModel(), actions: actions)
    let shot = try scratchFile("BetterShot_2026-10-11-19-16-44.png", bytes: 64)
    m.composer = "what's this"
    m.attach([shot], at: 0)
    try await Task.sleep(nanoseconds: 100_000_000)
    #expect(actions.calls == ["attach"])
    #expect(m.composer == "what's this")
    #expect(m.attachments.map(\.path) == ["/fixture/BetterShot_2026-10-11-19-16-44.png"])
    #expect(m.composedMessage == "what's this /fixture/BetterShot_2026-10-11-19-16-44.png")
    #expect(!m.composedMessage.contains(shot.deletingLastPathComponent().path))
    #expect(m.notice == "Attached BetterShot_2026-10-11-19-16-44.png")

    // Several at once: each uploaded and attached; a refused one is never sent.
    let a = try scratchFile("a.txt", bytes: 1), empty = try scratchFile("b.txt", bytes: 0)
    m.attachments = []
    m.attach([a, empty], at: nil)
    try await Task.sleep(nanoseconds: 100_000_000)
    #expect(actions.calls == ["attach", "attach"])
    #expect(m.attachments.map(\.path) == ["/fixture/a.txt"] && m.attachments[0].thumbnail == nil)
    #expect(m.notice == "Attached a.txt; b.txt is empty")
}

@MainActor @Test func botChatPasteUploadsToTheBot() async throws {
    let actions = FixtureBotChatActions(payload: nil)
    let bot = try JSONDecoder().decode(BotView.self, from: Data(#"{"id":1,"name":"stub","host":"titan","runtime":"omp","stopped":false,"state":"idle"}"#.utf8))
    let m = ChatModel(bot: bot, actions: actions)
    m.attach([try scratchFile("x.png", bytes: 8)], at: nil)
    try await Task.sleep(nanoseconds: 100_000_000)
    #expect(actions.calls == ["attach"] && m.composer == "" && m.attachments.map(\.path) == ["/fixture/x.png"])
    // An attachment alone is a message: it sends, and the composer empties.
    #expect(m.canSend && !m.composerIsEmpty)
    m.send()
    try await Task.sleep(nanoseconds: 100_000_000)
    #expect(actions.sent == ["/fixture/x.png"] && m.attachments.isEmpty)
}

/// A 2×2 PNG, as a screenshot to the clipboard carries it.
private func pngData() -> Data {
    let rep = NSBitmapImageRep(
        bitmapDataPlanes: nil, pixelsWide: 2, pixelsHigh: 2, bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true,
        isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
    return rep.representation(using: .png, properties: [:])!
}

/// A pasteboard of the test's own, so the user's clipboard is never touched.
@MainActor private func pasteboard(_ write: (NSPasteboard) -> Void) -> NSPasteboard {
    let pb = NSPasteboard(name: NSPasteboard.Name("lasso-test-\(UUID().uuidString)"))
    pb.clearContents()
    write(pb)
    return pb
}

/// An image pasted on the composer is an attachment, never text: the words
/// typed stay as they were, the preview has a thumbnail, and the send
/// carries the words and then the uploaded path.
@MainActor @Test func pastedImageIsAnAttachmentNotText() async throws {
    let actions = FixtureCardActions(chats: [:], canAcknowledge: true)
    let m = CardModel(pane: pane, fleet: FleetModel(), actions: actions)
    m.composer = "what's in\nthis screenshot?"
    let pb = pasteboard { $0.setData(pngData(), forType: .png) }
    #expect(AttachmentInput.take(pb, caret: 4, model: m))
    try await Task.sleep(nanoseconds: 100_000_000)
    #expect(m.composer == "what's in\nthis screenshot?")
    let a = try #require(m.attachments.first)
    #expect(m.attachments.count == 1)
    #expect(a.name.hasPrefix("pasted-") && a.path == "/fixture/\(a.name)" && a.thumbnail != nil)
    #expect(m.canSend && m.hasDraft)
    // One still uploading (no path yet): Send waits for it.
    let pending = ComposerAttachment(name: "late.png")
    m.attachments.append(pending)
    #expect(m.uploading && !m.canSend)
    m.removeAttachment(pending.id)
    m.send()
    try await Task.sleep(nanoseconds: 100_000_000)
    #expect(actions.sent == ["what's in\nthis screenshot? /fixture/\(a.name)"])
    #expect(m.composer == "" && m.attachments.isEmpty)

    // Words copied with a rendering stay a text paste, which the field does.
    let words = pasteboard {
        $0.setData(pngData(), forType: .png)
        $0.setString("Q3 revenue\t12", forType: .string)
    }
    #expect(!AttachmentInput.take(words, caret: nil, model: m))
    #expect(m.attachments.isEmpty)
}

/// A preview's x takes its file out of the message: it is not sent, and
/// the ones left are, in the order they were attached.
@MainActor @Test func removingAnAttachmentDropsItFromTheMessage() async throws {
    let actions = FixtureCardActions(chats: [:], canAcknowledge: true)
    let m = CardModel(pane: pane, fleet: FleetModel(), actions: actions)
    m.attach([try scratchFile("one.png", bytes: 8), try scratchFile("two.log", bytes: 8), try scratchFile("three.png", bytes: 8)], at: nil)
    try await Task.sleep(nanoseconds: 100_000_000)
    #expect(m.attachments.map(\.name) == ["one.png", "two.log", "three.png"])
    m.removeAttachment(m.attachments[1].id)
    m.composer = "compare these"
    #expect(m.composedMessage == "compare these /fixture/one.png /fixture/three.png")
    m.send()
    try await Task.sleep(nanoseconds: 100_000_000)
    #expect(actions.sent == ["compare these /fixture/one.png /fixture/three.png"])
    // Every attachment taken out, and nothing typed: nothing to send.
    m.attach([try scratchFile("four.png", bytes: 8)], at: nil)
    try await Task.sleep(nanoseconds: 100_000_000)
    m.removeAttachment(m.attachments[0].id)
    #expect(m.composerIsEmpty && !m.canSend && !m.hasDraft)
    m.send()
    #expect(actions.sent.count == 1)
}

/// An unsent message's attachments are kept with its words when the card
/// goes, and come back with them.
@MainActor @Test func aDraftKeepsItsAttachments() {
    let m = CardModel(pane: pane, fleet: FleetModel(), actions: FixtureCardActions(chats: [:], canAcknowledge: true))
    #expect(m.draft == nil)
    m.attachments = [ComposerAttachment(name: "a.png", path: "/r/a.png"), ComposerAttachment(name: "b.png")]
    // One still uploading is not kept.
    let d = m.draft
    #expect(d?.text == "" && d?.attachments.map(\.path) == ["/r/a.png"])
    let n = CardModel(pane: pane, fleet: FleetModel(), actions: FixtureCardActions(chats: [:], canAcknowledge: true))
    n.draft = d
    #expect(n.composedMessage == "/r/a.png" && n.hasDraft)
}

/// Return sends; Shift- or Option-Return starts a new line; ⌘ and ⌃ go to the system.
@Test func returnSendsAndShiftReturnStartsALine() {
    #expect(ComposerReturn([]) == .send)
    #expect(ComposerReturn(.capsLock) == .send)
    #expect(ComposerReturn(.shift) == .newline)
    #expect(ComposerReturn(.option) == .newline)
    #expect(ComposerReturn([.shift, .option]) == .newline)
    #expect(ComposerReturn(.command) == nil)
    #expect(ComposerReturn([.control, .shift]) == nil)
}

private func keyEvent(_ c: String, _ code: UInt16, _ flags: NSEvent.ModifierFlags = []) -> NSEvent {
    NSEvent.keyEvent(
        with: .keyDown, location: .zero, modifierFlags: flags, timestamp: 0, windowNumber: 0, context: nil,
        characters: c, charactersIgnoringModifiers: c, isARepeat: false, keyCode: code)!
}

/// The composer field itself, typed into: Shift-Return and Option-Return put
/// a newline in the message and send nothing; Return sends the lines as one
/// message. The field grows a line at a time and stops at `max_lines`.
@MainActor @Test func theComposerTakesSeveralLines() async throws {
    let actions = FixtureCardActions(chats: [:], canAcknowledge: true)
    let m = CardModel(pane: pane, fleet: FleetModel(), actions: actions)
    let host = NSHostingView(rootView: ComposerRow(model: m, attach: {}).frame(width: 400))
    let w = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 400, height: 300), styleMask: [.borderless], backing: .buffered, defer: false)
    w.contentView = host
    defer { w.orderOut(nil) }
    host.layoutSubtreeIfNeeded()
    func field(_ v: NSView) -> ComposerTextField? {
        if let f = v as? ComposerTextField { return f }
        for sub in v.subviews { if let f = field(sub) { return f } }
        return nil
    }
    let f = try #require(field(host))
    #expect(w.makeFirstResponder(f))
    #expect(m.composerFocused)
    let one = try #require(f.enclosingScrollView).frame.height
    #expect(one == Theme.Control.composer.number("height"))

    f.keyDown(with: keyEvent("a", 0))
    f.keyDown(with: keyEvent("\r", 36, .shift))
    f.keyDown(with: keyEvent("b", 11))
    f.keyDown(with: keyEvent("\r", 36, .option))
    f.keyDown(with: keyEvent("c", 8))
    #expect(m.composer == "a\nb\nc")
    #expect(actions.calls.isEmpty)
    await settle {
        host.layoutSubtreeIfNeeded()
        return (f.enclosingScrollView?.frame.height ?? 0) > one
    }
    #expect(try #require(f.enclosingScrollView).frame.height > one)
    // Pasted lines keep their newlines.
    f.insertText("\nd\ne", replacementRange: f.selectedRange())
    #expect(m.composer == "a\nb\nc\nd\ne")

    // Taller than max_lines: the field stops growing and scrolls.
    let line = ComposerLayout.lineHeight
    let most = ComposerLayout.height(String(repeating: "x\n", count: 20), width: 300)
    #expect(abs(most - (line * Theme.Control.composerGrow.number("max_lines") + 2 * ComposerLayout.inset).rounded(.up)) < 0.5)
    #expect(ComposerLayout.height("", width: 300) == Theme.Control.composer.number("height"))

    f.keyDown(with: keyEvent("\r", 36))
    await settle { f.string.isEmpty }
    #expect(actions.sent == ["a\nb\nc\nd\ne"])
    #expect(m.composer == "" && f.string == "")
}

/// The New Agent prompt has no agent yet: a paste goes to the chosen
/// machine as a preview, never text. Its path follows the prompt after a
/// blank line (the first line still names the agent), a command's after
/// the command; the x or another machine takes it out.
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
    #expect(m.prompt == "look at" && m.notice == "Attached y.png")
    #expect(m.attachments.map(\.path) == ["/fixture/minime/y.png"])
    #expect(m.request.prompt == "look at\n\n/fixture/minime/y.png" && m.title == "look at")
    m.kind = .terminal
    #expect(m.sentPrompt == "look at /fixture/minime/y.png")
    m.kind = .scratch
    m.prompt += "!"
    #expect(m.notice == nil)
    m.removeAttachment(m.attachments[0].id)
    #expect(m.request.prompt == "look at!")
    m.attach([try scratchFile("z.png", bytes: 8)], at: nil)
    try await Task.sleep(nanoseconds: 100_000_000)
    m.host = "local"
    #expect(m.attachments.isEmpty && m.notice?.hasPrefix("Attachments dropped") == true)
}
