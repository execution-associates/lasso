import SwiftUI

// An agent's prose as the Markdown it is: headings, lists, fenced code,
// pipe tables, quotes and rules, each line's inline Markdown (bold, code,
// links) styled by MessageBubble.styled. AttributedString parses inline
// Markdown only, so the blocks are read here, line by line.

/// One block of an agent's message.
enum MDBlock: Equatable {
    case heading(level: Int, text: String)
    case paragraph(String)
    case list(ordered: Bool, items: [MDListItem])
    case code(lang: String?, text: String)
    case table(header: [String], align: [MDAlign], rows: [[String]])
    case quote(String)
    case rule
}

struct MDListItem: Equatable {
    var level: Int
    /// "1." for an ordered item, nil for a bullet.
    var marker: String?
    var text: String
}

enum MDAlign: Equatable { case leading, center, trailing }

enum Markdown {
    static func blocks(_ text: String) -> [MDBlock] {
        let lines = text.replacingOccurrences(of: "\r\n", with: "\n").components(separatedBy: "\n")
        var out: [MDBlock] = []
        var para: [String] = []
        var i = 0
        func flush() {
            if !para.isEmpty { out.append(.paragraph(para.joined(separator: "\n"))) }
            para = []
        }
        while i < lines.count {
            let line = lines[i]
            let t = line.trimmingCharacters(in: .whitespaces)
            if t.isEmpty {
                flush()
                i += 1
            } else if t.hasPrefix("```") || t.hasPrefix("~~~") {
                flush()
                let fence = String(t.prefix(3))
                let lang = t.dropFirst(3).trimmingCharacters(in: .whitespaces)
                var body: [String] = []
                i += 1
                while i < lines.count, !lines[i].trimmingCharacters(in: .whitespaces).hasPrefix(fence) {
                    body.append(lines[i])
                    i += 1
                }
                i += 1
                out.append(.code(lang: lang.isEmpty ? nil : lang, text: body.joined(separator: "\n")))
            } else if t.contains("|"), i + 1 < lines.count, let align = separator(lines[i + 1]) {
                flush()
                let header = cells(t)
                var rows: [[String]] = []
                i += 2
                while i < lines.count {
                    let r = lines[i].trimmingCharacters(in: .whitespaces)
                    guard !r.isEmpty, r.contains("|") else { break }
                    rows.append(cells(r))
                    i += 1
                }
                let n = max(header.count, rows.map(\.count).max() ?? 0)
                func pad(_ r: [String]) -> [String] { r + Array(repeating: "", count: max(0, n - r.count)) }
                out.append(.table(header: pad(header), align: align + Array(repeating: .leading, count: max(0, n - align.count)), rows: rows.map(pad)))
            } else if let h = heading(t) {
                flush()
                out.append(h)
                i += 1
            } else if isRule(t) {
                flush()
                out.append(.rule)
                i += 1
            } else if t.hasPrefix(">") {
                flush()
                var q: [String] = []
                while i < lines.count {
                    let r = lines[i].trimmingCharacters(in: .whitespaces)
                    guard r.hasPrefix(">") else { break }
                    q.append(String(r.dropFirst()).trimmingCharacters(in: .whitespaces))
                    i += 1
                }
                out.append(.quote(q.joined(separator: "\n")))
            } else if let first = listItem(line) {
                flush()
                var items = [first]
                i += 1
                while i < lines.count {
                    if let next = listItem(lines[i]) {
                        // Bullets turning into numbers at the top level start another list.
                        if next.level == 0, (next.marker != nil) != (first.marker != nil) { break }
                        items.append(next)
                    } else if !lines[i].trimmingCharacters(in: .whitespaces).isEmpty, lines[i].hasPrefix("  ") {
                        // A wrapped item's continuation line.
                        items[items.count - 1].text += " " + lines[i].trimmingCharacters(in: .whitespaces)
                    } else {
                        break
                    }
                    i += 1
                }
                out.append(.list(ordered: first.marker != nil, items: items))
            } else {
                para.append(line)
                i += 1
            }
        }
        flush()
        return out
    }

    static func heading(_ t: String) -> MDBlock? {
        let hashes = t.prefix { $0 == "#" }.count
        guard (1...6).contains(hashes), t.dropFirst(hashes).first == " " else { return nil }
        return .heading(level: hashes, text: t.dropFirst(hashes).trimmingCharacters(in: .whitespaces))
    }

    static func isRule(_ t: String) -> Bool {
        let s = t.replacingOccurrences(of: " ", with: "")
        guard s.count >= 3, let c = s.first, "-*_".contains(c) else { return false }
        return s.allSatisfy { $0 == c }
    }

    static func listItem(_ line: String) -> MDListItem? {
        let indent = line.prefix { $0 == " " || $0 == "\t" }.reduce(0) { $0 + ($1 == "\t" ? 4 : 1) }
        let t = line.trimmingCharacters(in: .whitespaces)
        if let c = t.first, "-*+".contains(c), t.dropFirst().first == " " {
            return MDListItem(level: indent / 2, marker: nil, text: String(t.dropFirst(2)))
        }
        let digits = t.prefix { $0.isNumber }
        guard !digits.isEmpty, digits.count <= 3 else { return nil }
        let rest = t.dropFirst(digits.count)
        guard let p = rest.first, p == "." || p == ")", rest.dropFirst().first == " " else { return nil }
        return MDListItem(level: indent / 2, marker: digits + ".", text: String(rest.dropFirst(2)))
    }

    /// A table's delimiter row (`|---|:--:|--:|`), as each column's alignment.
    static func separator(_ line: String) -> [MDAlign]? {
        let parts = cells(line.trimmingCharacters(in: .whitespaces))
        guard !parts.isEmpty, line.contains("-") else { return nil }
        var out: [MDAlign] = []
        for p in parts {
            let core = p.trimmingCharacters(in: CharacterSet(charactersIn: ":"))
            guard !core.isEmpty, core.allSatisfy({ $0 == "-" }) else { return nil }
            let left = p.hasPrefix(":")
            let right = p.hasSuffix(":")
            out.append(left && right ? .center : right ? .trailing : .leading)
        }
        return out
    }

    /// A table row's cells: outer pipes dropped, `\|` kept as a pipe.
    static func cells(_ row: String) -> [String] {
        var s = Substring(row)
        if s.hasPrefix("|") { s = s.dropFirst() }
        if s.hasSuffix("|"), !s.hasSuffix("\\|") { s = s.dropLast() }
        var out: [String] = []
        var cur = ""
        var escaped = false
        for ch in s {
            if escaped {
                cur.append(ch)
                escaped = false
            } else if ch == "\\" {
                escaped = true
            } else if ch == "|" {
                out.append(cur.trimmingCharacters(in: .whitespaces))
                cur = ""
            } else {
                cur.append(ch)
            }
        }
        out.append(cur.trimmingCharacters(in: .whitespaces))
        return out
    }
}

/// An agent's message, block by block.
struct MarkdownView: View {
    let text: String
    /// The prose face: the chat's in a window, the body's in the card.
    var font: Font = Theme.Fonts.chat.font

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.space("l")) {
            ForEach(Array(Markdown.blocks(text).enumerated()), id: \.offset) { _, b in block(b) }
        }
        .foregroundStyle(Theme.Text.primary.color)
        .textSelection(.enabled)
    }

    @ViewBuilder private func block(_ b: MDBlock) -> some View {
        switch b {
        case .heading(let level, let t):
            Text(MessageBubble.styled(t))
                .font(level <= 2 ? Theme.Fonts.title.font : Theme.Fonts.chat.font(weight: Theme.Fonts.label.weightNumber))
                .padding(.top, level <= 2 ? Theme.space("xs") : 0)
        case .paragraph(let t):
            Text(MessageBubble.styled(t)).font(font).fixedSize(horizontal: false, vertical: true)
        case .list(_, let items):
            VStack(alignment: .leading, spacing: Theme.space("xs")) {
                ForEach(Array(items.enumerated()), id: \.offset) { _, item in
                    HStack(alignment: .firstTextBaseline, spacing: Theme.space("m")) {
                        Text(item.marker ?? (item.level == 0 ? "•" : "◦"))
                            .foregroundStyle(Theme.Text.secondary.color)
                            .monospacedDigit()
                        Text(MessageBubble.styled(item.text)).fixedSize(horizontal: false, vertical: true)
                    }
                    .font(font)
                    .padding(.leading, CGFloat(item.level) * Theme.space("2xl"))
                }
            }
        case .code(_, let t):
            ScrollView(.horizontal, showsIndicators: false) {
                Text(t).font(Theme.Fonts.terminal.font).fixedSize()
                    .padding(Theme.space("l"))
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt).fill(Theme.Panel.surface.color))
        case .table(let header, let align, let rows):
            MarkdownTable(header: header, align: align, rows: rows)
        case .quote(let t):
            HStack(alignment: .top, spacing: Theme.space("l")) {
                Rectangle().fill(Theme.accent.color).frame(width: Theme.space("2xs"))
                Text(MessageBubble.styled(t)).font(font).foregroundStyle(Theme.Text.secondary.color)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .fixedSize(horizontal: false, vertical: true)
        case .rule:
            Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair"))
        }
    }
}

/// A pipe table: a header row, a rule, the rows; scrolls sideways when it is
/// wider than the message.
struct MarkdownTable: View {
    let header: [String]
    let align: [MDAlign]
    let rows: [[String]]

    var body: some View {
        // As wide as the message when it fits; sideways scrolling when not.
        ViewThatFits(in: .horizontal) {
            grid
            ScrollView(.horizontal) { grid }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityLabel("Table: \(header.joined(separator: ", ")), \(rows.count) rows")
    }

    private var grid: some View {
            Grid(alignment: .leading, horizontalSpacing: Theme.space("xl"), verticalSpacing: Theme.space("s")) {
                GridRow {
                    ForEach(header.indices, id: \.self) { c in
                        cell(header[c], c).font(Theme.Fonts.body.font(weight: Theme.Fonts.label.weightNumber))
                            .gridColumnAlignment(alignment(c))
                    }
                }
                .foregroundStyle(Theme.Text.secondary.color)
                Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair")).gridCellUnsizedAxes(.horizontal)
                ForEach(rows.indices, id: \.self) { r in
                    GridRow {
                        ForEach(rows[r].indices, id: \.self) { c in cell(rows[r][c], c).font(Theme.Fonts.body.font) }
                    }
                }
            }
            .padding(.vertical, Theme.space("xs"))
    }

    private func alignment(_ c: Int) -> HorizontalAlignment {
        switch c < align.count ? align[c] : .leading {
        case .leading: .leading
        case .center: .center
        case .trailing: .trailing
        }
    }

    /// A short cell keeps its own width; a long one wraps at table_cell, so
    /// no column grows to fill the message.
    @ViewBuilder private func cell(_ s: String, _ c: Int) -> some View {
        let w = Theme.Chat.bubble.number("table_cell")
        let text = Text(MessageBubble.styled(s))
        if Theme.Fonts.body.nsFont.pointSize * CGFloat(s.count) / 2 <= w {
            text.fixedSize()
        } else {
            text.lineLimit(4).frame(width: w, alignment: Alignment(horizontal: alignment(c), vertical: .center))
                .fixedSize(horizontal: false, vertical: true)
        }
    }
}
