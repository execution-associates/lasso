import AppKit
import Combine
import Foundation

/// What GET /api/transcribe says (S2).
public struct TranscribeStatus: Codable, Sendable, Equatable {
    public var available: Bool
    public var provider: String?
    public var model: String?
    public var reason: String?
}

/// lasso's own setup, not a tour: which lasso, which assistant, which voice
/// engine, and the three permissions.
@MainActor
public final class OnboardingModel: ObservableObject {
    @Published public var engine: VoiceEngine
    @Published public var keyDraft = ""
    @Published public private(set) var transcribe: TranscribeStatus?
    @Published public private(set) var transcribeNote: String?
    @Published public private(set) var notice: String?
    /// Bumped to re-read the permission states after a request.
    @Published public private(set) var tick = 0

    public let permissions: Permissions
    public let setup: AssistantSetup
    public let baseURL: String
    public var version: () -> VersionInfo?
    public var usableHosts: () -> (usable: Int, total: Int)?
    public var onDone: () -> Void = {}
    public var onEngineChange: (VoiceEngine) -> Void = { _ in }
    let client: LassoClient?

    public static let doneKey = "onboarded"
    public static var isDone: Bool { UserDefaults.standard.bool(forKey: doneKey) }

    public init(
        permissions: Permissions, setup: AssistantSetup, client: LassoClient?, baseURL: String, engine: VoiceEngine,
        version: @escaping () -> VersionInfo? = { nil }, usableHosts: @escaping () -> (usable: Int, total: Int)? = { nil }
    ) {
        self.permissions = permissions
        self.setup = setup
        self.client = client
        self.baseURL = baseURL
        self.engine = engine
        self.version = version
        self.usableHosts = usableHosts
    }

    public func show(transcribe: TranscribeStatus?, note: String? = nil) {
        self.transcribe = transcribe
        transcribeNote = note
    }

    public func refresh() async {
        tick += 1
        guard let client else { return }
        do {
            transcribe = try await client.get("/api/transcribe", as: TranscribeStatus.self)
            transcribeNote = nil
        } catch LassoError.http(404, _) {
            transcribe = nil
            transcribeNote = "This lasso has no transcription route yet; it arrives with the next lasso release."
        } catch {
            transcribe = nil
            transcribeNote = "Could not ask lasso about transcription: \(error)"
        }
    }

    public func select(_ e: VoiceEngine) {
        engine = e
        keyDraft = ""
        UserDefaults.standard.set(e.rawValue, forKey: VoiceEngine.defaultsKey)
        onEngineChange(e)
    }

    public var keyAccount: Keychain.Account? {
        switch engine {
        case .groq: .groq
        case .openai: .openai
        default: nil
        }
    }

    /// The key goes straight to the Keychain; the field is cleared.
    public func saveKey() {
        guard let account = keyAccount else { return }
        let k = keyDraft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !k.isEmpty else { return }
        notice = Keychain.write(account, k) ? "\(engine.displayName) key saved in the Keychain" : "Could not save the key"
        keyDraft = ""
        tick += 1
    }

    public func removeKey() {
        guard let account = keyAccount else { return }
        Keychain.delete(account)
        notice = "\(engine.displayName) key removed"
        tick += 1
    }

    public func allowMicrophone() {
        Task {
            _ = await permissions.requestMicrophone()
            tick += 1
        }
    }

    public func allowScreenRecording() {
        permissions.requestScreenRecording()
        tick += 1
    }

    public func finish() {
        UserDefaults.standard.set(true, forKey: Self.doneKey)
        onDone()
    }
}
