import SwiftUI

/// A long message folded, as the web folds a long plan: past `chat.fold`'s
/// characters or lines it shows its first `height` points, fading out, with
/// Show more under it. Rows are drawn afresh on every poll, so whether one is
/// open lives in `Unfolded`, by row id.
enum Fold {
    static func isLong(_ text: String) -> Bool {
        let f = Theme.Chat.fold
        if text.count > Int(f.number("chars")) { return true }
        return text.reduce(1) { $1 == "\n" ? $0 + 1 : $0 } > Int(f.number("lines"))
    }
}

struct Folding<Content: View>: View {
    /// The row's id, which keeps it open across polls; nil never folds.
    let id: String?
    let long: Bool
    @ViewBuilder var content: Content
    @ObservedObject private var unfolded = Unfolded.shared

    var body: some View {
        if let id, long {
            let f = Theme.Chat.fold
            let open = unfolded.open.contains(id)
            VStack(alignment: .leading, spacing: Theme.space("xs")) {
                if open {
                    content
                } else {
                    // Laid out whole and clipped, so a fold never re-wraps or
                    // truncates the text it cuts through.
                    content
                        .fixedSize(horizontal: false, vertical: true)
                        .frame(height: f.number("height"), alignment: .top)
                        .clipped()
                        .mask(alignment: .top) {
                            VStack(spacing: 0) {
                                Rectangle()
                                LinearGradient(colors: [.black, .clear], startPoint: .top, endPoint: .bottom)
                                    .frame(height: f.number("fade"))
                            }
                        }
                }
                Button(open ? "Show less" : "Show more") { unfolded.toggle(id) }
                    .buttonStyle(.plain)
                    .font(Theme.Fonts.small.font)
                    .foregroundStyle(Theme.Text.secondary.color)
                    .parityAnchor("chat.fold")
            }
        } else {
            content
        }
    }
}

/// An agent's thinking, folded to one line as the web shows it: "Thinking"
/// and its opening words; a click unfolds the rest.
struct ThinkingRow: View {
    let id: String
    let text: String
    var font: Font = Theme.Fonts.body.font
    @ObservedObject private var unfolded = Unfolded.shared

    var body: some View {
        let open = unfolded.open.contains(id)
        VStack(alignment: .leading, spacing: Theme.space("xs")) {
            Button { unfolded.toggle(id) } label: {
                HStack(spacing: Theme.space("xs")) {
                    LucideIcon(open ? .chevronDown : .chevronRight, size: Theme.Fonts.small.number("size"))
                    Text("Thinking").italic()
                    if !open {
                        Text(Self.preview(text)).italic().opacity(0.7).truncationMode(.tail)
                    }
                }
                .lineLimit(1)
                .font(Theme.Fonts.small.font)
                .foregroundStyle(Theme.Text.secondary.color)
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel(open ? "Hide thinking" : "Show thinking")
            if open {
                Text(text)
                    .font(font)
                    .foregroundStyle(Theme.Text.secondary.color)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
    }

    /// The opening words: the first line, at most 140 characters.
    static func preview(_ text: String) -> String {
        String((text.split(separator: "\n", maxSplits: 1).first ?? "").prefix(140))
    }
}
