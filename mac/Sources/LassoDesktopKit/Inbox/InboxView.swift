import SwiftUI

/// The inbox panel: one row per thing that waits on the human, in the list's
/// row anatomy. A row opens the agent's card or the bot's chat.
public struct InboxView: View {
    @ObservedObject var model: InboxModel
    @ObservedObject var fleet: FleetModel

    public init(model: InboxModel) {
        self.model = model
        fleet = model.fleet
    }

    private var row: Token { Theme.List.row }

    public var body: some View {
        let items = model.items
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                Text("Inbox")
                    .font(Theme.Fonts.title.font)
                    .foregroundStyle(Theme.Text.primary.color)
                Text(items.isEmpty ? "" : "\(items.count)")
                    .font(Theme.Fonts.body.font)
                    .foregroundStyle(Theme.Text.secondary.color)
                Spacer()
                RoundButton(symbol: "xmark", label: "Close (esc)") { model.onClose() }
                    .keyHint(Keys.esc)
            }
            .padding(.horizontal, row.number("pad_h"))
            .padding(.top, Theme.Layout.card.number("top"))
            .padding(.bottom, Theme.space("m"))

            if items.isEmpty {
                Text("Nothing waits on you.")
                    .font(Theme.Fonts.body.font)
                    .foregroundStyle(Theme.Text.secondary.color)
                    .padding(.horizontal, row.number("pad_h"))
                    .padding(.top, Theme.space("m"))
                    .parityAnchor("inbox.empty")
                Spacer()
            } else {
                ScrollView(.vertical) {
                    VStack(alignment: .leading, spacing: 0) {
                        ForEach(items) { item in rowView(item) }
                    }
                    .padding(.bottom, Theme.space("m"))
                }
            }
        }
        .panelChrome(Theme.List.size.size, anchor: "inbox")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Inbox, \(items.count) waiting")
    }

    private func rowView(_ item: InboxItem) -> some View {
        let (name, detail, state, agent): (String, String, AgentState, String) =
            switch item {
            case .agent(let p): (p.displayName, [statusPhrase(p, now: model.now()), p.group].joined(separator: " · "), p.state, p.agent)
            case .bot(let b):
                (
                    b.name,
                    b.state == "blocked" ? "Waiting on you: \(b.waiting_for ?? "a question")" : (b.last_text ?? "New message"),
                    b.state == "blocked" ? .blocked : .done, b.runtime
                )
            }
        return Button { model.open(item) } label: {
            HStack(alignment: .center, spacing: 0) {
                Circle()
                    .fill(StateStyle.color(state))
                    .frame(width: row.number("dot"), height: row.number("dot"))
                    .parityAnchor("inbox.dot[\(item.id)]")
                    .frame(width: row.number("dot_col") - row.number("pad_h"), alignment: .leading)
                AgentAvatar(agent: agent, state: state)
                VStack(alignment: .leading, spacing: Theme.space("2xs")) {
                    Text(name)
                        .font(Theme.Fonts.name.font)
                        .foregroundStyle(Theme.Text.primary.color)
                        .lineLimit(1)
                    Text(detail)
                        .font(Theme.Fonts.small.font)
                        .foregroundStyle(Theme.Text.secondary.color)
                        .lineLimit(1)
                }
                .padding(.leading, row.number("name_x") - row.number("dot_col") - row.number("avatar"))
                Spacer(minLength: Theme.space("m"))
                if case .bot = item {
                    Image(systemName: "bubble.left")
                        .font(.system(size: Theme.icon.number("control")))
                        .foregroundStyle(Theme.Text.secondary.color)
                }
            }
            .padding(.horizontal, row.number("pad_h"))
            .padding(.vertical, Theme.space("s"))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .parityAnchor("inbox.row[\(item.id)]")
        .accessibilityLabel("\(name): \(detail)")
    }
}
