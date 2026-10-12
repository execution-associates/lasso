import AppKit
import UserNotifications

/// What a macOS notification is about: an inbox entry (an agent's pane, by
/// host and pane id, with the mark it had) or one of lasso's notices.
public enum NoteTarget: Equatable, Sendable {
    case entry(host: String, pane: String, mark: String)
    case notice(String)

    /// The notification's userInfo: plain strings (a pane key holds a NUL).
    var userInfo: [String: String] {
        switch self {
        case .entry(let host, let pane, let mark): ["host": host, "pane": pane, "mark": mark]
        case .notice(let id): ["notice": id]
        }
    }

    init?(userInfo: [AnyHashable: Any]) {
        if let id = userInfo["notice"] as? String {
            self = .notice(id)
        } else if let host = userInfo["host"] as? String, let pane = userInfo["pane"] as? String,
                  let mark = userInfo["mark"] as? String
        {
            self = .entry(host: host, pane: pane, mark: mark)
        } else {
            return nil
        }
    }
}

/// What the user did with a notification.
public enum NoteAction: Equatable, Sendable {
    /// Clicked it: open the inbox on it.
    case open
    case acknowledge
    /// Reply, with what was typed.
    case reply(String)
}

/// One macOS notification. `id` is the agent's (or the bot's, or the
/// notice's), so a newer one replaces it; `thread` groups an agent's.
public struct UserNote: Equatable, Sendable {
    public enum Category: String, Sendable {
        /// Something waiting on you: Acknowledge only (an answer is the card's).
        case waiting = "lasso.waiting"
        /// A finished turn: Acknowledge and Reply.
        case finished = "lasso.finished"
        case notice = "lasso.notice"
    }

    public var id: String
    public var thread: String
    public var title: String
    public var subtitle: String
    public var body: String
    public var category: Category
    public var target: NoteTarget
    /// A picture to show with it (a bot's), as a PNG file.
    public var image: URL?

    public init(
        id: String, thread: String, title: String, subtitle: String, body: String, category: Category, target: NoteTarget,
        image: URL? = nil
    ) {
        self.id = id
        self.thread = thread
        self.title = title
        self.subtitle = subtitle
        self.body = body
        self.category = category
        self.target = target
        self.image = image
    }
}

/// macOS's notifications, behind a protocol so the parity harness and the
/// tests never post a real one (`SystemNotifier` is the live one).
@MainActor
public protocol Notifier: AnyObject {
    /// Called when the user clicks a notification or one of its actions.
    var onAction: (NoteTarget, NoteAction) -> Void { get set }
    func authorization() async -> PermissionState
    /// Ask macOS (its one-time prompt); the state that results.
    func requestAuthorization() async -> PermissionState
    func post(_ note: UserNote)
    func remove(_ ids: [String])
    /// The ones still in Notification Center: their ids and targets.
    func delivered() async -> [(id: String, target: NoteTarget)]
    /// System Settings › Notifications, on Lasso.
    func openSettings()
}

/// The live notifier: UNUserNotificationCenter. It works only from the
/// signed app bundle (`available`); the center raises for a bare binary.
@MainActor
public final class SystemNotifier: NSObject, Notifier, UNUserNotificationCenterDelegate {
    public var onAction: (NoteTarget, NoteAction) -> Void = { _, _ in }
    private let center = UNUserNotificationCenter.current()

    /// A bundled app with an identifier: the only process macOS lets post.
    public static var available: Bool {
        Bundle.main.bundleIdentifier != nil && Bundle.main.bundleURL.pathExtension == "app"
    }

    override public init() {
        super.init()
        center.delegate = self
        let ack = UNNotificationAction(identifier: Self.acknowledgeID, title: "Acknowledge", options: [])
        let reply = UNTextInputNotificationAction(
            identifier: Self.replyID, title: "Reply", options: [], textInputButtonTitle: "Send", textInputPlaceholder: "Reply")
        center.setNotificationCategories([
            UNNotificationCategory(identifier: UserNote.Category.waiting.rawValue, actions: [ack], intentIdentifiers: [], options: []),
            UNNotificationCategory(identifier: UserNote.Category.finished.rawValue, actions: [reply, ack], intentIdentifiers: [], options: []),
            UNNotificationCategory(identifier: UserNote.Category.notice.rawValue, actions: [ack], intentIdentifiers: [], options: []),
        ])
    }

    nonisolated static let acknowledgeID = "acknowledge"
    nonisolated static let replyID = "reply"

    public func authorization() async -> PermissionState {
        await withCheckedContinuation { c in
            center.getNotificationSettings { @Sendable s in c.resume(returning: Self.state(s.authorizationStatus)) }
        }
    }

    public func requestAuthorization() async -> PermissionState {
        // Banners only: the app plays its own arrival sound (PLAN 17).
        let granted: Bool = await withCheckedContinuation { c in
            center.requestAuthorization(options: [.alert]) { @Sendable ok, _ in c.resume(returning: ok) }
        }
        return granted ? .granted : .denied
    }

    nonisolated static func state(_ s: UNAuthorizationStatus) -> PermissionState {
        switch s {
        case .authorized, .provisional: .granted
        case .denied: .denied
        default: .notDetermined
        }
    }

    public func post(_ note: UserNote) {
        let c = UNMutableNotificationContent()
        c.title = note.title
        c.subtitle = note.subtitle
        c.body = note.body
        c.threadIdentifier = note.thread
        c.categoryIdentifier = note.category.rawValue
        c.userInfo = note.target.userInfo
        // Silent: the arrival sound is the app's, once per burst.
        c.sound = nil
        if let url = note.image, let a = try? UNNotificationAttachment(identifier: "picture", url: url, options: nil) {
            c.attachments = [a]
        }
        center.add(UNNotificationRequest(identifier: note.id, content: c, trigger: nil)) { @Sendable error in
            if let error { log.error("notification failed: \(String(describing: error), privacy: .public)") }
        }
    }

    public func remove(_ ids: [String]) {
        guard !ids.isEmpty else { return }
        center.removeDeliveredNotifications(withIdentifiers: ids)
    }

    public func delivered() async -> [(id: String, target: NoteTarget)] {
        let pairs: [(String, [String: String])] = await withCheckedContinuation { c in
            center.getDeliveredNotifications { @Sendable list in
                c.resume(returning: list.map { n in
                    (n.request.identifier, n.request.content.userInfo.reduce(into: [String: String]()) { d, kv in
                        if let k = kv.key as? String, let v = kv.value as? String { d[k] = v }
                    })
                })
            }
        }
        return pairs.compactMap { id, info in NoteTarget(userInfo: info).map { (id, $0) } }
    }

    public func openSettings() {
        let id = Bundle.main.bundleIdentifier ?? "com.orangecountyai.Lasso"
        if let url = URL(string: "x-apple.systempreferences:com.apple.Notifications-Settings.extension?id=\(id)") {
            NSWorkspace.shared.open(url)
        }
    }

    // MARK: UNUserNotificationCenterDelegate

    /// A banner even while Lasso is the active app (the rail is an accessory
    /// app, rarely active, but a Dock click makes it so).
    nonisolated public func userNotificationCenter(
        _ center: UNUserNotificationCenter, willPresent notification: UNNotification,
        withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        completionHandler([.banner, .list])
    }

    nonisolated public func userNotificationCenter(
        _ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse,
        withCompletionHandler completionHandler: @escaping () -> Void
    ) {
        let target = NoteTarget(userInfo: response.notification.request.content.userInfo)
        let action: NoteAction? =
            switch response.actionIdentifier {
            case UNNotificationDefaultActionIdentifier: .open
            case Self.acknowledgeID: .acknowledge
            case Self.replyID: (response as? UNTextInputNotificationResponse).map { .reply($0.userText) }
            default: nil
            }
        completionHandler()
        guard let target, let action else { return }
        Task { @MainActor in self.onAction(target, action) }
    }
}

/// A bot's picture as a PNG file a notification can attach. macOS moves the
/// file into its own store, so each post writes a fresh one.
enum NotePicture {
    static func file(_ image: NSImage) -> URL? {
        guard let tiff = image.tiffRepresentation, let rep = NSBitmapImageRep(data: tiff),
              let png = rep.representation(using: .png, properties: [:])
        else { return nil }
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("LassoNotes", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let url = dir.appendingPathComponent("\(UUID().uuidString).png")
        return (try? png.write(to: url)) == nil ? nil : url
    }
}
