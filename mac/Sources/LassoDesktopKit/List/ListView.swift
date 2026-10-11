import SwiftUI

/// The fleet panel (ref a3): a header per workspace, then rows of state dot,
/// agent avatar, name (the agent's label or tab) and a close at the end.
/// Pins come first.
public struct ListView: View {
    @ObservedObject var model: ListModel
    @ObservedObject var fleet: FleetModel

    public init(model: ListModel) {
        self.model = model
        fleet = model.fleet
    }

    private var row: Token { Theme.List.row }

    public var body: some View {
        ScrollViewReader { proxy in
            ScrollView(.vertical) {
                VStack(alignment: .leading, spacing: 0) {
                    if !model.pinned.isEmpty {
                        header("Pinned", anchor: "list.header.pinned")
                        ForEach(model.pinned) { rowView($0, pinnedSection: true) }
                    }
                    ForEach(Array(model.groups.enumerated()), id: \.element.id) { gi, g in
                        header(g.title, anchor: "list.header[\(gi)]")
                        ForEach(g.agents) { rowView($0, pinnedSection: false) }
                    }
                    if fleet.agents.isEmpty {
                        Text("No agents running")
                            .font(Theme.Fonts.body.font)
                            .foregroundStyle(Theme.Text.secondary.color)
                            .padding(row.number("pad_h"))
                    }
                    if let n = model.notice {
                        Text(n)
                            .font(Theme.Fonts.small.font)
                            .foregroundStyle(Theme.Text.secondary.color)
                            .padding(.horizontal, row.number("pad_h"))
                            .padding(.vertical, Theme.space("m"))
                    }
                }
                .padding(.vertical, Theme.space("m"))
            }
            .onChange(of: model.selected) { _, key in
                if let key { proxy.scrollTo(key) }
            }
        }
        .onPreferenceChange(RowFrames.self) { frames in MainActor.assumeIsolated { model.rowFrames = frames } }
        .panelChrome(Theme.List.size.size, anchor: "list")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Fleet")
    }

    private func header(_ title: String, anchor: String) -> some View {
        Text(title)
            .font(Theme.Fonts.header.font)
            .foregroundStyle(Theme.Text.secondary.color)
            .lineLimit(1)
            .padding(.horizontal, row.number("pad_h"))
            .padding(.top, Theme.space("xl"))
            .padding(.bottom, Theme.space("xs"))
            .parityAnchor(anchor)
            .accessibilityAddTraits(.isHeader)
    }

    private func rowView(_ p: HostPane, pinnedSection: Bool) -> some View {
        let key = p.key
        let selected = model.selected == key
        let hovered = model.hovered == key
        let pinned = model.pinnedKeys.contains(key)
        return Group {
            if model.confirmEnd == key {
                HStack(spacing: Theme.space("l")) {
                    Text("End \(p.displayName)?")
                        .font(Theme.Fonts.name.font)
                        .foregroundStyle(Theme.Text.primary.color)
                        .lineLimit(1)
                    Spacer()
                    PillButton(title: "End", textColor: Theme.dangerText.color, label: "Confirm: end \(p.displayName)") {
                        model.end(p)
                    }
                    PillButton(title: "Cancel", label: "Cancel ending \(p.displayName)") { model.confirmEnd = nil }
                }
                .padding(.horizontal, row.number("pad_h"))
                .frame(height: row.number("height"))
            } else {
                HStack(spacing: 0) {
                    Button { model.open(p) } label: {
                        HStack(spacing: 0) {
                            Circle()
                                .fill(StateStyle.color(p.state))
                                .frame(width: row.number("dot"), height: row.number("dot"))
                                .parityAnchor(pinnedSection ? "list.dot.pinned[\(key)]" : "list.dot[\(key)]")
                                .frame(width: row.number("dot_col") - row.number("pad_h"), alignment: .leading)
                            AgentAvatar(agent: p.agent, state: p.state)
                            Text(p.displayName)
                                .font(Theme.Fonts.name.font)
                                .foregroundStyle(Theme.Text.primary.color)
                                .lineLimit(1)
                                .truncationMode(.tail)
                                .padding(.leading, row.number("name_x") - row.number("dot_col") - row.number("avatar"))
                            if pinned && !pinnedSection {
                                Image(systemName: "pin.fill")
                                    .font(.system(size: Theme.icon.number("pin")))
                                    .foregroundStyle(Theme.Text.secondary.color)
                                    .padding(.leading, Theme.space("s"))
                            }
                            Spacer(minLength: 8)
                        }
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(
                        "\(p.displayName), \(p.state.label), \(p.agent) in \(p.group) on \(p.host_label)\(pinned ? ", pinned" : "")"
                            + (fleet.bot(for: p) != nil ? ", a bot" : ""))
                    Button { model.askEnd(p) } label: {
                        Image(systemName: "xmark").frame(width: row.number("action_w"), height: row.number("height")).contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("End \(p.displayName)")
                }
                .font(.system(size: Theme.icon.number("control")))
                .foregroundStyle(Theme.Text.secondary.color)
                .padding(.leading, row.number("pad_h"))
                .padding(.trailing, row.number("pad_h") - Theme.space("m"))
                .frame(height: row.number("height"))
                .background(
                    RoundedRectangle(cornerRadius: Theme.Row.radius.pt)
                        .fill(selected ? Theme.Row.selected.color : (hovered ? Theme.Row.hover.color : .clear))
                        .padding(.horizontal, Theme.space("m"))
                )
                .background(
                    GeometryReader { g in
                        Color.clear.preference(key: RowFrames.self, value: [pinnedSection ? "pinned:\(key)" : key: g.frame(in: .global)])
                    })
                .contextMenu {
                    Button(pinned ? "Unpin" : "Pin") { model.togglePin(p) }
                    Button("Focus in herdr") { model.focus(p) }
                    Divider()
                    Button("End agent…") { model.askEnd(p) }
                }
                .accessibilityAction(named: pinned ? "Unpin" : "Pin") { model.togglePin(p) }
                .accessibilityAction(named: "Focus in herdr") { model.focus(p) }
            }
        }
        .id(pinnedSection ? "pinned:\(key)" : key)
        .parityAnchor(pinnedSection ? "list.row.pinned[\(key)]" : "list.row[\(key)]")
    }
}

/// The rows' frames in the window, for the controller's hover polling.
struct RowFrames: PreferenceKey {
    static let defaultValue: [String: CGRect] = [:]
    static func reduce(value: inout [String: CGRect], nextValue: () -> [String: CGRect]) {
        value.merge(nextValue()) { $1 }
    }
}
