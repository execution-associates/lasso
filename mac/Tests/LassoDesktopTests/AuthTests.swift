import Foundation
import Testing

@testable import LassoDesktopKit

/// A canned network: each request is answered by `respond`, and every
/// request is recorded.
final class StubProtocol: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var respond: (URLRequest) -> (Int, [String: String], Data) = { _ in (404, [:], Data()) }
    nonisolated(unsafe) static var seen: [URLRequest] = []

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        Self.seen.append(request)
        let (code, headers, body) = Self.respond(request)
        let resp = HTTPURLResponse(url: request.url!, statusCode: code, httpVersion: "HTTP/1.1", headerFields: headers)!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: body)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}

    static var session: URLSession {
        let c = URLSessionConfiguration.ephemeral
        c.protocolClasses = [StubProtocol.self]
        return URLSession(configuration: c)
    }
}

/// The pieces of the sign-in that need no browser: the challenge, the
/// metadata address, PKCE, the probe, which header carries the token, and a
/// refusal that refreshes once and retries (PLAN 18).
@Suite(.serialized) struct AuthTests {
    @Test func challengeAndMetadataAddresses() {
        let c = #"Bearer realm="OAuth", resource_metadata="https://lasso.example.com/.well-known/oauth-protected-resource", error="invalid_token""#
        #expect(LassoAuth.resourceMetadata(c)?.absoluteString == "https://lasso.example.com/.well-known/oauth-protected-resource")
        #expect(LassoAuth.resourceMetadata(#"Basic realm="lasso""#) == nil)
        #expect(
            LassoAuth.serverMetadataURL(URL(string: "https://team.cloudflareaccess.com")!)?.absoluteString
                == "https://team.cloudflareaccess.com/.well-known/oauth-authorization-server")
        #expect(
            LassoAuth.serverMetadataURL(URL(string: "https://idp.example.com/tenant/a")!)?.absoluteString
                == "https://idp.example.com/.well-known/oauth-authorization-server/tenant/a")
        #expect(LassoAuth.origin(URL(string: "https://lasso.example.com/x/y")!) == "https://lasso.example.com")
        // RFC 7636 appendix B.
        #expect(LassoAuth.challenge(for: "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk") == "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM")
    }

    @Test func probeReadsWhatALassoWants() async {
        let base = URL(string: "https://lasso.example.com")!
        StubProtocol.respond = { _ in (200, ["Content-Type": "application/json"], Data(#"{"lasso_protocol":22,"lasso_version":"6.4.0","herdr_protocol":22,"herdr_version":"0.9.3","compatible":true}"#.utf8)) }
        #expect(await LassoAuth.probe(base, session: StubProtocol.session) == .open)
        #expect(StubProtocol.seen.last?.value(forHTTPHeaderField: "X-Requested-With") == "XMLHttpRequest")
        #expect(StubProtocol.seen.last?.value(forHTTPHeaderField: "Authorization") == nil)
        StubProtocol.respond = { _ in (401, ["WWW-Authenticate": #"Bearer resource_metadata="https://lasso.example.com/.well-known/oauth-protected-resource""#], Data()) }
        #expect(await LassoAuth.probe(base, session: StubProtocol.session)
            == .signIn(resourceMetadata: URL(string: "https://lasso.example.com/.well-known/oauth-protected-resource")!))
        StubProtocol.respond = { _ in (401, ["WWW-Authenticate": #"Basic realm="lasso""#], Data()) }
        #expect(await LassoAuth.probe(base, session: StubProtocol.session) == .password)
    }

    @Test func aRefusalRefreshesOnceAndRetries() async throws {
        let base = URL(string: "https://lasso.example.com")!
        let grant = OAuthGrant(
            clientID: "c1", tokenEndpoint: URL(string: "https://auth.example.com/token")!, resource: "https://lasso.example.com",
            accessToken: "old", refreshToken: "r1", expiresAt: Date().addingTimeInterval(900))
        StubProtocol.seen = []
        StubProtocol.respond = { r in
            if r.url?.path == "/token" { return (200, [:], Data(#"{"access_token":"new","expires_in":900}"#.utf8)) }
            let auth = r.value(forHTTPHeaderField: "Authorization")
            return auth == "Bearer new" ? (200, ["Content-Type": "application/json"], Data(#"{"panes":[]}"#.utf8)) : (401, [:], Data())
        }
        let kept = Box()
        let c = LassoClient(baseURL: base, grant: grant, protocolClasses: [StubProtocol.self], persist: { g, _ in kept.grant = g })
        struct P: Decodable { var panes: [String] }
        let p: P = try await c.get("/api/all-panes")
        #expect(p.panes.isEmpty)
        #expect(StubProtocol.seen.map { $0.url!.path } == ["/api/all-panes", "/token", "/api/all-panes"])
        // A refresh that rotates nothing keeps the refresh token it had.
        #expect(kept.grant?.accessToken == "new" && kept.grant?.refreshToken == "r1")

        // With an Access service token at the edge, lasso's token moves to X-Lasso-Token.
        let s = LassoClient(baseURL: base, accessID: "id", accessSecret: "secret", grant: grant, protocolClasses: [StubProtocol.self])
        let r = s.request("/api/version")
        #expect(r.value(forHTTPHeaderField: "X-Lasso-Token") == "old" && r.value(forHTTPHeaderField: "Authorization") == nil)
        #expect(r.value(forHTTPHeaderField: "CF-Access-Client-Id") == "id")
    }
}

private final class Box: @unchecked Sendable { var grant: OAuthGrant? }
