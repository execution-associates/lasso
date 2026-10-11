import SwiftUI

/// Settings, in the panels' language: the lasso this app talks to, and the
/// permissions.
public struct SettingsView: View {
    @ObservedObject var model: SettingsModel

    public init(model: SettingsModel) { self.model = model }

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
                    section("Permissions") { permissionsSection }
                }
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
        let current = model.baseURL()
        let list = model.lassos.isEmpty ? [LassoChoice(url: current, version: model.version())] : model.lassos
        ForEach(list) { c in lassoRow(c, inUse: c.url == current) }
        if let h = model.usableHosts() { line("\(h.usable) of \(h.total) machines usable", secondary: true) }
        if model.version() == nil { line("Not answering yet. Is the tunnel up?", secondary: true) }
    }

    /// One lasso: what it is, where, and Use for the ones not in use.
    private func lassoRow(_ c: LassoChoice, inUse: Bool) -> some View {
        HStack(alignment: .top, spacing: Theme.space("m")) {
            Circle()
                .fill(c.version == nil ? Theme.State.blocked.color : Theme.State.done.color)
                .frame(width: Theme.space("m"), height: Theme.space("m"))
                .parityAnchor("settings.lasso[\(c.url)]")
                .padding(.top, Theme.space("xs"))
            VStack(alignment: .leading, spacing: Theme.space("hair")) {
                HStack(spacing: Theme.space("s")) {
                    Text(c.title).font(Theme.Fonts.body.font).foregroundStyle(Theme.Text.primary.color)
                    if inUse { Text("In use").font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color) }
                }
                Text(c.detail).font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color)
            }
            Spacer()
            if !inUse, c.version != nil {
                PillButton(title: "Use", label: "Use \(c.title) at \(c.detail)") { model.use(c) }
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel("\(c.title), \(c.detail)\(inUse ? ", in use" : "")")
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
