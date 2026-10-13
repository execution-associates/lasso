import Testing

@testable import LassoDesktopKit

/// A message folds past chat.fold's characters or lines, as the web folds a
/// long plan; a short one never does.
@Test func aLongMessageFolds() {
    #expect(!Fold.isLong("short answer"))
    #expect(Fold.isLong(String(repeating: "x", count: 1201)))
    #expect(!Fold.isLong(String(repeating: "x", count: 1200)))
    #expect(Fold.isLong(Array(repeating: "line", count: 25).joined(separator: "\n")))
    #expect(!Fold.isLong(Array(repeating: "line", count: 24).joined(separator: "\n")))
}

/// Folded thinking shows its first line, cut at 140 characters.
@Test func thinkingFoldsToItsOpeningWords() {
    #expect(ThinkingRow.preview("Look at the tests first.\nThen the code.") == "Look at the tests first.")
    #expect(ThinkingRow.preview(String(repeating: "y", count: 200)).count == 140)
}
