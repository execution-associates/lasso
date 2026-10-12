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
                    HStack(spacing: Theme.space("s")) {
                        filterBar
                        PillButton(title: "New", primary: true, label: "New: an agent or a terminal") { model.onNew() }
                            .parityAnchor("list.new")
                    }
                    .padding(.horizontal, row.number("pad_h"))
                    .padding(.top, Theme.space("xs"))
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
                    } else if model.shown.isEmpty {
                        Text("No agents match")
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
                .coordinateSpace(name: ColumnRows.space)
            }
            .onChange(of: model.selected) { _, key in
                if let key { proxy.scrollTo(key) }
            }
        }
        .onPreferenceChange(ColumnRowFrames.self) { frames in MainActor.assumeIsolated { model.rowFrames = frames } }
        .panelChrome(Theme.List.size.size, anchor: "list")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Fleet")
    }

    /// Filter-as-you-type, as the web's agent lists have: a typed key goes
    /// here wherever the cursor is (`ListController`), esc clears it.
    private var filterBar: some View {
        HStack(spacing: Theme.space("s")) {
            LucideIcon(.search, size: Theme.Fonts.body.number("size")).foregroundStyle(Theme.Text.secondary.color)
            PlainField(text: $model.filter, placeholder: "Filter", label: "Filter agents", made: { model.filterField = $0 })
            if !model.filter.isEmpty {
                Text("\(model.shown.count)/\(fleet.agents.count)")
                    .font(Theme.Fonts.small.font)
                    .foregroundStyle(Theme.Text.secondary.color)
                    .fixedSize()
                Button { model.filter = "" } label: {
                    LucideIcon(.x, size: Theme.icon.number("control")).contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .foregroundStyle(Theme.Text.secondary.color)
                .accessibilityLabel("Clear filter")
            }
        }
        .font(Theme.Fonts.body.font)
        .parityAnchor("list.filter")
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
        let id = pinnedSection ? "pinned:\(key)" : key
        let lit = model.isLit(id, key: key)
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
                .columnRow(id)
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
                            Group {
                                if model.renaming == key {
                                    InlineField(
                                        text: $model.renameDraft, placeholder: p.displayName, label: "New name for \(p.displayName)",
                                        onSubmit: { model.commitRename() }, onCancel: { model.cancelRename() })
                                        .padding(.horizontal, Theme.space("s"))
                                        .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt).strokeBorder(Theme.accent.color, lineWidth: 1))
                                } else {
                                    Text(p.displayName)
                                        .font(Theme.Fonts.name.font)
                                        .foregroundStyle(Theme.Text.primary.color)
                                        .lineLimit(1)
                                        .truncationMode(.tail)
                                }
                            }
                            .padding(.leading, row.number("name_x") - row.number("dot_col") - row.number("avatar"))
                            if pinned && !pinnedSection {
                                LucideIcon(.pin, size: Theme.icon.number("pin"))
                                    .foregroundStyle(Theme.Text.secondary.color)
                                    .padding(.leading, Theme.space("s"))
                            }
                            Spacer(minLength: 8)
                        }
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    // While renaming, a click in the field must not open the agent.
                    .disabled(model.renaming == key)
                    .accessibilityLabel(
                        "\(p.displayName), \(p.state.label), \(p.agent) in \(p.group) on \(p.host_label)\(pinned ? ", pinned" : "")"
                            + (fleet.bot(for: p) != nil ? ", a bot" : ""))
                    Button { model.askEnd(p) } label: {
                        LucideIcon(.x, size: Theme.icon.number("control"))
                            .frame(width: row.number("action_w"), height: row.number("height")).contentShape(Rectangle())
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
                        .fill(lit ? (model.keyboardLit ? Theme.Row.selected.color : Theme.Row.hover.color) : .clear)
                        .padding(.horizontal, Theme.space("m"))
                )
                .columnRow(id)
                .contextMenu {
                    Button(pinned ? "Unpin" : "Pin") { model.togglePin(p) }
                    // A bot is found by its herdr name: it is renamed in its settings.
                    if fleet.bot(for: p) == nil { Button("Rename…") { model.startRename(p) } }
                    Button("Focus in herdr") { model.focus(p) }
                    Divider()
                    Button("End agent…") { model.askEnd(p) }
                }
                .accessibilityAction(named: pinned ? "Unpin" : "Pin") { model.togglePin(p) }
                .accessibilityAction(named: "Focus in herdr") { model.focus(p) }
                .accessibilityAction(named: "Rename") { model.startRename(p) }
            }
        }
        .id(pinnedSection ? "pinned:\(key)" : key)
        .parityAnchor(pinnedSection ? "list.row.pinned[\(key)]" : "list.row[\(key)]")
    }
}
