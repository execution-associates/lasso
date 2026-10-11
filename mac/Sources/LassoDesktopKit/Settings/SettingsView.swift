import SwiftUI

/// Settings, in the panels' language: the lasso this app talks to, the
/// default bot, and the permissions.
public struct SettingsView: View {
    @ObservedObject var model: SettingsModel
    @ObservedObject var setup: BotSetup
    @ObservedObject var fleet: FleetModel

    public init(model: SettingsModel) {
        self.model = model
        setup = model.setup
        fleet = model.setup.fleet
    }

    private var pad: CGFloat { Theme.Panel.inset.pt }

    public var body: some View {
        let _ = model.tick
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                Text("Settings")
                    .font(Theme.Fonts.title.font)
                    .foregroundStyle(Theme.Text.primary.color)
                Spacer()
                RoundButton(symbol: "xmark", label: "Close settings (esc)") { model.close() }
                    .keyHint(Keys.esc)
            }
            .padding(.horizontal, pad)
            .padding(.top, Theme.Layout.card.number("top"))

            ScrollView(.vertical) {
                VStack(alignment: .leading, spacing: Theme.space("4xl")) {
                    section("lasso") { lassoSection }
                    section("Default bot") { botSection }
                    section("Permissions") { permissionsSection }
                }
                .padding(.horizontal, pad)
                .padding(.vertical, Theme.space("2xl"))
            }

            if let s = setup.status {
                Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair"))
                Text(s)
                    .font(Theme.Fonts.small.font)
                    .foregroundStyle(Theme.Text.secondary.color)
                    .lineLimit(2)
                    .padding(.horizontal, pad)
                    .padding(.vertical, Theme.space("2xl"))
            }
        }
        .panelChrome(Theme.Card.size.size, anchor: "settings")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Settings")
    }

    private func section<C: View>(_ title: String, @ViewBuilder _ content: () -> C) -> some View {
        VStack(alignment: .leading, spacing: Theme.space("s")) {
            Text(title)
                .font(Theme.Fonts.header.font)
                .foregroundStyle(Theme.Text.secondary.color)
                .accessibilityAddTraits(.isHeader)
            content()
        }
    }

    private func line(_ s: String, secondary: Bool = false) -> some View {
        Text(s)
            .font(secondary ? Theme.Fonts.small.font : Theme.Fonts.body.font)
            .foregroundStyle(secondary ? Theme.Text.secondary.color : Theme.Text.primary.color)
            .fixedSize(horizontal: false, vertical: true)
    }

    @ViewBuilder private var lassoSection: some View {
        if let v = model.version() {
            line("\(model.baseURL) · lasso \(v.lasso_version)")
            if let h = model.usableHosts() { line("\(h.usable) of \(h.total) machines usable", secondary: true) }
        } else {
            line("\(model.baseURL)")
            line("Not answering yet. Is the tunnel up?", secondary: true)
        }
    }

    @ViewBuilder private var botSection: some View {
        if let bot = setup.defaultBot {
            HStack(spacing: Theme.space("m")) {
                AgentAvatar(agent: bot.runtime, state: botState(bot))
                line("\(bot.name) · \(bot.runtime) · \(bot.state)")
            }
            line(
                setup.shared ? "Shared with lasso's web client" : "Saved on this Mac: this lasso cannot share the choice",
                secondary: true)
        } else {
            line("No default bot yet. Pick one of your bots, or make one.", secondary: true)
        }
        ForEach(fleet.bots.filter { $0.name != setup.defaultBot?.name }) { bot in
            Button { Task { await setup.choose(bot.name) } } label: {
                HStack(spacing: Theme.space("m")) {
                    AgentAvatar(agent: bot.runtime, state: botState(bot))
                    Text(bot.name).font(Theme.Fonts.name.font).foregroundStyle(Theme.Text.primary.color)
                    Text("\(bot.runtime) · \(bot.state)")
                        .font(Theme.Fonts.small.font)
                        .foregroundStyle(Theme.Text.secondary.color)
                    Spacer()
                    Text("Make default")
                        .font(Theme.Fonts.small.font)
                        .foregroundStyle(Theme.Text.secondary.color)
                }
                .padding(.horizontal, Theme.space("xl"))
                .frame(height: Theme.List.row.number("height"))
                .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt).fill(Theme.Panel.inner.color("fill")))
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Make \(bot.name) the default bot")
        }
        if setup.defaultBot == nil {
            PillButton(
                title: setup.working ? "Creating…" : "Create a bot", primary: true, label: "Create a bot on lasso"
            ) { Task { await setup.createDefault() } }
            .disabled(setup.working)
            .parityAnchor("settings.create")
        }
    }

    private func botState(_ b: BotView) -> AgentState {
        switch b.state {
        case "blocked": .blocked
        case "working": .working
        default: .idle
        }
    }

    @ViewBuilder private var permissionsSection: some View {
        let p = model.permissions
        permissionRow(
            "Screen Recording", "S attaches a screenshot to a message.", p.screenRecording,
            action: p.screenRecording == .granted ? nil : "Allow"
        ) { model.allowScreenRecording() }
        permissionRow(
            "Accessibility", "Optional: not used yet.", p.accessibility,
            action: p.accessibility == .granted ? nil : "Settings"
        ) { p.openAccessibilitySettings() }
    }

    private func permissionRow(
        _ name: String, _ why: String, _ state: PermissionState, action: String?, _ act: @escaping () -> Void
    ) -> some View {
        HStack(alignment: .top, spacing: Theme.space("m")) {
            Circle()
                .fill(
                    state == .granted
                        ? Theme.State.done.color : (state == .denied ? Theme.State.blocked.color : Theme.State.idle.color)
                )
                .frame(width: Theme.space("m"), height: Theme.space("m"))
                .parityAnchor("settings.permission[\(name)]")
                .padding(.top, Theme.space("xs"))
            VStack(alignment: .leading, spacing: Theme.space("hair")) {
                HStack(spacing: Theme.space("s")) {
                    Text(name).font(Theme.Fonts.body.font).foregroundStyle(Theme.Text.primary.color)
                    Text(state.label).font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color)
                }
                Text(why).font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color)
            }
            Spacer()
            if let action {
                PillButton(title: action, label: "\(action): \(name)", action: act)
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel("\(name): \(state.label)")
    }
}
