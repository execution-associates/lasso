@preconcurrency import AVFoundation
import Combine
import SwiftUI

/// One voice turn: capture, then transcribe, then hand the text on. Visible
/// in the rail (the record tool) and in the composer (RecordingStrip).
@MainActor
public final class RecordingModel: ObservableObject {
    public enum Phase: Equatable, Sendable {
        case idle
        case recording(handsFree: Bool)
        case transcribing
        case failed(String)
    }

    @Published public private(set) var phase: Phase = .idle
    /// 0…1, from the input level (-60…0 dBFS), refreshed at the meter rate.
    @Published public private(set) var level = 0.0
    @Published public private(set) var engineName: String

    public var onTranscript: (Transcript) -> Void = { _ in }
    public var onError: (String) -> Void = { _ in }

    private let permissions: Permissions
    private let makeTranscriber: () -> Transcriber
    private var capture: AudioCapture?
    private var meter: Task<Void, Never>?
    private var pressedAt: Date?

    public init(permissions: Permissions, engineName: String, transcriber: @escaping () -> Transcriber) {
        self.permissions = permissions
        self.engineName = engineName
        makeTranscriber = transcriber
    }

    /// A fixed state for the parity harness: no microphone involved.
    public func showForParity(phase: Phase, level: Double) {
        self.phase = phase
        self.level = level
    }

    public func setEngine(_ name: String) { engineName = name }

    /// A recording model that never records: the chat without voice wired.
    static let inert = RecordingModel(permissions: FixedPermissions([:]), engineName: "") { ParakeetTranscriber() }

    public var isRecording: Bool {
        if case .recording = phase { return true }
        return false
    }

    /// V down: start (push-to-talk).
    public func keyDown() {
        guard !isRecording else { return }
        pressedAt = Date()
        start(handsFree: false)
    }

    /// V up: a short tap keeps listening hands-free until speech ends; a
    /// hold ends the turn now.
    public func keyUp() {
        guard case .recording(let handsFree) = phase, !handsFree, let at = pressedAt else { return }
        if Date().timeIntervalSince(at) * 1000 < Theme.Voice.pttSplit.seconds {
            phase = .recording(handsFree: true)
        } else {
            finish()
        }
    }

    /// The record button: toggle a hands-free turn.
    public func toggle() {
        if isRecording { finish() } else { start(handsFree: true) }
    }

    public func cancel() {
        _ = capture?.stop()
        capture = nil
        meter?.cancel()
        phase = .idle
        level = 0
    }

    private func start(handsFree: Bool) {
        Task {
            guard await permissions.requestMicrophone() else {
                fail(PermissionError.microphone.description)
                return
            }
            let c = AudioCapture()
            do {
                try c.start()
            } catch {
                fail("could not start the microphone: \(error)")
                return
            }
            capture = c
            phase = .recording(handsFree: handsFree)
            meter?.cancel()
            meter = Task { [weak self] in
                let hz = Theme.Recording.meterHz.seconds
                while !Task.isCancelled {
                    try? await Task.sleep(nanoseconds: UInt64(1e9 / hz))
                    guard let self, let snap = self.capture?.snapshot() else { return }
                    self.level = max(0, min(1, (snap.level + 60) / 60))
                    if case .recording(let hf) = self.phase, (hf && snap.ended) || snap.full {
                        self.finish()
                        return
                    }
                }
            }
        }
    }

    private func finish() {
        meter?.cancel()
        guard let c = capture else { return }
        capture = nil
        let samples = c.stop()
        level = 0
        // Less than the VAD's minimum speech is a stray tap.
        guard Double(samples.count) / Double(Theme.Voice.sampleRate.int) * 1000 >= Theme.Voice.vadMinSpeech.seconds else {
            phase = .idle
            return
        }
        phase = .transcribing
        let wav = WAV.encode(samples, sampleRate: Theme.Voice.sampleRate.int)
        let t = makeTranscriber()
        Task {
            do {
                let tr = try await t.transcribe(wav: wav, language: nil)
                phase = .idle
                let text = tr.text.trimmingCharacters(in: .whitespacesAndNewlines)
                if text.isEmpty { onError("Nothing was heard") } else { onTranscript(tr) }
            } catch {
                fail("\(t.displayName): \(error)")
            }
        }
    }

    private func fail(_ message: String) {
        phase = .failed(message)
        onError(message)
    }
}

/// The recording state inside the composer: a level meter, what is
/// happening, and which engine will hear it.
struct RecordingStrip: View {
    @ObservedObject var model: RecordingModel

    var body: some View {
        switch model.phase {
        case .recording, .transcribing:
            HStack(spacing: 8) {
                Circle()
                    .strokeBorder(Theme.Recording.ring.color, lineWidth: Theme.Rail.ringStroke.pt)
                    .frame(width: Theme.Rail.glyphSize.pt, height: Theme.Rail.glyphSize.pt)
                    .parityAnchor("recording.ring")
                if model.phase == .transcribing {
                    Text("Transcribing with \(model.engineName)…")
                } else {
                    Meter(level: model.level)
                    Text("Listening · \(model.engineName)")
                }
                Spacer(minLength: 0)
            }
            .font(Theme.Fonts.small.font)
            .foregroundStyle(Theme.Text.secondary.color)
            .padding(.horizontal, 2)
            .frame(maxHeight: .infinity)
            .accessibilityElement(children: .combine)
            .accessibilityLabel(
                model.phase == .transcribing ? "Transcribing with \(model.engineName)" : "Recording with \(model.engineName)")
        default:
            EmptyView()
        }
    }
}

/// Bars for the input level.
struct Meter: View {
    let level: Double

    var body: some View {
        HStack(alignment: .center, spacing: 2) {
            ForEach(0..<12, id: \.self) { i in
                let on = Double(i) / 12 < level
                RoundedRectangle(cornerRadius: 1)
                    .fill(on ? Theme.Recording.meterFill.color : Theme.Row.hover.color)
                    .frame(width: 3, height: 4 + CGFloat(i % 4) * 3)
            }
        }
        .parityAnchor("recording.meter")
        .accessibilityHidden(true)
    }
}
