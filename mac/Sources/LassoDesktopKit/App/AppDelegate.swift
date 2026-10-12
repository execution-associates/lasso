import AppKit
import os

let log = Logger(subsystem: "com.orangecountyai.Lasso", category: "app")

/// Starts either the app (the rail on the screen edge) or, with --parity,
/// the conformance harness, which exits when it is done.
@MainActor
public final class AppDelegate: NSObject, NSApplicationDelegate {
    private let arguments: [String]
    private var sync: FleetSync?
    private var coordinator: AppCoordinator?

    public init(arguments: [String]) {
        self.arguments = arguments
    }

    private func value(after flag: String) -> String? {
        guard let i = arguments.firstIndex(of: flag), i + 1 < arguments.count else { return nil }
        return arguments[i + 1]
    }

    /// The Spec directory: --spec, else the bundle's Resources/Spec.
    private var specDir: URL {
        if let s = value(after: "--spec") { return URL(fileURLWithPath: (s as NSString).expandingTildeInPath) }
        return Bundle.main.resourceURL?.appendingPathComponent("Spec")
            ?? URL(fileURLWithPath: FileManager.default.currentDirectoryPath).appendingPathComponent("Spec")
    }

    public func applicationDidFinishLaunching(_ notification: Notification) {
        BundledFonts.register()
        if let states = value(after: "--parity") {
            runParity(states)
        } else {
            startApp()
        }
    }

    /// The Dock icon clicked (when the app is in the Dock): open the inbox.
    public func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        coordinator?.openInbox()
        return false
    }

    private func runParity(_ states: String) {
        let out = URL(fileURLWithPath: ((value(after: "--out") ?? "dist/parity") as NSString).expandingTildeInPath)
        let dir = specDir
        Task { @MainActor in
            do {
                let runner = try ParityRunner(
                    specDir: dir, out: out, states: states == "all" ? nil : states.split(separator: ",").map(String.init))
                exit(await runner.run())
            } catch {
                FileHandle.standardError.write(Data("parity: \(error)\n".utf8))
                exit(2)
            }
        }
    }

    private func startApp() {
        let fleet = FleetModel()
        var fixture: Fixture?
        var sync: FleetSync?
        if let name = value(after: "--fixture") {
            // A fixture instead of a live lasso: what the parity states show.
            do {
                let fx = try Fixture.load(name, from: specDir.appendingPathComponent("fixtures"))
                fleet.apply(panes: fx.server.all_panes)
                fleet.apply(bots: fx.server.bots)
                fleet.apply(uiState: fx.server.ui_state)
                fixture = fx
            } catch {
                log.error("fixture \(name, privacy: .public) failed: \(String(describing: error), privacy: .public)")
            }
        } else {
            let s = FleetSync(client: .configured(), fleet: fleet)
            s.start()
            sync = s
        }
        let app = AppCoordinator(fleet: fleet, sync: sync, fixture: fixture)
        app.start()
        coordinator = app
        self.sync = sync
    }
}
