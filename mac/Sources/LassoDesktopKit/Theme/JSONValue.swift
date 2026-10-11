import Foundation

/// A JSON value. Theme tokens hold their value in the same form tokens.json
/// spells it, so the parity harness can compare code against spec exactly.
public enum JSONValue: Sendable, Equatable, Codable {
    case number(Double)
    case string(String)
    case bool(Bool)
    case array([JSONValue])
    case object([String: JSONValue])
    case null

    public init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if c.decodeNil() {
            self = .null
        } else if let b = try? c.decode(Bool.self) {
            self = .bool(b)
        } else if let n = try? c.decode(Double.self) {
            self = .number(n)
        } else if let s = try? c.decode(String.self) {
            self = .string(s)
        } else if let a = try? c.decode([JSONValue].self) {
            self = .array(a)
        } else {
            self = .object(try c.decode([String: JSONValue].self))
        }
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        switch self {
        case .number(let n): try c.encode(n)
        case .string(let s): try c.encode(s)
        case .bool(let b): try c.encode(b)
        case .array(let a): try c.encode(a)
        case .object(let o): try c.encode(o)
        case .null: try c.encodeNil()
        }
    }

    public var double: Double? {
        if case .number(let n) = self { return n }
        return nil
    }

    public var string: String? {
        if case .string(let s) = self { return s }
        return nil
    }

    public var array: [JSONValue]? {
        if case .array(let a) = self { return a }
        return nil
    }

    public subscript(key: String) -> JSONValue? {
        if case .object(let o) = self { return o[key] }
        return nil
    }

    /// Compact, key-sorted rendering for the parity report.
    public var compact: String {
        switch self {
        case .number(let n):
            return n == n.rounded() && abs(n) < 1e9 ? String(Int(n)) : String(n)
        case .string(let s): return s
        case .bool(let b): return String(b)
        case .null: return "null"
        case .array(let a): return "[" + a.map(\.compact).joined(separator: ",") + "]"
        case .object(let o):
            return "{" + o.keys.sorted().map { "\($0):\(o[$0]!.compact)" }.joined(separator: ",") + "}"
        }
    }
}

extension JSONValue: ExpressibleByIntegerLiteral, ExpressibleByFloatLiteral, ExpressibleByStringLiteral,
    ExpressibleByArrayLiteral, ExpressibleByDictionaryLiteral, ExpressibleByBooleanLiteral
{
    public init(integerLiteral value: Int) { self = .number(Double(value)) }
    public init(floatLiteral value: Double) { self = .number(value) }
    public init(stringLiteral value: String) { self = .string(value) }
    public init(arrayLiteral elements: JSONValue...) { self = .array(elements) }
    public init(dictionaryLiteral elements: (String, JSONValue)...) {
        self = .object(Dictionary(uniqueKeysWithValues: elements))
    }
    public init(booleanLiteral value: Bool) { self = .bool(value) }
}
