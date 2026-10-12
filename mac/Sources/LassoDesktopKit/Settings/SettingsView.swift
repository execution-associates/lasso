import AppKit
import SwiftUI

/// Settings, in the panels' language and macOS's grouped layout: three tabs,
/// each a column of titled groups, a group a card of rows split by hairlines,
/// a row its name (and why) leading and its control trailing.
public struct SettingsView: View {
    @ObservedObject var model: SettingsModel
    @ObservedObject var auth = AuthState.shared

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
            tabs
                .padding(.horizontal, pad)
                .padding(.top, Theme.space("l"))

            ScrollView(.vertical) {
                VStack(alignment: .leading, spacing: Theme.space("5xl")) {
                    switch model.tab {
                    case .notifications: notificationsTab
                    case .shortcuts: shortcutsTab
                    case .info: infoTab
                    }
                }
                .padding(.horizontal, pad)
                .padding(.vertical, Theme.space("2xl"))
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            footer
        }
        .panelChrome(Theme.Card.size.size, anchor: "settings")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Settings")
    }

    // MARK: tabs

    private func icon(_ t: SettingsTab) -> String {
        switch t {
        case .notifications: "bell"
        case .shortcuts: "keyboard"
        case .info: "info.circle"
        }
    }

    /// The tabs, as a segmented row: the chosen one lifted onto its own pill.
    private var tabs: some View {
        HStack(spacing: Theme.space("2xs")) {
            ForEach(SettingsTab.allCases, id: \.self) { t in
                let on = t == model.tab
                Button { model.tab = t } label: {
                    HStack(spacing: Theme.space("s")) {
                        Image(systemName: icon(t))
                            .font(.system(size: Theme.icon.number("pin"), weight: .semibold))
                            .foregroundStyle(on ? Theme.accent.color : Theme.Text.secondary.color)
                        Text(t.title)
                            .font(Theme.Fonts.small.font.weight(on ? .semibold : .medium))
                            .foregroundStyle(on ? Theme.Text.primary.color : Theme.Text.secondary.color)
                    }
                    .frame(maxWidth: .infinity)
                    .frame(height: Theme.NewAgent.layout.number("button_h"))
                    .background {
                        if on {
                            Capsule().fill(Theme.Control.round.color("fill"))
                                .overlay(Capsule().strokeBorder(Theme.Panel.divider.color, lineWidth: 1))
                                .shadow(
                                    color: .black.opacity(Theme.NewAgent.layout.number("tint") * 3), radius: Theme.space("xs"),
                                    y: Theme.space("hair"))
                        }
                    }
                    .contentShape(Capsule())
                }
                .buttonStyle(.plain)
                .accessibilityLabel(t.title)
                .accessibilityAddTraits(on ? .isSelected : [])
                .parityAnchor("settings.tab[\(t.rawValue)]")
            }
        }
        .padding(Theme.space("2xs"))
        .background(Capsule().fill(Theme.Panel.inner.color("fill")))
        .animation(.easeOut(duration: Theme.Motion.cardOpen.number("duration")), value: model.tab)
    }

    // MARK: the grouped layout

    /// A group: its title above, a card of rows, a footnote below.
    private func group<C: View>(_ title: String?, footnote: String? = nil, @ViewBuilder _ rows: () -> C) -> some View {
        let inner = Theme.Panel.inner
        let shape = RoundedRectangle(cornerRadius: inner.number("radius"), style: .continuous)
        return VStack(alignment: .leading, spacing: Theme.space("s")) {
            if let title {
                Text(title)
                    .font(Theme.Fonts.header.font)
                    .foregroundStyle(Theme.Text.secondary.color)
                    .padding(.horizontal, Theme.space("xs"))
                    .accessibilityAddTraits(.isHeader)
            }
            VStack(alignment: .leading, spacing: 0) { rows() }
                .background(shape.fill(inner.color("fill")))
                .overlay(shape.strokeBorder(Theme.Panel.divider.color, lineWidth: 1))
                .clipShape(shape)
            if let footnote, !footnote.isEmpty {
                Text(footnote)
                    .font(Theme.Fonts.small.font)
                    .foregroundStyle(Theme.Text.secondary.color)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.horizontal, Theme.space("xs"))
            }
        }
    }

    /// A row: its name and why on the leading side, its control trailing,
    /// and a hairline under it unless it is the group's last.
    private func row<T: View>(
        _ title: String, detail: String? = nil, last: Bool = false, @ViewBuilder trailing: () -> T
    ) -> some View {
        VStack(spacing: 0) {
            HStack(alignment: .center, spacing: Theme.space("l")) {
                VStack(alignment: .leading, spacing: Theme.space("2xs")) {
                    Text(title).font(Theme.Fonts.body.font).foregroundStyle(Theme.Text.primary.color)
                    if let detail {
                        Text(detail).font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                Spacer(minLength: Theme.space("l"))
                trailing()
            }
            .padding(.horizontal, Theme.space("xl"))
            .padding(.vertical, Theme.space("l"))
            if !last { hairline }
        }
    }

    /// A row whose control is a set of choices under its name.
    private func stackedRow<C: View>(
        _ title: String, detail: String? = nil, last: Bool = false, @ViewBuilder _ content: () -> C
    ) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            VStack(alignment: .leading, spacing: Theme.space("m")) {
                VStack(alignment: .leading, spacing: Theme.space("2xs")) {
                    Text(title).font(Theme.Fonts.body.font).foregroundStyle(Theme.Text.primary.color)
                    if let detail {
                        Text(detail).font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                content()
            }
            .padding(.horizontal, Theme.space("xl"))
            .padding(.vertical, Theme.space("l"))
            .frame(maxWidth: .infinity, alignment: .leading)
            if !last { hairline }
        }
    }

    /// A one-line field in the address row's style.
    private func field<F: View>(_ f: F, label: String) -> some View {
        f.textFieldStyle(.plain)
            .font(Theme.Fonts.body.font)
            .foregroundStyle(Theme.Text.primary.color)
            .padding(.horizontal, Theme.space("l"))
            .frame(height: Theme.NewAgent.layout.number("chip_h"))
            .background(Capsule().fill(Theme.Panel.surface.color))
            .accessibilityLabel(label)
    }

    private var hairline: some View {
        Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair")).padding(.leading, Theme.space("xl"))
    }

    private func value(_ s: String) -> some View {
        Text(s).font(Theme.Fonts.body.font).foregroundStyle(Theme.Text.secondary.color)
    }

    private func toggle(_ on: Bool, _ label: String, _ act: @escaping () -> Void) -> some View {
        Switch(on: on, label: label, showsLabel: false, anchor: "settings.switch[\(label)]", toggle: act)
    }

    private func choice(_ title: String, on: Bool, label: String, _ act: @escaping () -> Void) -> some View {
        PillButton(title: title, primary: on, label: label, action: act)
            .accessibilityAddTraits(on ? .isSelected : [])
    }

    private func dot(_ color: Color, anchor: String) -> some View {
        Circle().fill(color).frame(width: Theme.space("m"), height: Theme.space("m")).parityAnchor(anchor)
    }

    // MARK: Notifications

    @ViewBuilder private var notificationsTab: some View {
        group("Sound", footnote: model.soundNotice) {
            stackedRow("Arrival sound", detail: "Plays when something new lands in the inbox. Picking one plays it.", last: model.upload == nil) {
                HStack(spacing: Theme.space("s")) {
                    ForEach(InboxSound.all.map { ($0.id, $0.name) } + [(InboxSound.none, "None")], id: \.0) { id, name in
                        choice(name, on: model.sound == id, label: id == InboxSound.none ? "No sound" : "Play \(name) when something arrives") {
                            model.choose(sound: id)
                        }
                    }
                    if let c = model.customSound() {
                        choice(c.name, on: model.sound == InboxSound.custom, label: "Play your sound, \(c.name), when something arrives") {
                            model.choose(sound: InboxSound.custom)
                        }
                    }
                }
            }
            if model.upload != nil {
                row("Your own sound", detail: "Kept on lasso, so every Mac using it gets it.", last: true) {
                    HStack(spacing: Theme.space("s")) {
                        if model.customSound() != nil {
                            PillButton(title: "Remove", label: "Remove your sound from lasso") { model.removeSound() }
                        }
                        PillButton(title: model.customSound() == nil ? "Upload…" : "Replace…", label: "Upload your own sound to lasso") {
                            model.pickSound()
                        }
                    }
                }
                .parityAnchor("settings.sound.upload")
            }
        }
        if let n = model.notify() {
            group("Notify me about", footnote: "A kind that is off makes no sound on this Mac. The inbox still lists everything waiting.") {
                ForEach(Array(n.kinds.enumerated()), id: \.element.id) { i, k in
                    let on = n.wants(k.id)
                    row(k.label, detail: k.description, last: i == n.kinds.count - 1) {
                        toggle(on, "\(k.label)") { model.setNotify(k.id, !on) }
                    }
                    .parityAnchor("settings.notify[\(k.id)]")
                }
            }
        } else {
            group("Notify me about", footnote: "This lasso has no notification kinds yet, so every arrival sounds.") { EmptyView() }
        }
    }

    // MARK: Shortcuts

    @ViewBuilder private var shortcutsTab: some View {
        let sc = model.shortcut
        group("Inbox", footnote: model.recording ? "Press the combination, with ⌘, ⌥ or ⌃. esc cancels." : nil) {
            stackedRow("Open the inbox", detail: "From any app.", last: !model.needsAccessibility) {
                HStack(spacing: Theme.space("s")) {
                    let fallback = InboxShortcutChoice.fallback
                    choice(fallback.label, on: sc == fallback, label: "\(fallback.label) opens the inbox") { model.choose(fallback) }
                    ForEach([InboxShortcutChoice.Modifier.option, .command, .control], id: \.self) { m in
                        choice("\(m.symbol)\(m.symbol)", on: sc == .doubleTap(m), label: "Double \(m.rawValue) opens the inbox") {
                            model.choose(.doubleTap(m))
                        }
                    }
                    if case .combo = sc, sc != fallback, !model.recording {
                        choice(sc.label, on: true, label: "\(sc.label) opens the inbox") { model.startRecording() }
                    }
                    choice(model.recording ? "Press keys…" : "Record…", on: model.recording, label: "Record a key combination") {
                        model.startRecording()
                    }
                    choice("Off", on: sc == .off, label: "No inbox shortcut") { model.choose(.off) }
                }
                .parityAnchor("settings.shortcut")
            }
            if model.needsAccessibility {
                row("A double tap needs Accessibility", detail: "It reads modifier keys in every app. Allow LassoDesktop there, then it works.", last: true) {
                    PillButton(title: "Open", label: "Open Accessibility settings") { model.permissions.openAccessibilitySettings() }
                }
                .parityAnchor("settings.shortcut.accessibility")
            }
        }
        group(
            "New agents",
            footnote: model.recordingSlot.map { $0 != .inbox } == true
                ? "Press the combination, with ⌘, ⌥ or ⌃. esc cancels."
                : "From any app. While a LassoDesktop window has the keyboard, ⌘O and ⇧⌘O do the same."
        ) {
            ForEach(Array([ShortcutSlot.newAgent, .newScratch].enumerated()), id: \.element) { i, slot in
                let s = model.shortcuts[slot] ?? slot.fallback
                row(slot.title, last: i == 1) {
                    HStack(spacing: Theme.space("s")) {
                        choice(model.recordingSlot == slot ? "Press keys…" : (s == .off ? "Record…" : s.label), on: model.recordingSlot == slot,
                               label: "\(slot.title): \(s == .off ? "no shortcut" : s.label). Record another") {
                            model.startRecording(slot)
                        }
                        choice("Off", on: s == .off, label: "\(slot.title): no shortcut") { model.choose(.off, for: slot) }
                    }
                }
                .parityAnchor("settings.shortcut[\(slot.rawValue)]")
            }
        }
        group("Permissions") {
            let p = model.permissions
            let state = p.accessibility
            row("Accessibility", detail: "Only for a double-tap inbox shortcut, which reads modifier keys in every app.", last: true) {
                HStack(spacing: Theme.space("m")) {
                    dot(
                        state == .granted ? Theme.State.done.color : (state == .denied ? Theme.State.blocked.color : Theme.State.idle.color),
                        anchor: "settings.permission[Accessibility]")
                    value(state.label)
                    if state != .granted {
                        PillButton(title: "Settings", label: "Settings: Accessibility") { p.openAccessibilitySettings() }
                    }
                }
            }
            .accessibilityElement(children: .contain)
            .accessibilityLabel("Accessibility: \(state.label)")
        }
    }

    // MARK: Info

    @ViewBuilder private var infoTab: some View {
        let c = LassoChoice(url: model.baseURL(), version: model.version(), name: model.lassoName())
        group("lasso", footnote: model.connectNote) {
            if auth.expired != nil {
                row("lasso signed this Mac out", detail: "Everything stays as it was until you sign in.") {
                    PillButton(title: "Sign in again", primary: true, label: "Sign in to lasso again") { model.signInAgain() }
                }
                .parityAnchor("settings.signin_again")
            }
            VStack(spacing: 0) {
                HStack(alignment: .center, spacing: Theme.space("l")) {
                    dot(c.supported ? Theme.State.done.color : Theme.State.blocked.color, anchor: "settings.lasso[\(c.url)]")
                    VStack(alignment: .leading, spacing: Theme.space("2xs")) {
                        Text(c.title).font(Theme.Fonts.name.font.weight(.semibold)).foregroundStyle(Theme.Text.primary.color)
                        Text(c.detail).font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color)
                    }
                    Spacer(minLength: Theme.space("l"))
                    if !model.changingLasso {
                        PillButton(title: "Change…", label: "Connect this Mac to another lasso") { model.changingLasso = true }
                    }
                }
                .padding(.horizontal, Theme.space("xl"))
                .padding(.vertical, Theme.space("l"))
                .accessibilityElement(children: .contain)
                .accessibilityLabel("\(c.title), \(c.detail)")
                hairline
            }
            if model.changingLasso {
                VStack(spacing: 0) {
                    HStack(spacing: Theme.space("m")) {
                        TextField("Address, e.g. lasso.example.com", text: $model.urlDraft)
                            .textFieldStyle(.plain)
                            .font(Theme.Fonts.body.font)
                            .foregroundStyle(Theme.Text.primary.color)
                            .padding(.horizontal, Theme.space("l"))
                            .frame(height: Theme.NewAgent.layout.number("chip_h"))
                            .background(Capsule().fill(Theme.Panel.surface.color))
                            .onSubmit { model.connect() }
                            .accessibilityLabel("lasso address")
                        PillButton(title: model.connecting ? "Connecting…" : "Connect", primary: true, label: "Connect to the lasso at this address") {
                            model.connect()
                        }
                        PillButton(title: "Cancel", label: "Keep this lasso") { model.cancelChange() }
                    }
                    .padding(.horizontal, Theme.space("xl"))
                    .padding(.vertical, Theme.space("l"))
                    hairline
                }
                if let asking = model.passwordFor {
                    VStack(spacing: 0) {
                        HStack(spacing: Theme.space("m")) {
                            field(TextField("User", text: $model.userDraft), label: "UI_AUTH user for \(LassoConnector.shown(asking))")
                            field(SecureField("Password", text: $model.passwordDraft), label: "UI_AUTH password")
                                .onSubmit { model.submitPassword() }
                            PillButton(title: "Sign in", primary: true, label: "Sign in to this lasso with its user and password") {
                                model.submitPassword()
                            }
                        }
                        .padding(.horizontal, Theme.space("xl"))
                        .padding(.vertical, Theme.space("l"))
                        hairline
                    }
                    .parityAnchor("settings.password")
                }
            }
            if let v = model.version(), let h = v.herdr_version {
                row("herdr") { value(h) }
            }
            if let h = model.usableHosts() {
                row("Machines it drives") { value("\(h.usable) of \(h.total)") }
            }
            if model.signedIn {
                row("Signed in on this Mac") {
                    PillButton(title: "Sign out", label: "Sign out of this lasso") { model.signOut() }
                }
            }
            row("In the web app", detail: "Agents, themes, plugins and everything else lasso has.", last: true) {
                PillButton(title: "Advanced Settings", label: "Open lasso's settings in the browser") { model.openAdvancedSettings() }
            }
        }
        group("LassoDesktop", footnote: AppUpdates.checkNow == nil ? "A development build: it does not update itself." : nil) {
            row("Version") {
                HStack(spacing: Theme.space("m")) {
                    value(AppUpdates.version)
                    if let check = AppUpdates.checkNow {
                        PillButton(title: "Check for updates", label: "Check for a newer LassoDesktop") { check() }
                    }
                }
            }
            row("Show in the Dock", detail: "With a menu bar of Settings and Quit.") {
                toggle(model.inDock, "Show in the Dock") { model.setDock(!model.inDock) }
            }
            row("Show in the menu bar", detail: "The monogram, with the inbox's count.", last: true) {
                toggle(model.inMenuBar, "Show in the menu bar") { model.setMenuBar(!model.inMenuBar) }
            }
        }
        group(nil) {
            row("Quit LassoDesktop", detail: "Until the next login. The rail goes with it.", last: true) {
                PillButton(title: "Quit", textColor: Theme.dangerText.color, label: "Quit LassoDesktop") { model.quit() }
            }
        }
    }

    // MARK: footer

    /// Who makes it: the Execution Associates lockup, linking to the site.
    private var footer: some View {
        let f = Theme.Settings.footer
        return VStack(spacing: 0) {
            Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair"))
            HStack {
                Button { NSWorkspace.shared.open(Self.site) } label: {
                    Group {
                        if let logo = Self.lockup {
                            Image(nsImage: logo).renderingMode(.template).resizable().scaledToFit()
                        } else {
                            Text("Execution Associates").font(Theme.Fonts.small.font)
                        }
                    }
                    .frame(height: f.number("logo_height"))
                    .foregroundStyle(Theme.Text.secondary.color)
                    .parityAnchor("settings.footer.logo")
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Execution Associates website")
                .help("execution.associates")
                Spacer()
            }
            .padding(.horizontal, pad)
            .padding(.vertical, f.number("pad_v"))
        }
    }

    static let site = URL(string: "https://execution.associates")!

    /// The bundled lockup (Resources/Brand), a white mask tinted like text.
    static let lockup: NSImage? = {
        guard let url = Bundle.main.resourceURL?.appendingPathComponent("Brand/exa-lockup@3x.png"),
              let image = NSImage(contentsOf: url)
        else { return nil }
        image.isTemplate = true
        return image
    }()
}
