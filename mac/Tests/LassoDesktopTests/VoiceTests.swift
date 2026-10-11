import Foundation
import Testing

@testable import LassoDesktopKit

@Test func wavHeaderIsPCM16Mono16k() {
    let wav = WAV.encode([0, 0.5, -0.5, 1], sampleRate: 16000)
    #expect(wav.count == 44 + 8)
    #expect(String(decoding: wav[0..<4], as: UTF8.self) == "RIFF")
    #expect(String(decoding: wav[8..<12], as: UTF8.self) == "WAVE")
    func u16(_ o: Int) -> UInt16 { UInt16(wav[o]) | UInt16(wav[o + 1]) << 8 }
    func u32(_ o: Int) -> UInt32 { UInt32(u16(o)) | UInt32(u16(o + 2)) << 16 }
    #expect(u16(20) == 1 && u16(22) == 1 && u32(24) == 16000 && u16(34) == 16)
    #expect(u32(40) == 8)
    #expect(Int16(bitPattern: u16(44 + 6)) == Int16.max)
}

@Test func vadEndsATurnAfterSpeechThenSilence() {
    var vad = VAD()
    var events: [VAD.Event] = []
    // 300 ms of room noise, 600 ms of speech, then silence.
    for _ in 0..<15 { events.append(vad.feed(-60)) }
    for _ in 0..<30 { events.append(vad.feed(-25)) }
    var endedAt: Int?
    for i in 0..<100 {
        if vad.feed(-60) == .ended {
            endedAt = i
            break
        }
    }
    #expect(events.contains(.speechStarted))
    // 900 ms of 20 ms windows.
    #expect(endedAt == 44)
}

@Test func vadIgnoresAClickShorterThanMinimumSpeech() {
    var vad = VAD()
    for _ in 0..<15 { _ = vad.feed(-60) }
    for _ in 0..<5 { _ = vad.feed(-20) }
    var ended = false
    for _ in 0..<100 where vad.feed(-60) == .ended { ended = true }
    #expect(!ended)
}

@Test func transcriptMetaFitsTheServersRules() {
    let meta = Transcript(text: "hi", engine: "groq", model: "whisper-large-v3-turbo").meta
    #expect(meta.keys.allSatisfy { $0.range(of: "^[a-z_]{1,32}$", options: .regularExpression) != nil })
    #expect(meta["input"] == "voice")
}
