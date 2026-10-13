import Foundation
import UniformTypeIdentifiers

// An agent showing the human a file (open_file, `lasso open`): lasso's
// `open-file` event, and what the app needs to know about the file to pick a
// window for it (PLAN 26). The server's shapes are openFileEvent and
// fileStatResp/fileStatReq (src/openfile.go, src/filestat.go); the contract
// goldens in Spec/contract hold the two to the same tags.

/// The `open-file` event: a file (or a folder) an agent opened for the human.
public struct FileOpen: Codable, Sendable, Equatable {
    public var path: String
    /// The machine it lives on, as lasso names it ("local" for lasso's own).
    public var host: String
    /// The line to show, 1-based.
    public var line: Int?
    public var dir: Bool?
    /// Who opened it: the agent's name.
    public var from: String?

    public init(path: String, host: String = "local", line: Int? = nil, dir: Bool? = nil, from: String? = nil) {
        self.path = path
        self.host = host
        self.line = line
        self.dir = dir
        self.from = from
    }

    /// The host as every request names it: lasso's own is "local".
    public var hostKey: String { FileHost.normal(host) }
    /// The file's name, as its window's bar shows it.
    public var name: String { (path as NSString).lastPathComponent }
    /// Who opened it, as a sentence starts.
    public var who: String { (from?.isEmpty ?? true) ? "an agent" : from! }
    /// The window it belongs in: one per file per machine.
    public var key: String { FileHost.key(host: hostKey, path: path) }
}

enum FileHost {
    /// lasso's own machine is "local" whether the event spelled it or not.
    static func normal(_ h: String?) -> String { (h?.isEmpty ?? true) ? "local" : h! }

    static func key(host: String, path: String) -> String { normal(host) + "\u{0}" + path }
}

/// POST /api/file-stat's body: up to 64 paths on one machine.
public struct FileStatRequest: Codable, Sendable, Equatable {
    public var host: String
    public var paths: [String]

    public init(host: String, paths: [String]) {
        self.host = host
        self.paths = paths
    }
}

/// One path's state on its machine. `mtime` is unix milliseconds, the clock
/// /api/file's X-Lasso-Mtime and /api/file-write's base_mtime use.
public struct FileStatEntry: Codable, Sendable, Equatable {
    public var path: String
    public var exists: Bool
    public var dir: Bool
    public var size: Int64
    public var mtime: Int64
    public var error: String?

    public init(path: String, exists: Bool, dir: Bool = false, size: Int64 = 0, mtime: Int64 = 0, error: String? = nil) {
        self.path = path
        self.exists = exists
        self.dir = dir
        self.size = size
        self.mtime = mtime
        self.error = error
    }

    /// What a change is measured by: whether it is there, its size and its
    /// mtime (SFTP's whole seconds included: the page compares content).
    var signature: String { exists ? "\(size):\(mtime)" : "-" }
}

/// POST /api/file-stat's answer, in the request's order.
public struct FileStatPayload: Codable, Sendable, Equatable {
    public var host: String
    public var files: [FileStatEntry]

    public init(host: String, files: [FileStatEntry]) {
        self.host = host
        self.files = files
    }
}

/// How a file is shown. The web kinds run in lasso's page (the editor, and
/// rendered markdown and SVG); rendered HTML has a web view of its own
/// (DocPage); the rest are the Mac's own viewers (FileViewers).
public enum FileKind: String, Sendable, CaseIterable {
    case markdown, html, svg, text, image, av, pdf, quicklook

    /// /api/file swaps a text file over 2 MiB for a "too large to preview"
    /// line, and saving that line would overwrite the real file: such a file
    /// is shown read-only by QuickLook instead (the server's maxPreview).
    public static let textLimit: Int64 = 2 << 20

    /// What a file is, by its type (UTType from its extension) and size.
    public static func classify(path: String, size: Int64?) -> FileKind {
        let ext = (path as NSString).pathExtension.lowercased()
        let kind = byType(ext)
        if kind.isWeb, let size, size > textLimit { return .quicklook }
        return kind
    }

    private static let markdownExtensions: Set<String> = ["md", "markdown", "mdown", "mkd", "mkdn", "mdx"]

    /// Extensions macOS gives another type that agents write as source:
    /// `.ts` is an MPEG transport stream to UTType, TypeScript to everyone here.
    private static let sourceExtensions: Set<String> = ["ts", "tsx", "mts", "cts"]

    private static func byType(_ ext: String) -> FileKind {
        if markdownExtensions.contains(ext) { return .markdown }
        if sourceExtensions.contains(ext) { return .text }
        // No extension (Makefile, Dockerfile, .env): text.
        guard !ext.isEmpty, let t = UTType(filenameExtension: ext) else { return .text }
        if t.conforms(to: .svg) { return .svg }
        if t.conforms(to: .html) { return .html }
        if t.conforms(to: .pdf) { return .pdf }
        if t.conforms(to: .image) { return .image }
        if t.conforms(to: .audiovisualContent) || t.conforms(to: .audio) || t.conforms(to: .movie) { return .av }
        for text in [UTType.text, .sourceCode, .script, .json, .xml, .yaml, .commaSeparatedText] where t.conforms(to: text) {
            return .text
        }
        // An extension macOS knows nothing of is most likely a source or
        // config file (.toml, .lock, .proto); a known one that is none of the
        // above (a document, an archive, a 3D scene) is QuickLook's.
        return t.isDynamic ? .text : .quicklook
    }

    /// Shown and edited in lasso's page.
    public var isWeb: Bool { [.markdown, .html, .svg, .text].contains(self) }
    /// Has a rendered view beside its source (the bar's rich/raw toggle).
    public var hasRich: Bool { [.markdown, .html, .svg].contains(self) }

    /// AVFoundation plays neither these containers nor what is in them, and
    /// QuickLook cannot either: the window says so and offers the default app.
    private static let unplayable: Set<String> = ["webm", "mkv", "ogg", "ogv", "oga", "opus"]

    public static func playable(_ path: String) -> Bool {
        !unplayable.contains((path as NSString).pathExtension.lowercased())
    }

    /// The bar's icon.
    var icon: Lucide {
        switch self {
        case .markdown, .pdf: .fileText
        case .html, .text: .fileCode
        case .svg, .image: .image
        case .av: .film
        case .quicklook: .file
        }
    }
}

/// The page's mode for a web kind: rendered, or its source in the editor.
public enum FileMode: String, Sendable {
    case rich, raw
}

/// What the app asks of lasso about files (a fake in tests and the harness).
@MainActor
public protocol FileSource: AnyObject {
    /// POST /api/file-stat: each path's state, in order. Throws on a lasso
    /// that predates the route (it answers its web app, LassoClient.send's
    /// "no such route").
    func stat(host: String, paths: [String]) async throws -> [FileStatEntry]
    /// A file's bytes, whole, to `dest` (`/api/file?download=1`, past the
    /// preview cap).
    func download(host: String, path: String, to dest: URL, progress: @escaping @Sendable (Int64, Int64) -> Void) async throws
}

/// The source on a live lasso.
@MainActor
public final class LiveFileSource: FileSource {
    let client: LassoClient

    public init(client: LassoClient) { self.client = client }

    public func stat(host: String, paths: [String]) async throws -> [FileStatEntry] {
        let p: FileStatPayload = try await client.post("/api/file-stat", json: FileStatRequest(host: host, paths: paths))
        return p.files
    }

    public func download(host: String, path: String, to dest: URL, progress: @escaping @Sendable (Int64, Int64) -> Void) async throws {
        let r = client.request(raw: "/api/file?" + FilePage.query([("path", path), ("host", host), ("download", "1")]))
        try await client.download(r, to: dest, progress: progress)
    }
}
