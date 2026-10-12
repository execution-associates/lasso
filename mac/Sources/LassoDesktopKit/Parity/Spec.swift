import Foundation

/// mac/Spec/tokens.json, as the harness reads it.
public struct Spec: Decodable, Sendable {
    public struct Tolerances: Decodable, Sendable {
        public var deltaE2000: Double
        public var geometry_pt: Double
        public var duration_ms: Double
        public var overshoot_px: Double
    }

    public struct SpecToken: Decodable, Sendable {
        public var value: JSONValue
        public var provenance: String
        public var proposed: Bool?
    }

    public struct Patch: Decodable, Sendable {
        public var state: String
        public var anchor: String
        public var rect: [Double]?
        public var token: String
        /// For a translucent token: the token it is composited over.
        public var over: String?
        /// A vertical gradient [top, bottom] and how far down it to read.
        public var gradient: [String]?
        public var at: Double?
    }

    public struct Geometry: Decodable, Sendable {
        public var state: String
        public var anchor: String
        public var measure: String
        public var token: String
        public var expect: Double?
        public var row: Double?
    }

    public struct Motion: Decodable, Sendable {
        public var transition: String
        public var token: String
        public var travel_pt: Double?
        public var assert: [String]
    }

    public var tolerances: Tolerances
    public var tokens: [String: SpecToken]
    public var patches: [Patch]
    public var geometry: [Geometry]
    public var motion: [Motion]
    public var states: [String]

    public static func load(_ url: URL) throws -> Spec {
        try JSONDecoder().decode(Spec.self, from: Data(contentsOf: url))
    }
}

/// One line of the parity report.
public struct ParityRow: Codable, Sendable {
    public enum Result: String, Codable, Sendable { case pass, fail, pending, info }

    public var state: String
    public var check: String
    public var token: String
    public var spec: String
    public var measured: String
    public var delta: String
    public var result: Result
}

extension Theme {
    /// Theme.all in tokens.json's shape, to seed the spec when the tokens
    /// are reworked (`Lasso --dump-tokens`). The spec is then edited
    /// by hand and checked against the code like any other spec.
    public static func dumpTokens() -> Data {
        var out: [String: Any] = [:]
        for t in all {
            let note: String
            switch t.provenance {
            case .measured(let n), .unmeasured(let n), .inferred(let n): note = n
            }
            var row: [String: Any] = [
                "value": (try? JSONSerialization.jsonObject(with: JSONEncoder().encode(t.value), options: [.fragmentsAllowed])) ?? NSNull(),
                "provenance": t.provenance.kind, "source": note,
            ]
            if t.proposed { row["proposed"] = true }
            out[t.id] = row
        }
        return (try? JSONSerialization.data(withJSONObject: out, options: [.prettyPrinted, .sortedKeys])) ?? Data()
    }
}
