import Foundation

/// One line per state change on stderr, which the LaunchAgent writes to
/// ~/Library/Logs/LassoDesktop.log (dev.sh logs). Events only: never text a
/// user typed or said, audio, or a key.
enum Trace {
    nonisolated(unsafe) private static let stamp: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f
    }()

    static func event(_ s: String) {
        FileHandle.standardError.write(Data("\(stamp.string(from: Date())) \(s)\n".utf8))
    }
}
