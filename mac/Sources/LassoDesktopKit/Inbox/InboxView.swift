import SwiftUI

/// The inbox panel, for when the card has no message to open: lasso's
/// notices, newest first, each acknowledged with ×, or the empty state.
public struct InboxView: View {
    @ObservedObject var model: InboxModel
    @ObservedObject var fleet: FleetModel

    public init(model: InboxModel) {
        self.model = model
        fleet = model.fleet
    }

    private var row: Token { Theme.List.row }

    public var body: some View {
        let notices = fleet.inboxNotices.reversed()
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                Text("Inbox")
                    .font(Theme.Fonts.title.font)
                    .foregroundStyle(Theme.Text.primary.color)
                Spacer()
                RoundButton(symbol: "xmark", label: "Close (esc)") { model.onClose() }
            }
            .padding(.horizontal, row.number("pad_h"))
            .padding(.top, Theme.Layout.card.number("top"))
            .padding(.bottom, Theme.space("m"))

            if notices.isEmpty {
                VStack(alignment: .leading, spacing: Theme.space("xs")) {
                    Text("Nothing waiting on you.")
                        .font(Theme.Fonts.body.font)
                        .foregroundStyle(Theme.Text.primary.color)
                    Text("Questions, finished turns and notices from your claude and omp agents land here.")
                        .font(Theme.Fonts.small.font)
                        .foregroundStyle(Theme.Text.secondary.color)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .padding(.horizontal, row.number("pad_h"))
                .padding(.top, Theme.space("m"))
                .parityAnchor("inbox.empty")
                Spacer()
            } else {
                ScrollView(.vertical) {
                    VStack(alignment: .leading, spacing: 0) {
                        ForEach(Array(notices)) { n in noticeRow(n) }
                    }
                    .padding(.bottom, Theme.space("m"))
                }
            }
        }
        .panelChrome(Theme.List.size.size, anchor: "inbox")
        .accessibilityElement(children: .contain)
        .accessibilityLabel(notices.isEmpty ? "Inbox, nothing waiting" : "Inbox, \(notices.count) notices")
    }

    private func noticeRow(_ n: InboxNotice) -> some View {
        let state: AgentState = n.level == "error" ? .blocked : (n.level == "success" ? .done : .idle)
        return HStack(alignment: .top, spacing: 0) {
            Circle()
                .fill(StateStyle.color(state))
                .frame(width: row.number("dot"), height: row.number("dot"))
                .padding(.top, Theme.space("s"))
                .frame(width: row.number("dot_col") - row.number("pad_h"), alignment: .leading)
                .parityAnchor("inbox.notice.dot[\(n.id)]")
            VStack(alignment: .leading, spacing: Theme.space("2xs")) {
                Text(n.title)
                    .font(Theme.Fonts.name.font)
                    .foregroundStyle(Theme.Text.primary.color)
                if let d = n.detail, !d.isEmpty {
                    Text(d)
                        .font(Theme.Fonts.small.font)
                        .foregroundStyle(Theme.Text.secondary.color)
                        .fixedSize(horizontal: false, vertical: true)
                }
                Text(Date(timeIntervalSince1970: Double(n.at) / 1000), style: .relative)
                    .font(Theme.Fonts.small.font)
                    .foregroundStyle(Theme.Text.secondary.color)
            }
            Spacer(minLength: Theme.space("m"))
            Button { model.acknowledge(n) } label: {
                Image(systemName: "xmark").frame(width: row.number("action_w"), height: row.number("height")).contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .foregroundStyle(Theme.Text.secondary.color)
            .accessibilityLabel("Acknowledge \(n.title)")
        }
        .padding(.horizontal, row.number("pad_h"))
        .padding(.vertical, Theme.space("s"))
        .parityAnchor("inbox.notice[\(n.id)]")
        .accessibilityElement(children: .contain)
    }
}
