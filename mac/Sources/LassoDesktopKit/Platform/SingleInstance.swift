import Foundation

/// One live app per user. Two launchers can start it at login: the app's own
/// login item (SMAppService) and `Scripts/agent.sh`'s dev LaunchAgent, which
/// stays behind when a release build is installed over a dev one. Each copy
/// would draw its own rail, so the second one to start exits.
///
/// The claim is an `flock` on a file, not a scan of running apps: the kernel
/// drops it when the holder dies (even on SIGKILL), and the parity harness,
/// which runs the same bundle, never takes it.
public final class SingleInstance {
    private let fd: Int32

    private init(fd: Int32) { self.fd = fd }
    deinit { if fd >= 0 { close(fd) } }

    /// The default lock: Application Support/com.orangecountyai.Lasso/app.lock.
    public static var defaultPath: URL {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first
            ?? URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent("Library/Application Support")
        return base.appendingPathComponent("com.orangecountyai.Lasso").appendingPathComponent("app.lock")
    }

    /// The claim, or nil when another live instance holds it. A lock that
    /// cannot be opened at all is not a reason to refuse to start, so that
    /// answers a claim too (one holding nothing).
    public static func claim(at path: URL = defaultPath) -> SingleInstance? {
        try? FileManager.default.createDirectory(at: path.deletingLastPathComponent(), withIntermediateDirectories: true)
        let fd = open(path.path, O_RDWR | O_CREAT | O_CLOEXEC, 0o600)
        guard fd >= 0 else { return SingleInstance(fd: -1) }
        if flock(fd, LOCK_EX | LOCK_NB) != 0 {
            let busy = errno == EWOULDBLOCK
            close(fd)
            return busy ? nil : SingleInstance(fd: -1)
        }
        return SingleInstance(fd: fd)
    }
}
