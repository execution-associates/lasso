import AppKit
import Foundation
import UniformTypeIdentifiers

/// What a paste or a drop into a message box carries, read off the pasteboard.
public struct PasteContents: Equatable, Sendable {
    public var fileURLs: [URL] = []
    public var png: Data?
    /// TIFF, or any other image AppKit can read, as TIFF.
    public var tiff: Data?
    /// The plain text, if any.
    public var text: String?

    public init(fileURLs: [URL] = [], png: Data? = nil, tiff: Data? = nil, text: String? = nil) {
        self.fileURLs = fileURLs
        self.png = png
        self.tiff = tiff
        self.text = text
    }
}

/// What a paste or a drop does with it.
public enum PastePlan: Equatable, Sendable {
    /// Upload these files to the agent's machine and insert their paths.
    case files([URL])
    /// Write this image to a PNG, then upload it as a file.
    case image(Data, isPNG: Bool)
    /// Plain text: paste it as the text field always has.
    case text
}

/// Files and images handed to a message box: each is uploaded to the machine
/// the agent runs on (lasso's /api/paste-file), and the path it lands at
/// is what goes in the message, never a path on this Mac.
public enum Attachments {
    /// lasso's /api/paste-file cap (src/main.go maxUpload, 1 GiB).
    public static let maxBytes: Int64 = 1 << 30

    /// Decide a paste. File URLs win over everything else on the pasteboard:
    /// a screenshot tool (BetterShot, CleanShot) and Finder put the file
    /// alongside its path or name as text and an icon as an image, and the
    /// file is what was meant. Image data wins over text only when the text
    /// is nothing but a URL or a path (a browser's Copy Image); text with
    /// words in it alongside a rendering (Word, Excel, Keynote) pastes as text.
    public static func plan(_ c: PasteContents) -> PastePlan {
        if !c.fileURLs.isEmpty { return .files(c.fileURLs) }
        let image: PastePlan
        if let p = c.png {
            image = .image(p, isPNG: true)
        } else if let t = c.tiff {
            image = .image(t, isPNG: false)
        } else {
            return .text
        }
        let text = (c.text ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        if text.isEmpty || isLocator(text) { return image }
        return .text
    }

    /// One token that names a place: a URL with a scheme, or a file path.
    static func isLocator(_ s: String) -> Bool {
        guard !s.contains(where: \.isWhitespace) else { return false }
        return s.hasPrefix("/") || s.hasPrefix("~/") || s.range(of: #"^[a-zA-Z][a-zA-Z0-9+.-]*://"#, options: .regularExpression) != nil
    }

    /// Read what a paste or a drop could use from a pasteboard.
    @MainActor
    public static func read(_ pb: NSPasteboard) -> PasteContents {
        var c = PasteContents()
        c.fileURLs = (pb.readObjects(forClasses: [NSURL.self], options: [.urlReadingFileURLsOnly: true]) as? [URL]) ?? []
        c.png = pb.data(forType: .png)
        c.tiff = pb.data(forType: .tiff)
        if c.png == nil, c.tiff == nil, c.fileURLs.isEmpty,
            let img = (pb.readObjects(forClasses: [NSImage.self], options: nil) as? [NSImage])?.first
        {
            c.tiff = img.tiffRepresentation
        }
        c.text = pb.string(forType: .string)
        return c
    }

    /// The pasteboard types a message box takes as a drop.
    public static let dropTypes: [NSPasteboard.PasteboardType] = [.fileURL, .png, .tiff]

    /// Where pasted images are written before they are uploaded.
    static var scratch: URL { FileManager.default.temporaryDirectory.appendingPathComponent("LassoPaste", isDirectory: true) }

    /// Write a pasted image as `pasted-<timestamp>.png` for upload.
    public static func writeImage(_ data: Data, isPNG: Bool, now: Date = Date()) throws -> URL {
        var png = data
        if !isPNG {
            guard let rep = NSBitmapImageRep(data: data), let p = rep.representation(using: .png, properties: [:]) else {
                throw Unreadable()
            }
            png = p
        }
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.dateFormat = "yyyy-MM-dd-HH-mm-ss"
        let dir = scratch.appendingPathComponent(UUID().uuidString, isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let url = dir.appendingPathComponent("pasted-\(f.string(from: now)).png")
        try png.write(to: url)
        return url
    }

    struct Unreadable: Error, CustomStringConvertible {
        var description: String { "the pasted image could not be read" }
    }

    /// The files a plan uploads (writing an image first); nil for text.
    public static func files(for plan: PastePlan) throws -> [URL]? {
        switch plan {
        case .text: nil
        case .files(let urls): urls
        case .image(let data, let isPNG): [try writeImage(data, isPNG: isPNG)]
        }
    }

    /// A pasted image's scratch file goes once it is uploaded (or refused).
    public static func discardIfScratch(_ file: URL) {
        let dir = file.deletingLastPathComponent()
        guard dir.deletingLastPathComponent().standardizedFileURL.path == scratch.standardizedFileURL.path else { return }
        try? FileManager.default.removeItem(at: dir)
    }

    /// Why a file cannot be attached (a folder, empty, over lasso's cap), or nil.
    public static func refusal(_ file: URL, maxBytes: Int64 = maxBytes) -> String? {
        let name = file.lastPathComponent
        guard let v = try? file.resourceValues(forKeys: [.isDirectoryKey, .fileSizeKey, .isRegularFileKey]) else {
            return "\(name) could not be read"
        }
        if v.isDirectory == true { return "\(name) is a folder: attach the files in it" }
        let size = Int64(v.fileSize ?? 0)
        if size == 0 { return "\(name) is empty" }
        if size > maxBytes {
            let f = ByteCountFormatter()
            f.countStyle = .binary
            return "\(name) is too large to attach (\(f.string(fromByteCount: size)); lasso takes up to \(f.string(fromByteCount: maxBytes)))"
        }
        return nil
    }

    /// Insert `paths` into `text` at the UTF-16 offset `caret` (the end when
    /// nil or out of range), set off by spaces from the words around them.
    /// Answers the new text and the offset just after what was inserted.
    public static func insert(_ paths: [String], into text: String, at caret: Int?) -> (text: String, caret: Int) {
        let u = Array(text.utf16)
        let at = caret.map { min(max($0, 0), u.count) } ?? u.count
        var piece = paths.joined(separator: " ")
        let space = " ".utf16.first!
        let isSpace: (UInt16) -> Bool = { $0 == space || $0 == "\n".utf16.first! || $0 == "\t".utf16.first! }
        if at > 0, !isSpace(u[at - 1]) { piece = " " + piece }
        if at < u.count, !isSpace(u[at]) { piece += " " }
        let out = String(decoding: Array(u[..<at]) + Array(piece.utf16) + Array(u[at...]), as: UTF16.self)
        return (out, at + piece.utf16.count)
    }
}

extension Attachments {
    struct Failed: Error, CustomStringConvertible {
        let name: String
        let error: any Error
        var description: String { "\(name): \(error)" }
    }

    /// Upload each file that may go, inserting each path into the model's
    /// `text` at `caret` as it lands (so a failure part way keeps the paths
    /// already uploaded), and answer the notice to show. A file refused here
    /// (a folder, empty, over lasso's cap) is never sent; nothing on this
    /// Mac's disk is named in the message.
    @MainActor
    public static func upload<M: AnyObject>(
        _ files: [URL], at caret: Int?, into text: ReferenceWritableKeyPath<M, String>, of model: M,
        _ send: (URL) async throws -> String
    ) async throws -> String {
        defer { files.forEach(discardIfScratch) }
        var at = caret
        var done: [String] = []
        var refused: [String] = []
        for f in files {
            if let why = refusal(f) {
                refused.append(why)
                continue
            }
            let path: String
            do { path = try await send(f) } catch { throw Failed(name: f.lastPathComponent, error: error) }
            let r = insert([path], into: model[keyPath: text], at: at)
            model[keyPath: text] = r.text
            at = r.caret
            done.append(f.lastPathComponent)
        }
        let attached = done.isEmpty ? [] : [done.count == 1 ? "Attached \(done[0])" : "Attached \(done.count) files"]
        return (attached + refused).joined(separator: "; ")
    }

    /// The notice while an upload runs.
    public static func progress(_ files: [URL]) -> String {
        files.count == 1 ? "Attaching \(files[0].lastPathComponent)…" : "Attaching \(files.count) files…"
    }
}

/// A model whose message box takes uploaded files: the paperclip's open
/// panel, a paste and a drop all end in `attach(_:at:)`.
@MainActor
public protocol AttachingModel: AnyObject {
    /// Upload `files` to the agent's machine and insert their paths at the
    /// UTF-16 offset `caret` of the message (its end when nil).
    func attach(_ files: [URL], at caret: Int?)
    func say(_ s: String?)
}

/// The paste and drop handling a message box's text view shares.
@MainActor
enum AttachmentInput {
    /// Take a paste or a drop if it carries files or an image: true when it
    /// was taken (uploaded or refused), false to paste the text as usual.
    static func take(_ pb: NSPasteboard, caret: Int?, model: AttachingModel?) -> Bool {
        let plan = Attachments.plan(Attachments.read(pb))
        if plan == .text { return false }
        guard let model else { return true }
        do {
            if let files = try Attachments.files(for: plan) { model.attach(files, at: caret) }
        } catch {
            model.say("Paste failed: \(error)")
        }
        return true
    }

    /// Whether a drag carries something the message box uploads.
    static func accepts(_ pb: NSPasteboard) -> Bool {
        Attachments.plan(Attachments.read(pb)) != .text
    }
}

/// A text view (a field's editor, or the New Agent prompt) that uploads what
/// is pasted or dropped on it when that is files or an image.
final class AttachingTextView: NSTextView {
    weak var attachTarget: AttachingModel?

    private var caret: Int { selectedRange().location + selectedRange().length }

    override func paste(_ sender: Any?) {
        if AttachmentInput.take(.general, caret: caret, model: attachTarget) { return }
        super.paste(sender)
    }

    override func pasteAsPlainText(_ sender: Any?) {
        if AttachmentInput.take(.general, caret: caret, model: attachTarget) { return }
        super.pasteAsPlainText(sender)
    }

    override var acceptableDragTypes: [NSPasteboard.PasteboardType] {
        var t = super.acceptableDragTypes
        for d in Attachments.dropTypes where !t.contains(d) { t.append(d) }
        return t
    }

    override func draggingEntered(_ sender: NSDraggingInfo) -> NSDragOperation {
        AttachmentInput.accepts(sender.draggingPasteboard) ? .copy : super.draggingEntered(sender)
    }

    override func draggingUpdated(_ sender: NSDraggingInfo) -> NSDragOperation {
        AttachmentInput.accepts(sender.draggingPasteboard) ? .copy : super.draggingUpdated(sender)
    }

    override func performDragOperation(_ sender: NSDraggingInfo) -> Bool {
        let at = characterIndexForInsertion(at: convert(sender.draggingLocation, from: nil))
        if AttachmentInput.take(sender.draggingPasteboard, caret: at, model: attachTarget) { return true }
        return super.performDragOperation(sender)
    }
}
