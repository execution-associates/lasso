import SwiftUI

/// A plan Claude Code put up for approval (ExitPlanMode), rendered in full:
/// the plan is what the human approves, so it reads as the document it is
/// rather than as a one-line tool row. While its dialog is up, its choices are
/// the footer's next steps like any question's (CardModel's capture of the
/// tool's `ask`), and the dialog's own "Tell Claude what to change" is the
/// Other row.
struct PlanRow: View {
    let tool: ChatTool
    /// The prose face: the chat's in a window, the body's in the card.
    var font: Font = Theme.Fonts.chat.font
    /// In a bubble like the agent's messages (a window's conversation); the
    /// card's conversation is already a box, so there it sits bare.
    var boxed = true

    var body: some View {
        let b = Theme.Chat.bubble
        VStack(alignment: .leading, spacing: Theme.space("s")) {
            HStack(spacing: Theme.space("s")) {
                LucideIcon(.listTodo, size: Theme.Fonts.small.number("size"))
                Text(tool.title ?? "Plan")
                Spacer(minLength: Theme.space("m"))
                if let v = verdict { Text(v) }
            }
            .font(Theme.Fonts.small.font)
            .foregroundStyle(Theme.Text.secondary.color)
            .lineLimit(1)
            MarkdownView(text: tool.plan?.text ?? "", font: font)
                .frame(maxWidth: .infinity, alignment: .leading)
            if let fb = tool.plan?.feedback, !fb.isEmpty {
                Text("→ Sent back: " + fb)
                    .font(font)
                    .foregroundStyle(Theme.Text.secondary.color)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            if tool.state == "error", let e = tool.error, !e.isEmpty {
                Text(e)
                    .font(Theme.Fonts.small.font)
                    .foregroundStyle(Theme.Text.secondary.color)
                    .lineLimit(2)
            }
        }
        .padding(.horizontal, boxed ? b.number("pad_h") : 0)
        .padding(.vertical, boxed ? b.number("pad_v") : 0)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
            RoundedRectangle(cornerRadius: b.number("radius"), style: .continuous)
                .fill(boxed ? Theme.Panel.inner.color("fill") : .clear))
        .parityAnchor("chat.plan")
    }

    /// Where the plan stands: waiting on the reader, or what became of it.
    private var verdict: String? {
        switch tool.state {
        case "running": "Waiting for approval"
        case "error": "Not approved"
        default: tool.result_line.map { $0.prefix(1).uppercased() + $0.dropFirst() }
        }
    }
}
