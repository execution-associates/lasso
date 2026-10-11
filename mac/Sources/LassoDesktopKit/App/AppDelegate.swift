import AppKit
import os

let log = Logger(subsystem: "com.orangecountyai.LassoDesktop", category: "app")

/// Starts either the app (the rail on the screen edge) or, with --parity,
/// the conformance harness, which exits when it is done.
@MainActor
public final class AppDelegate: NSObject, NSApplicationDelegate {
    private let arguments: [String]
    private var rail: RailController?
    private var hotKey: HotKey?
    private var sync: FleetSync?
    private var card: CardController?

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
        if let states = value(after: "--parity") {
            runParity(states)
        } else {
            startApp()
        }
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
        if let name = value(after: "--fixture") {
            // A fixture instead of a live lasso: what the parity states show.
            do {
                let fx = try Fixture.load(name, from: specDir.appendingPathComponent("fixtures"))
                fleet.apply(panes: fx.server.all_panes)
                fleet.apply(bots: fx.server.bots)
                fleet.apply(uiState: fx.server.ui_state)
            } catch {
                log.error("fixture \(name, privacy: .public) failed: \(String(describing: error), privacy: .public)")
            }
        } else {
            let sync = FleetSync(client: .configured(), fleet: fleet)
            sync.start()
            self.sync = sync
        }
        let actions: CardActions
        if let sync {
            let live = LiveCardActions(client: sync.client, fleet: fleet)
            live.refreshUIState = { [weak sync] in await sync?.refreshUIState() }
            actions = live
        } else {
            let fx = try? Fixture.load(value(after: "--fixture") ?? "", from: specDir.appendingPathComponent("fixtures"))
            actions = FixtureCardActions(chats: fx?.chats ?? [:], canDismiss: fx?.server.ui_state.dismissed_cards != nil)
        }
        let model = RailModel(fleet: fleet)
        let rail = RailController(model: model)
        let card = CardController(fleet: fleet, actions: actions)
        card.railFrame = { [weak rail] in rail?.panel.frame ?? .zero }
        model.onPickAgent = { [weak card] pane in card?.open(pane) }
        rail.show()
        self.rail = rail
        self.card = card
        hotKey = HotKey(token: Theme.summonKey) { [weak rail] in rail?.summon() }
        if hotKey == nil { log.error("summon hotkey could not be registered") }
    }
}
