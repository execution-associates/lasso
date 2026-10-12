import Foundation

/// herdr's agent names are identifiers: a lowercase letter first, then
/// lowercase letters, digits, '-' and '_', 1-32 characters. What a human
/// types becomes one ("Check HN page" is "check-hn-page"); lasso's
/// /api/agent/rename applies the same rule (agentName in src/agentrename.go).
public enum AgentName {
    public static func from(_ typed: String) -> String {
        var out = ""
        var dash = false
        for ch in typed.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() {
            if ch.isASCII, ch.isLetter || ch.isNumber || ch == "_" || ch == "-" {
                out.append(ch)
                dash = ch == "-"
            } else if !dash, !out.isEmpty {
                out.append("-")
                dash = true
            }
        }
        var n = Substring(out).drop { $0.isNumber || $0 == "_" || $0 == "-" }
        if n.count > 32 { n = n.prefix(32) }
        while let last = n.last, last == "_" || last == "-" { n = n.dropLast() }
        return String(n)
    }
}
