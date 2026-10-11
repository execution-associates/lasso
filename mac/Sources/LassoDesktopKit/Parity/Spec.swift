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
