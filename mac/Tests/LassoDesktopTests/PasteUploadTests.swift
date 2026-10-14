import Foundation
import Testing

@testable import LassoDesktopKit

/// Answers like lasso's /api/paste-file in parts: the first part opens an
/// upload of `size`, each next one must name the bytes so far.
final class PasteStub: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var seen: [(URLRequest, Data)] = []
    nonisolated(unsafe) static var received = 0
    nonisolated(unsafe) static var size = 0

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        var body = request.httpBody ?? Data()
        if body.isEmpty, let s = request.httpBodyStream {
            s.open()
            var buf = [UInt8](repeating: 0, count: 4096)
            while s.hasBytesAvailable {
                let n = s.read(&buf, maxLength: buf.count)
                if n <= 0 { break }
                body.append(buf, count: n)
            }
            s.close()
        }
        Self.seen.append((request, body))
        let q = Dictionary(
            uniqueKeysWithValues: (URLComponents(url: request.url!, resolvingAgainstBaseURL: false)?.queryItems ?? []).map { ($0.name, $0.value ?? "") })
        if let s = q["size"] { Self.size = Int(s)!; Self.received = 0 }
        Self.received += body.count
        let more = Self.size > 0 && Self.received < Self.size
        let json = more ? #"{"path":"/up/rec.mov","upload":"t1","received":\#(Self.received)}"# : #"{"path":"/up/rec.mov"}"#
        let resp = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": "application/json"])!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(json.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

/// A file over one part goes in parts: the first names the whole size, each
/// next one its token and offset, and together they carry every byte.
@Test func aBigFileUploadsInParts() async throws {
    let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
    try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    defer { try? FileManager.default.removeItem(at: dir) }
    let file = dir.appendingPathComponent("rec.mov")
    let bytes = Data((0..<25).map { UInt8($0) })
    try bytes.write(to: file)

    let client = LassoClient(baseURL: URL(string: "http://127.0.0.1:18090")!, protocolClasses: [PasteStub.self])
    let path = try await client.pasteFile(file, host: "minime", chunk: 10)
    #expect(path == "/up/rec.mov")
    let parts = PasteStub.seen
    #expect(parts.count == 3)
    #expect(parts.map(\.1).reduce(Data(), +) == bytes)
    let queries = parts.map { req, _ in
        Dictionary(uniqueKeysWithValues: (URLComponents(url: req.url!, resolvingAgainstBaseURL: false)?.queryItems ?? []).map { ($0.name, $0.value ?? "") })
    }
    #expect(queries[0] == ["host": "minime", "name": "rec.mov", "size": "25"])
    #expect(queries[1] == ["host": "minime", "upload": "t1", "offset": "10"])
    #expect(queries[2] == ["host": "minime", "upload": "t1", "offset": "20"])
}
