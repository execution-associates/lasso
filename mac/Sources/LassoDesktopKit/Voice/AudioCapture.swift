@preconcurrency import AVFoundation
import Foundation

/// Energy-based end-of-speech detection over 20 ms windows (Theme.Voice):
/// the first window learns the noise floor, speech is the floor plus a
/// margin, and a turn ends after a run of silence that follows enough speech.
public struct VAD: Sendable {
    public enum Event: Equatable, Sendable { case none, speechStarted, ended }

    let floorWindows: Int
    let thresholdDB: Double
    let endSilenceWindows: Int
    let minSpeechWindows: Int

    private var seen = 0
    private var floorSum = 0.0
    private var floor = -90.0
    private var speech = 0
    private var silence = 0
    public private(set) var speaking = false

    public init(windowMs: Double = 20) {
        floorWindows = max(1, Int(Theme.Voice.vadFloorWindow.seconds / windowMs))
        thresholdDB = Theme.Voice.vadThreshold.seconds
        endSilenceWindows = Int(Theme.Voice.vadEndSilence.seconds / windowMs)
        minSpeechWindows = Int(Theme.Voice.vadMinSpeech.seconds / windowMs)
    }

    /// Feed one window's level in dBFS.
    public mutating func feed(_ db: Double) -> Event {
        seen += 1
        if seen <= floorWindows {
            floorSum += db
            floor = floorSum / Double(seen)
            return .none
        }
        if db > floor + thresholdDB {
            speech += 1
            silence = 0
            if !speaking, speech >= 3 {
                speaking = true
                return .speechStarted
            }
        } else if speech >= minSpeechWindows {
            silence += 1
            if silence >= endSilenceWindows { return .ended }
        }
        return .none
    }
}

/// dBFS of a window of samples (RMS), floored at -90.
public func levelDB(_ s: ArraySlice<Float>) -> Double {
    guard !s.isEmpty else { return -90 }
    var sum: Float = 0
    for x in s { sum += x * x }
    let rms = (sum / Float(s.count)).squareRoot()
    return rms > 0 ? max(-90, 20 * log10(Double(rms))) : -90
}

/// 16 kHz mono 16-bit PCM WAV, in memory. Audio never touches the disk.
public enum WAV {
    public static func encode(_ samples: [Float], sampleRate: Int) -> Data {
        var d = Data()
        func u32(_ v: UInt32) { withUnsafeBytes(of: v.littleEndian) { d.append(contentsOf: $0) } }
        func u16(_ v: UInt16) { withUnsafeBytes(of: v.littleEndian) { d.append(contentsOf: $0) } }
        let bytes = UInt32(samples.count * 2)
        d.append(contentsOf: Array("RIFF".utf8))
        u32(36 + bytes)
        d.append(contentsOf: Array("WAVE".utf8))
        d.append(contentsOf: Array("fmt ".utf8))
        u32(16)
        u16(1)  // PCM
        u16(1)  // mono
        u32(UInt32(sampleRate))
        u32(UInt32(sampleRate * 2))
        u16(2)
        u16(16)
        d.append(contentsOf: Array("data".utf8))
        u32(bytes)
        d.reserveCapacity(d.count + samples.count * 2)
        for s in samples {
            let v = Int16(max(-1, min(1, s)) * Float(Int16.max))
            u16(UInt16(bitPattern: v))
        }
        return d
    }
}

/// The microphone, through an AVAudioEngine input tap, converted to 16 kHz
/// mono. Samples are kept in memory, capped at the maximum turn.
public final class AudioCapture: @unchecked Sendable {
    public struct Snapshot: Sendable {
        public var level: Double
        public var ended: Bool
        public var full: Bool
    }

    private let engine = AVAudioEngine()
    private let lock = NSLock()
    private var samples: [Float] = []
    private var pending: [Float] = []
    private var vad = VAD()
    private var level = -90.0
    private var ended = false
    private let rate = Double(Theme.Voice.sampleRate.int)
    private let maxSamples = Int(Theme.Voice.maxTurn.seconds * Double(Theme.Voice.sampleRate.int))

    public init() {}

    public func start() throws {
        let input = engine.inputNode
        let inFormat = input.outputFormat(forBus: 0)
        guard let outFormat = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: rate, channels: 1, interleaved: false),
              let converter = AVAudioConverter(from: inFormat, to: outFormat)
        else { throw VoiceError.audioFormat }
        let window = Int(rate / 50)  // 20 ms
        input.installTap(onBus: 0, bufferSize: 2048, format: inFormat) { [weak self] buffer, _ in
            guard let self else { return }
            let ratio = outFormat.sampleRate / inFormat.sampleRate
            let cap = AVAudioFrameCount(Double(buffer.frameLength) * ratio + 32)
            guard let out = AVAudioPCMBuffer(pcmFormat: outFormat, frameCapacity: cap) else { return }
            var fed = false
            var err: NSError?
            converter.convert(to: out, error: &err) { _, status in
                if fed {
                    status.pointee = .noDataNow
                    return nil
                }
                fed = true
                status.pointee = .haveData
                return buffer
            }
            guard err == nil, let ch = out.floatChannelData?[0] else { return }
            let chunk = Array(UnsafeBufferPointer(start: ch, count: Int(out.frameLength)))
            self.lock.lock()
            defer { self.lock.unlock() }
            if self.samples.count < self.maxSamples { self.samples.append(contentsOf: chunk.prefix(self.maxSamples - self.samples.count)) }
            self.pending.append(contentsOf: chunk)
            while self.pending.count >= window {
                let w = self.pending.prefix(window)
                let db = levelDB(w)
                self.level = db
                if self.vad.feed(db) == .ended { self.ended = true }
                self.pending.removeFirst(window)
            }
        }
        engine.prepare()
        try engine.start()
    }

    /// Stop and hand back everything captured.
    public func stop() -> [Float] {
        engine.inputNode.removeTap(onBus: 0)
        engine.stop()
        lock.lock()
        defer {
            samples = []
            pending = []
            lock.unlock()
        }
        return samples
    }

    public func snapshot() -> Snapshot {
        lock.lock()
        defer { lock.unlock() }
        return Snapshot(level: level, ended: ended, full: samples.count >= maxSamples)
    }
}

public enum VoiceError: Error, CustomStringConvertible {
    case audioFormat, tooShort, noKey(String), unavailable(String), engine(String)

    public var description: String {
        switch self {
        case .audioFormat: "the microphone's format could not be converted"
        case .tooShort: "nothing was said"
        case .noKey(let e): "no \(e) API key: add one in Lasso's setup"
        case .unavailable(let why): why
        case .engine(let m): m
        }
    }
}
