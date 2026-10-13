import CryptoKit
import Foundation

/// Where a native kind's bytes live on this Mac while its window shows them
/// (PLAN 26): `~/Library/Caches/Lasso/Files/<sha(host, path)>/<mtime>-<size>.<ext>`.
/// AVFoundation, PDFKit and QuickLook all want a file, and a video is never
/// held in memory. Each version is its own name, so a viewer still reading
/// the old one is not pulled from under; the old one goes once the new one
/// is in, and the whole folder when the window closes.
struct FileCache: Sendable {
    let root: URL

    static var standard: FileCache {
        let caches = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask).first
            ?? FileManager.default.temporaryDirectory
        return FileCache(root: caches.appendingPathComponent("Lasso/Files", isDirectory: true))
    }

    /// One file's folder: named by a digest, so no path from another machine
    /// becomes a path on this one.
    func folder(host: String, path: String) -> URL {
        let digest = SHA256.hash(data: Data(FileHost.key(host: host, path: path).utf8))
        return root.appendingPathComponent(digest.map { String(format: "%02x", $0) }.joined(), isDirectory: true)
    }

    /// The name one version of the file is kept under; the extension stays,
    /// so the viewers know what it is.
    func version(host: String, path: String, mtime: Int64, size: Int64) -> URL {
        let ext = (path as NSString).pathExtension
        let name = "\(mtime)-\(size)" + (ext.isEmpty ? "" : "." + ext)
        return folder(host: host, path: path).appendingPathComponent(name)
    }

    /// Every other version of the file goes.
    func keepOnly(_ file: URL) {
        let dir = file.deletingLastPathComponent()
        let fm = FileManager.default
        for f in (try? fm.contentsOfDirectory(at: dir, includingPropertiesForKeys: nil)) ?? [] where f.lastPathComponent != file.lastPathComponent {
            try? fm.removeItem(at: f)
        }
    }

    /// The window closed: its folder goes.
    func remove(host: String, path: String) {
        try? FileManager.default.removeItem(at: folder(host: host, path: path))
    }
}
