import Combine
import Foundation

/// The default bot (brief B.5): which lasso bot this app opens first, and
/// making one when there is none. The choice is lasso's shared ui_state
/// (`default_bot`, S1) so the web client and this app agree; a lasso without
/// it keeps the choice on this Mac and says so.
@MainActor
public final class BotSetup: ObservableObject {
    @Published public private(set) var status: String?
    @Published public private(set) var working = false

    public let fleet: FleetModel
    let client: LassoClient?
    static let localKey = "defaultBot"

    public init(fleet: FleetModel, client: LassoClient?) {
        self.fleet = fleet
        self.client = client
    }

    /// Whether this lasso keeps the choice in shared state.
    public var shared: Bool { fleet.uiState.default_bot != nil }

    public var chosenName: String? {
        let n = shared ? fleet.uiState.default_bot : UserDefaults.standard.string(forKey: Self.localKey)
        return (n?.isEmpty ?? true) ? nil : n
    }

    public var defaultBot: BotView? { chosenName.flatMap { n in fleet.bots.first { $0.name == n } } }

    struct Choose: Encodable {
        var client_id = "lasso-desktop"
        var default_bot: String
    }

    public func choose(_ name: String) async {
        if shared, let client {
            do {
                let s: UIStateSlice = try await client.post("/api/ui-state", json: Choose(default_bot: name))
                fleet.apply(uiState: s)
                status = "\(name) is the default bot"
            } catch {
                status = "Could not save the choice: \(error)"
            }
        } else {
            UserDefaults.standard.set(name, forKey: Self.localKey)
            objectWillChange.send()
            status = "\(name) is the default bot (saved on this Mac: this lasso cannot share the choice)"
        }
    }

    struct Create: Encodable {
        var name: String
        var runtime = "omp"
        var start = true
        var instructions: String
    }

    struct Created: Decodable {
        var bot: BotView?
        var error: String?
    }

    static let instructions = """
        # Desktop bot

        You are a lasso bot that the user reaches from Lasso, the macOS app for their lasso fleet: a \
        small rail on their screen edge opens a chat with you.

        Keep replies short and lead with the answer; they read you in a 500-point panel.
        """

    /// Create an omp bot named "desktop" (or the first free "desktop-N"),
    /// started, and make it the default. Only from the user's click.
    public func createDefault() async {
        guard let client else { return }
        working = true
        defer { working = false }
        let taken = Set(fleet.bots.map(\.name))
        var name = "desktop"
        var n = 2
        while taken.contains(name) {
            name = "desktop-\(n)"
            n += 1
        }
        do {
            let r: Created = try await client.post("/api/bots", json: Create(name: name, instructions: Self.instructions))
            if let bot = r.bot {
                fleet.apply(bots: BotsPayload(bots: fleet.bots + [bot]))
            }
            await choose(name)
            // A 200 can still carry an error: the row exists, its folder or start failed.
            if let e = r.error { status = "\(name) was created but did not start: \(e)" }
        } catch {
            status = "Could not create the bot: \(error)"
        }
    }
}
