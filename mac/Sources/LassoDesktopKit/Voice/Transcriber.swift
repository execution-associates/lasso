import Foundation

/// Text from speech, and which engine made it.
public struct Transcript: Sendable, Equatable {
    public var text: String
    public var engine: String
    public var model: String

    /// What rides the bot message (S3): how it was input, by what.
    public var meta: [String: String] { ["input": "voice", "engine": engine, "model": model] }
}

/// Speech to text. One per engine (brief C); the audio goes only where the
/// selected engine needs it.
public protocol Transcriber: Sendable {
    var id: String { get }
    var displayName: String { get }
    func transcribe(wav: Data, language: String?) async throws -> Transcript
}

public enum VoiceEngine: String, CaseIterable, Sendable {
    case lasso, groq, openai, parakeet

    public static let defaultsKey = "voiceEngine"

    public static var selected: VoiceEngine {
        UserDefaults.standard.string(forKey: defaultsKey).flatMap(VoiceEngine.init(rawValue:)) ?? .lasso
    }

    public var displayName: String {
        switch self {
        case .lasso: "lasso"
        case .groq: "Groq"
        case .openai: "OpenAI"
        case .parakeet: "Parakeet (on device)"
        }
    }

    public func transcriber(client: LassoClient) -> Transcriber {
        switch self {
        case .lasso: LassoTranscriber(client: client)
        case .groq:
            OpenAICompatibleTranscriber(
                id: "groq", displayName: "Groq", base: URL(string: "https://api.groq.com/openai/v1")!,
                model: "whisper-large-v3-turbo", key: .groq)
        case .openai:
            OpenAICompatibleTranscriber(
                id: "openai", displayName: "OpenAI", base: URL(string: "https://api.openai.com/v1")!,
                model: "gpt-4o-mini-transcribe", key: .openai)
        case .parakeet: ParakeetTranscriber()
        }
    }
}

/// "The server transcribes this for me": POST /api/transcribe (S2). The
/// app holds no key; lasso picks the provider, possibly one on its own box.
public struct LassoTranscriber: Transcriber {
    public let id = "lasso"
    public let displayName = "lasso"
    let client: LassoClient

    struct Reply: Decodable {
        var text: String
        var provider: String?
        var model: String?
    }

    public func transcribe(wav: Data, language: String?) async throws -> Transcript {
        var req = client.request(
            "/api/transcribe", query: language.map { ["language": $0] } ?? [:], method: "POST", body: wav)
        req.setValue("audio/wav", forHTTPHeaderField: "Content-Type")
        req.timeoutInterval = 70
        let r: Reply = try await client.send(req)
        let model = [r.provider, r.model].compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: "/")
        return Transcript(text: r.text, engine: "lasso", model: model)
    }
}

/// Groq and OpenAI: the OpenAI audio/transcriptions API, called from the Mac
/// with the user's key from the Keychain.
public struct OpenAICompatibleTranscriber: Transcriber {
    public let id: String
    public let displayName: String
    let base: URL
    let model: String
    let key: Keychain.Account

    struct Reply: Decodable { var text: String }

    public func transcribe(wav: Data, language: String?) async throws -> Transcript {
        guard let secret = Keychain.read(key), !secret.isEmpty else { throw VoiceError.noKey(displayName) }
        let boundary = "lasso-\(UUID().uuidString)"
        var body = Data()
        func field(_ name: String, _ value: String) {
            body.append(Data("--\(boundary)\r\nContent-Disposition: form-data; name=\"\(name)\"\r\n\r\n\(value)\r\n".utf8))
        }
        field("model", model)
        field("response_format", "json")
        if let language { field("language", language) }
        body.append(
            Data(
                "--\(boundary)\r\nContent-Disposition: form-data; name=\"file\"; filename=\"audio.wav\"\r\nContent-Type: audio/wav\r\n\r\n"
                    .utf8))
        body.append(wav)
        body.append(Data("\r\n--\(boundary)--\r\n".utf8))
        var req = URLRequest(url: base.appendingPathComponent("audio/transcriptions"))
        req.httpMethod = "POST"
        req.timeoutInterval = 60
        req.setValue("Bearer \(secret)", forHTTPHeaderField: "Authorization")
        req.setValue("multipart/form-data; boundary=\(boundary)", forHTTPHeaderField: "Content-Type")
        req.httpBody = body
        let cfg = URLSessionConfiguration.ephemeral
        cfg.urlCache = nil
        let (data, resp) = try await URLSession(configuration: cfg).data(for: req)
        let code = (resp as? HTTPURLResponse)?.statusCode ?? 0
        guard (200..<300).contains(code) else {
            // A provider's error body can echo request details; report the code only.
            throw VoiceError.engine("\(displayName) answered HTTP \(code)")
        }
        return Transcript(text: try JSONDecoder().decode(Reply.self, from: data).text, engine: id, model: model)
    }
}

/// On-device Parakeet is planned, not built (PLAN 4.2, Q3).
public struct ParakeetTranscriber: Transcriber {
    public let id = "parakeet"
    public let displayName = "Parakeet"

    public func transcribe(wav: Data, language: String?) async throws -> Transcript {
        throw VoiceError.unavailable("on-device Parakeet is not built yet; pick lasso, Groq or OpenAI")
    }
}
