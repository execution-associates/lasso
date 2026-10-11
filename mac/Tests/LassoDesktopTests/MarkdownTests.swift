import Testing

@testable import LassoDesktopKit

/// An agent's message is read into blocks: the HN table from the user's
/// screenshot, lists, fences, headings, quotes and rules.
@Test func markdownBlocks() {
    let text = """
        HN front page right now (rank order):

        | # | Story | Pts |
        |---|---|--:|
        | 1 | [Total Annihilation](https://example.com) | 279 |
        | 2 | Pipes \\| escaped | 111 |

        ## Notes
        - one
          wrapped
        - two
          - nested
        1. first
        2) second

        ```swift
        let x = 1
        ```
        > quoted
        ---
        """
    let b = Markdown.blocks(text)
    #expect(b.first == .paragraph("HN front page right now (rank order):"))
    guard case .table(let header, let align, let rows) = b[1] else {
        Issue.record("no table: \(b)")
        return
    }
    #expect(header == ["#", "Story", "Pts"] && align == [.leading, .leading, .trailing])
    #expect(rows.count == 2 && rows[0][1] == "[Total Annihilation](https://example.com)" && rows[1][1] == "Pipes | escaped")
    guard b.count == 8 else {
        Issue.record("blocks: \(b)")
        return
    }
    #expect(b[2] == .heading(level: 2, text: "Notes"))
    #expect(b[3] == .list(ordered: false, items: [
        MDListItem(level: 0, marker: nil, text: "one wrapped"), MDListItem(level: 0, marker: nil, text: "two"),
        MDListItem(level: 1, marker: nil, text: "nested"),
    ]))
    #expect(b[4] == .list(ordered: true, items: [MDListItem(level: 0, marker: "1.", text: "first"), MDListItem(level: 0, marker: "2.", text: "second")]))
    #expect(b[5] == .code(lang: "swift", text: "let x = 1"))
    #expect(b[6] == .quote("quoted"))
    #expect(b[7] == .rule)
    // A lone pipe in prose is not a table.
    #expect(Markdown.blocks("a | b\nnext line") == [.paragraph("a | b\nnext line")])
}
