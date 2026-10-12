import AVFoundation
import AVKit
import AppKit
import Combine
import CryptoKit
import SwiftUI

// Media an agent shows the human: the files of Claude Code's SendUserFile
// (chatTool.files) and the images an agent writes into its prose as markdown.
// Images render inline, audio and video as players, anything else as a chip
// that opens the file. The bytes come through lasso's /api/file on the host
// that holds the transcript, and are cached on this Mac.

/// Where a shown file's bytes come from: a local copy of it.
@MainActor
public protocol MediaSource: AnyObject, Sendable {
    /// A local file holding `path` on `host`. `version` names this showing of
    /// it (the chat item's id): the same path shown again later is fetched
    /// again, since an agent overwrites its output in place.
    func local(_ path: String, host: String, version: String) async throws -> URL
}

/// lasso's /api/file, cached in ~/Library/Caches/LassoDesktop/Media.
@MainActor
public final class LiveMediaSource: MediaSource {
    let client: LassoClient
    private var inflight: [String: Task<URL, Error>] = [:]

    public init(client: LassoClient) { self.client = client }

    static var dir: URL {
        FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("LassoDesktop/Media", isDirectory: true)
    }

    public func local(_ path: String, host: String, version: String) async throws -> URL {
        let digest = SHA256.hash(data: Data("\(host)\u{0}\(path)\u{0}\(version)".utf8))
            .map { String(format: "%02x", $0) }.joined()
        let ext = (path as NSString).pathExtension
        let file = Self.dir.appendingPathComponent(ext.isEmpty ? digest : "\(digest).\(ext)")
        if FileManager.default.fileExists(atPath: file.path) { return file }
        if let t = inflight[file.path] { return try await t.value }
        let client = client
        let task = Task<URL, Error> {
            let data: Data
            if let url = URL(string: path), let scheme = url.scheme, ["http", "https"].contains(scheme.lowercased()) {
                // An image the agent linked on the web: fetched as a browser would.
                (data, _) = try await URLSession.shared.data(from: url)
            } else {
                let (d, code) = try await client.raw(
                    client.request("/api/file", query: ["path": path, "host": host, "download": "1"]))
                guard code == 200 else { throw LassoError.http(code, "could not read \(path)") }
                data = d
            }
            try FileManager.default.createDirectory(at: Self.dir, withIntermediateDirectories: true)
            try data.write(to: file, options: .atomic)
            return file
        }
        inflight[file.path] = task
        defer { inflight[file.path] = nil }
        return try await task.value
    }
}

/// The parity fixtures' media: files beside the fixtures (Spec/fixtures/media),
/// by name; nothing is fetched.
@MainActor
public final class FixtureMediaSource: MediaSource {
    let dir: URL
    public init(dir: URL) { self.dir = dir }

    public func local(_ path: String, host: String, version: String) async throws -> URL {
        let f = dir.appendingPathComponent((path as NSString).lastPathComponent)
        guard FileManager.default.fileExists(atPath: f.path) else { throw LassoError.http(404, path) }
        return f
    }
}

struct MediaSourceKey: EnvironmentKey {
    static let defaultValue: MediaSource? = nil
}

extension EnvironmentValues {
    var mediaSource: MediaSource? {
        get { self[MediaSourceKey.self] }
        set { self[MediaSourceKey.self] = newValue }
    }
}

/// One file's load: its local copy, or why there is none. Kept by key so a
/// redraw does not fetch again (the Command Line Tools have no @StateObject).
@MainActor
final class MediaLoad: ObservableObject {
    @Published var url: URL?
    @Published var image: NSImage?
    @Published var failed: String?
    @Published var playing = false
    var player: AVPlayer?
    private var started = false
    private var endObserver: NSObjectProtocol?

    private static var all: [String: MediaLoad] = [:]

    static func of(_ key: String) -> MediaLoad {
        if let m = all[key] { return m }
        let m = MediaLoad()
        all[key] = m
        return m
    }

    func start(_ file: SharedFile, host: String, version: String, source: MediaSource?) {
        guard !started, let source else { return }
        started = true
        Task {
            do {
                let u = try await source.local(file.path, host: host, version: version)
                if file.kind == "image" { image = NSImage(contentsOf: u) }
                url = u
                // The conversation grew: whoever was at its end stays there.
                NotificationCenter.default.post(name: .lassoMediaLoaded, object: nil)
            } catch {
                failed = String(describing: error)
            }
        }
    }

    func avPlayer() -> AVPlayer? {
        guard let url else { return nil }
        if let player { return player }
        let p = AVPlayer(url: url)
        endObserver = NotificationCenter.default.addObserver(
            forName: .AVPlayerItemDidPlayToEndTime, object: p.currentItem, queue: .main
        ) { [weak self] _ in
            MainActor.assumeIsolated {
                self?.playing = false
                self?.player?.seek(to: .zero)
            }
        }
        player = p
        return p
    }

    func toggle() {
        guard let p = avPlayer() else { return }
        if playing { p.pause() } else { p.play() }
        playing.toggle()
    }
}

/// The files of one showing, with their caption once above them.
struct SharedFilesView: View {
    let files: [SharedFile]
    let host: String
    let version: String

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.space("m")) {
            if let c = files.first?.caption, !c.isEmpty {
                Text(c)
                    .font(Theme.Fonts.small.font)
                    .foregroundStyle(Theme.Text.secondary.color)
            }
            ForEach(files, id: \.self) { f in MediaView(file: f, host: host, version: version) }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .parityAnchor("agent.media")
    }
}

/// One file, in the viewer its kind wants.
struct MediaView: View {
    let file: SharedFile
    let host: String
    let version: String
    @ObservedObject var load: MediaLoad
    @Environment(\.mediaSource) private var source

    init(file: SharedFile, host: String, version: String) {
        self.file = file
        self.host = host
        self.version = version
        load = MediaLoad.of("\(host)\u{0}\(file.path)\u{0}\(version)")
    }

    private var name: String { (file.path as NSString).lastPathComponent }

    var body: some View {
        let m = Theme.Agent.media
        Group {
            switch file.kind {
            case "image":
                if let img = load.image {
                    Image(nsImage: img)
                        .resizable()
                        .scaledToFit()
                        .frame(maxWidth: .infinity, maxHeight: m.number("image_max_h"), alignment: .leading)
                        .clipShape(RoundedRectangle(cornerRadius: m.number("radius"), style: .continuous))
                        .onTapGesture { open() }
                        .help("\(name): click to open it")
                        .accessibilityLabel("Image \(name)")
                } else {
                    chip("photo")
                }
            case "video":
                if let p = load.avPlayer() {
                    PlayerView(player: p)
                        .frame(height: m.number("video_h"))
                        .clipShape(RoundedRectangle(cornerRadius: m.number("radius"), style: .continuous))
                        .accessibilityLabel("Video \(name)")
                } else {
                    chip("film")
                }
            case "audio":
                HStack(spacing: Theme.space("m")) {
                    RoundButton(
                        symbol: load.playing ? "pause.fill" : "play.fill", enabled: load.url != nil,
                        label: load.playing ? "Pause \(name)" : "Play \(name)"
                    ) { load.toggle() }
                    Image(systemName: "waveform").foregroundStyle(Theme.Text.secondary.color)
                    Text(name).font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.primary.color).lineLimit(1)
                    Spacer(minLength: 0)
                }
                .padding(Theme.space("m"))
                .background(RoundedRectangle(cornerRadius: m.number("radius"), style: .continuous).fill(Theme.Panel.inner.color("fill")))
            default:
                chip("doc")
            }
        }
        .onAppear { load.start(file, host: host, version: version, source: source) }
    }

    /// A file shown by name: a click opens it in its own app.
    private func chip(_ symbol: String) -> some View {
        Button { open() } label: {
            HStack(spacing: Theme.space("s")) {
                Image(systemName: symbol)
                Text(name).lineLimit(1).truncationMode(.middle)
                if load.failed != nil { Text("unavailable").foregroundStyle(Theme.Text.secondary.color) }
            }
            .font(Theme.Fonts.small.font)
            .foregroundStyle(Theme.Text.primary.color)
            .padding(.horizontal, Theme.space("l"))
            .padding(.vertical, Theme.space("s"))
            .background(Capsule().fill(Theme.Control.pill.color("fill")))
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Open \(name)")
    }

    private func open() {
        if let u = load.url { NSWorkspace.shared.open(u) }
    }
}

/// AVKit's player view, inline controls.
struct PlayerView: NSViewRepresentable {
    let player: AVPlayer

    func makeNSView(context: Context) -> AVPlayerView {
        let v = AVPlayerView()
        v.player = player
        v.controlsStyle = .inline
        v.videoGravity = .resizeAspect
        return v
    }

    func updateNSView(_ v: AVPlayerView, context: Context) {
        if v.player !== player { v.player = player }
    }
}

/// The images an agent wrote into its prose as markdown (`![alt](src)`): the
/// text without them, and each as a file to show, a relative path resolved
/// against the session's folder.
func extractMarkdownImages(_ text: String, cwd: String?) -> (text: String, images: [SharedFile]) {
    guard text.contains("![") else { return (text, []) }
    let re = /!\[[^\]]*\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)/
    var images: [SharedFile] = []
    for m in text.matches(of: re) {
        var src = String(m.output.1)
        if src.hasPrefix("data:") { continue }
        if !src.contains("://"), !src.hasPrefix("/"), let cwd, !cwd.isEmpty {
            src = (cwd as NSString).appendingPathComponent(src)
        }
        images.append(SharedFile(path: src.contains("://") ? src : (src as NSString).standardizingPath, kind: "image"))
    }
    let stripped = text.replacing(re, with: "").trimmingCharacters(in: .whitespacesAndNewlines)
    return (stripped, images)
}

extension Notification.Name {
    /// A shown file finished loading (and so took its full height).
    static let lassoMediaLoaded = Notification.Name("lassoMediaLoaded")
}
