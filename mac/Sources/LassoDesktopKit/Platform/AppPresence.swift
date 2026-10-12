import AppKit
import Combine

/// Where the app shows itself besides the rail: a Dock icon (with the menu
/// bar an app in the Dock gets) and a menu-bar icon carrying the inbox's
/// count. Both are the user's (Settings › Info), off by default: the rail is
/// the app's own presence. UserDefaults `showInDock`, `showInMenuBar`.
@MainActor
public final class AppPresence: NSObject {
    static let dockKey = "showInDock"
    static let menuBarKey = "showInMenuBar"

    public static var inDock: Bool { UserDefaults.standard.bool(forKey: dockKey) }
    public static var inMenuBar: Bool { UserDefaults.standard.bool(forKey: menuBarKey) }

    private let fleet: FleetModel
    var openInbox: () -> Void = {}
    var newAgent: () -> Void = {}
    var openSettings: () -> Void = {}
    private var item: NSStatusItem?
    private var watch: AnyCancellable?

    init(fleet: FleetModel) {
        self.fleet = fleet
        super.init()
    }

    /// Apply both choices, as stored: at launch, and after either changes.
    func apply() {
        NSApp.mainMenu = mainMenu()
        NSApp.setActivationPolicy(Self.inDock ? .regular : .accessory)
        if Self.inMenuBar { showItem() } else { hideItem() }
    }

    public static func set(dock: Bool) { UserDefaults.standard.set(dock, forKey: dockKey) }
    public static func set(menuBar: Bool) { UserDefaults.standard.set(menuBar, forKey: menuBarKey) }

    // MARK: the menu-bar icon

    /// The EXA monogram as a template (Resources/Brand/monogram-template*,
    /// cut from the app icon), so the menu bar tints it for light and dark.
    /// Its letters fill the whole square, so it is drawn at the count's
    /// cap height (icon.menubar), not a full 18 pt icon.
    static let monogram: NSImage? = {
        guard let dir = Bundle.main.resourceURL?.appendingPathComponent("Brand") else { return nil }
        let side = Theme.icon.number("menubar")
        let image = NSImage(size: NSSize(width: side, height: side))
        for name in ["monogram-template.png", "monogram-template@2x.png", "monogram-template@3x.png"] {
            if let rep = NSImageRep(contentsOf: dir.appendingPathComponent(name)) {
                rep.size = image.size
                image.addRepresentation(rep)
            }
        }
        guard !image.representations.isEmpty else { return nil }
        image.isTemplate = true
        image.accessibilityDescription = "Lasso"
        return image
    }()

    private func showItem() {
        guard item == nil else { return }
        let i = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        i.button?.image = Self.monogram
        i.button?.imagePosition = .imageLeading
        i.menu = statusMenu()
        item = i
        refreshCount()
        // The count follows the inbox; objectWillChange fires before the
        // change lands, so read it on the next turn.
        watch = fleet.objectWillChange.sink { [weak self] _ in
            DispatchQueue.main.async { self?.refreshCount() }
        }
    }

    private func hideItem() {
        watch = nil
        if let i = item { NSStatusBar.system.removeStatusItem(i) }
        item = nil
    }

    private func refreshCount() {
        let n = fleet.badgeCount
        // The count in the menu bar's own size, medium, with steady-width
        // digits: it sits beside the monogram as a pair, not a caption.
        let text = n > 0 ? " \(n > 99 ? "99+" : String(n))" : ""
        item?.button?.attributedTitle = NSAttributedString(
            string: text,
            attributes: [.font: NSFont.monospacedDigitSystemFont(ofSize: Theme.icon.number("menubar_text"), weight: .medium)])
        item?.button?.setAccessibilityLabel(n > 0 ? "Lasso, \(n) in the inbox" : "Lasso")
    }

    private func statusMenu() -> NSMenu {
        let m = NSMenu()
        m.addItem(action("Open Inbox", #selector(doOpenInbox)))
        m.addItem(action("New Agent…", #selector(doNewAgent)))
        m.addItem(action("Settings…", #selector(doOpenSettings)))
        m.addItem(.separator())
        m.addItem(NSMenuItem(title: "Quit Lasso", action: #selector(NSApplication.terminate(_:)), keyEquivalent: ""))
        return m
    }

    // MARK: the menu bar a Dock app gets (and the key equivalents text fields use)

    private func mainMenu() -> NSMenu {
        let main = NSMenu()
        let app = NSMenu(title: "Lasso")
        app.addItem(action("Settings…", #selector(doOpenSettings), key: ","))
        app.addItem(.separator())
        app.addItem(NSMenuItem(title: "Hide Lasso", action: #selector(NSApplication.hide(_:)), keyEquivalent: "h"))
        app.addItem(NSMenuItem(title: "Quit Lasso", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q"))
        let edit = NSMenu(title: "Edit")
        for (title, sel, key) in [
            ("Undo", Selector(("undo:")), "z"), ("Redo", Selector(("redo:")), "Z"), ("Cut", #selector(NSText.cut(_:)), "x"),
            ("Copy", #selector(NSText.copy(_:)), "c"), ("Paste", #selector(NSText.paste(_:)), "v"),
            ("Select All", #selector(NSText.selectAll(_:)), "a"),
        ] {
            edit.addItem(NSMenuItem(title: title, action: sel, keyEquivalent: key))
        }
        for menu in [app, edit] {
            let holder = NSMenuItem()
            holder.submenu = menu
            main.addItem(holder)
        }
        return main
    }

    private func action(_ title: String, _ sel: Selector, key: String = "") -> NSMenuItem {
        let i = NSMenuItem(title: title, action: sel, keyEquivalent: key)
        i.target = self
        return i
    }

    @objc private func doOpenInbox() { openInbox() }
    @objc private func doNewAgent() { newAgent() }
    @objc private func doOpenSettings() { openSettings() }
}
