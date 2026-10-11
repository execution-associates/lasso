import SwiftUI

/// The fleet panel: the same row anatomy as the card (state glyph, name,
/// second line), grouped machine → repo → agent, pins on top.
public struct ListView: View {
    @ObservedObject var model: ListModel
    @ObservedObject var fleet: FleetModel

    public init(model: ListModel) {
        self.model = model
        fleet = model.fleet
    }

    private var pad: CGFloat { Theme.Card.pad.pt }

    public var body: some View {
        let size = Theme.List.size.size
        VStack(spacing: 0) {
            HStack {
                Text("Fleet")
                    .font(Theme.Fonts.name.font)
                    .foregroundStyle(Theme.Text.primary.color)
                Text("\(fleet.agents.count) agents")
                    .font(Theme.Fonts.secondLine.font)
                    .foregroundStyle(Theme.Text.secondary.color)
                Spacer()
                Button { model.onClose() } label: { Keycap(Keys.esc) }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Close (esc)")
            }
            .padding(.horizontal, pad)
            .frame(height: Theme.Card.headerHeight.pt)

            ScrollViewReader { proxy in
                ScrollView(.vertical) {
                    VStack(alignment: .leading, spacing: 0) {
                        if !model.pinned.isEmpty {
                            header("Pinned", anchor: "list.header.pinned")
                            ForEach(model.pinned) { row($0, pinnedSection: true) }
                        }
                        ForEach(Array(model.groups.enumerated()), id: \.element.id) { gi, g in
                            header(g.machine, anchor: "list.header.machine[\(gi)]")
                            ForEach(g.repos) { r in
                                Text(r.name)
                                    .font(Theme.Fonts.tree.font)
                                    .foregroundStyle(Theme.Text.secondary.color)
                                    .padding(.horizontal, Theme.Row.pad.number("h"))
                                    .padding(.top, 4)
                                    .accessibilityAddTraits(.isHeader)
                                ForEach(r.agents) { row($0, pinnedSection: false) }
                            }
                        }
                        if fleet.agents.isEmpty {
                            Text("No agents running")
                                .font(Theme.Fonts.secondLine.font)
                                .foregroundStyle(Theme.Text.secondary.color)
                                .padding(Theme.Row.pad.number("h"))
                        }
                    }
                    .padding(.horizontal, pad - Theme.Row.pad.number("h"))
                    .padding(.bottom, 8)
                }
                .onChange(of: model.selected) { _, key in
                    if let key { proxy.scrollTo(key) }
                }
            }

            Text(model.notice ?? " ")
                .font(Theme.Fonts.secondLine.font)
                .foregroundStyle(Theme.Text.secondary.color)
                .lineLimit(1)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, pad)
                .frame(height: Theme.Card.footerHeight.pt)
                .background(Theme.Card.footer.color)
        }
        .frame(width: size.width, height: size.height)
        .background(Theme.Card.surface.color)
        .clipShape(RoundedRectangle(cornerRadius: Theme.Card.radius.pt, style: .continuous))
        .parityAnchor("list")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Fleet")
    }

    private func header(_ title: String, anchor: String) -> some View {
        Text(title)
            .font(
                .system(size: Theme.List.groupHeaderFont.number("size"), weight: Theme.List.groupHeaderFont.weight))
            .foregroundStyle(Theme.List.groupHeaderFont.textColor)
            .padding(.horizontal, Theme.Row.pad.number("h"))
            .padding(.top, 10)
            .padding(.bottom, 2)
            .parityAnchor(anchor)
            .accessibilityAddTraits(.isHeader)
    }

    private func row(_ p: HostPane, pinnedSection: Bool) -> some View {
        let key = p.key
        let selected = model.selected == key
        let pinned = model.pinnedKeys.contains(key)
        let fill =
            selected ? Theme.Row.selected.color : (model.hovered == key ? Theme.Row.hover.color : Color.clear)
        return Group {
            if model.confirmEnd == key {
                HStack(spacing: 8) {
                    Text("End \(p.displayName)?")
                        .font(Theme.Fonts.name.font)
                        .foregroundStyle(Theme.Text.primary.color)
                        .lineLimit(1)
                    Spacer()
                    Button("End") { model.end(p) }
                        .buttonStyle(.plain)
                        .font(Theme.Fonts.tree.font)
                        .foregroundStyle(Theme.dangerText.color)
                        .padding(.horizontal, Theme.Row.pad.number("h"))
                        .padding(.vertical, 3)
                        .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt).fill(Theme.dangerFill.color))
                        .accessibilityLabel("Confirm: end \(p.displayName)")
                    Button("Cancel") { model.confirmEnd = nil }
                        .buttonStyle(.plain)
                        .font(Theme.Fonts.tree.font)
                        .foregroundStyle(Theme.Text.secondary.color)
                        .accessibilityLabel("Cancel ending \(p.displayName)")
                }
                .padding(.vertical, Theme.Row.pad.number("v"))
                .padding(.horizontal, Theme.Row.pad.number("h"))
            } else {
                Button {
                    model.selected = key
                    model.onOpen(p)
                } label: {
                    HStack(alignment: .top, spacing: 8) {
                        AgentGlyph(slot: .agent(p))
                            .padding(.top, 3)
                        VStack(alignment: .leading, spacing: 1) {
                            HStack(spacing: 4) {
                                Text(p.displayName)
                                    .font(Theme.Fonts.name.font)
                                    .foregroundStyle(Theme.Text.primary.color)
                                    .lineLimit(1)
                                if pinned && !pinnedSection {
                                    Image(systemName: "pin.fill")
                                        .font(.system(size: Theme.Fonts.secondLine.number("size")))
                                        .foregroundStyle(Theme.Text.secondary.color)
                                }
                            }
                            Text([statusPhrase(p, now: model.now()), p.agent].filter { !$0.isEmpty }.joined(separator: " · "))
                                .font(Theme.Fonts.secondLine.font)
                                .foregroundStyle(Theme.Text.secondary.color)
                                .lineLimit(1)
                        }
                        Spacer(minLength: 0)
                    }
                    .padding(.vertical, Theme.Row.pad.number("v"))
                    .padding(.horizontal, Theme.Row.pad.number("h"))
                    .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt).fill(fill))
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .onHover { model.hovered = $0 ? key : (model.hovered == key ? nil : model.hovered) }
                .contextMenu {
                    Button(pinned ? "Unpin" : "Pin") { model.togglePin(p) }
                    Button("Focus in herdr") { model.focus(p) }
                    Divider()
                    Button("End agent…") { model.askEnd(p) }
                }
                .accessibilityLabel("\(p.displayName), \(p.state.label), \(p.agent) on \(p.host_label)\(pinned ? ", pinned" : "")")
                .accessibilityAction(named: pinned ? "Unpin" : "Pin") { model.togglePin(p) }
                .accessibilityAction(named: "Focus in herdr") { model.focus(p) }
                .accessibilityAction(named: "End agent") { model.askEnd(p) }
            }
        }
        .id(pinnedSection ? "pinned:\(key)" : key)
        .parityAnchor(pinnedSection ? "list.row.pinned[\(key)]" : "list.row[\(key)]")
    }
}
