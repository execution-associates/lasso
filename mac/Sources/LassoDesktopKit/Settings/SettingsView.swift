import AppKit
import SwiftUI

/// Settings, in the panels' language: the lasso this app talks to, and the
/// permissions.
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
                VStack(alignment: .leading, spacing: Theme.space("4xl")) {
                    switch model.tab {
                    case .notifications:
                        section("Sound") { soundSection }
                        section("Notify me about") { notifySection }
                    case .shortcuts:
                        section("Open the inbox") { inboxShortcutSection }
                        section("New agents") { newAgentSection }
                        section("Permissions") { permissionsSection }
                    case .info: infoSection
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

    /// The tabs, as a segmented row: the chosen one lifted.
    private var tabs: some View {
        HStack(spacing: Theme.space("2xs")) {
            ForEach(SettingsTab.allCases, id: \.self) { t in
                let on = t == model.tab
                Button { model.tab = t } label: {
                    Text(t.title)
                        .font(Theme.Fonts.small.font.weight(on ? .semibold : .regular))
                        .foregroundStyle(on ? Theme.Text.primary.color : Theme.Text.secondary.color)
                        .frame(maxWidth: .infinity)
                        .frame(height: Theme.NewAgent.layout.number("switch_h"))
                        .background {
                            if on {
                                Capsule().fill(Theme.Control.round.color("fill"))
                                    .overlay(Capsule().strokeBorder(Theme.Panel.divider.color, lineWidth: 1))
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
    }

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
        if auth.expired != nil {
            HStack(spacing: Theme.space("m")) {
                Text("lasso signed this Mac out.").font(Theme.Fonts.body.font).foregroundStyle(Theme.Text.primary.color)
                Spacer()
                PillButton(title: "Sign in again", primary: true, label: "Sign in to lasso again") { model.signInAgain() }
            }
            .parityAnchor("settings.signin_again")
        }
        HStack(alignment: .top) {
            lassoRow(LassoChoice(url: current, version: model.version(), name: model.lassoName()))
            if !model.changingLasso {
                PillButton(title: "Change…", label: "Connect this Mac to another lasso") { model.changingLasso = true }
            }
        }
        if model.changingLasso {
            HStack(spacing: Theme.space("m")) {
            TextField("Address, e.g. lasso.example.com", text: $model.urlDraft)
                .textFieldStyle(.plain)
                .font(Theme.Fonts.body.font)
                .foregroundStyle(Theme.Text.primary.color)
                .padding(.horizontal, Theme.space("l"))
                .frame(height: Theme.NewAgent.layout.number("chip_h"))
                .background(Capsule().fill(Theme.Panel.inner.color("fill")))
                .onSubmit { model.connect() }
                .accessibilityLabel("lasso address")
            PillButton(title: model.connecting ? "Connecting…" : "Connect", label: "Connect to the lasso at this address") {
                model.connect()
            }
            PillButton(title: "Cancel", label: "Keep this lasso") {
                model.changingLasso = false
                model.urlDraft = ""
            }
            }
        }
        if model.signedIn {
            HStack {
                line("Signed in on this Mac.", secondary: true)
                Spacer()
                PillButton(title: "Sign out", label: "Sign out of this lasso") { model.signOut() }
            }
        }
        if let note = model.connectNote { line(note, secondary: true) }
    }

    /// The app's version and updates, and the lasso it talks to.
    @ViewBuilder private var infoSection: some View {
        section("lasso") {
            lassoSection
            if let v = model.version(), let h = v.herdr_version { line("herdr \(h)", secondary: true) }
            if let h = model.usableHosts() { line("Drives \(h.usable) of \(h.total) machines", secondary: true) }
        }
        section("LassoDesktop") {
            HStack {
                line("Version \(AppUpdates.version)")
                Spacer()
                if let check = AppUpdates.checkNow {
                    PillButton(title: "Check for updates", label: "Check for a newer LassoDesktop") { check() }
                }
            }
            if AppUpdates.checkNow == nil { line("A development build: it does not update itself.", secondary: true) }
        }
    }

    /// The lasso: what it is and where.
    private func lassoRow(_ c: LassoChoice) -> some View {
        HStack(alignment: .top, spacing: Theme.space("m")) {
            Circle()
                .fill(c.version == nil ? Theme.State.blocked.color : Theme.State.done.color)
                .frame(width: Theme.space("m"), height: Theme.space("m"))
                .parityAnchor("settings.lasso[\(c.url)]")
                .padding(.top, Theme.space("xs"))
            VStack(alignment: .leading, spacing: Theme.space("hair")) {
                HStack(spacing: Theme.space("s")) {
                    Text(c.title).font(Theme.Fonts.body.font).foregroundStyle(Theme.Text.primary.color)
                }
                Text(c.detail).font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color)
            }
            Spacer()
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel("\(c.title), \(c.detail)")
    }

    /// The shortcut that opens the inbox.
    @ViewBuilder private var inboxShortcutSection: some View {
        let sc = model.shortcut
        VStack(alignment: .leading, spacing: Theme.space("s")) {
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
            if model.needsAccessibility {
                HStack(alignment: .top, spacing: Theme.space("m")) {
                    line("A double tap needs Accessibility: it reads modifier keys in every app. Allow LassoDesktop there, then it works.", secondary: true)
                    Spacer()
                    PillButton(title: "Open", label: "Open Accessibility settings") { model.permissions.openAccessibilitySettings() }
                }
                .parityAnchor("settings.shortcut.accessibility")
            } else if model.recording {
                line("Press the combination, with ⌘, ⌥ or ⌃. esc cancels.", secondary: true)
            }
        }
    }

    /// What an arrival sounds like, and which kinds may interrupt this Mac.
    @ViewBuilder private var soundSection: some View {
        VStack(alignment: .leading, spacing: Theme.space("s")) {
            HStack(spacing: Theme.space("s")) {
                ForEach(InboxSound.all.map { ($0.id, $0.name) } + [(InboxSound.none, "None")], id: \.0) { id, name in
                    choice(name, on: model.sound == id, label: id == InboxSound.none ? "No sound" : "Play \(name) when something arrives") {
                        model.choose(sound: id)
                    }
                }
            }
            if model.upload != nil {
                HStack(spacing: Theme.space("s")) {
                    if let c = model.customSound() {
                        choice(c.name, on: model.sound == InboxSound.custom, label: "Play your sound, \(c.name), when something arrives") {
                            model.choose(sound: InboxSound.custom)
                        }
                        PillButton(title: "Remove", label: "Remove your sound from lasso") { model.removeSound() }
                    }
                    PillButton(title: model.customSound() == nil ? "Upload your own…" : "Replace…", label: "Upload your own sound to lasso") {
                        model.pickSound()
                    }
                }
                .parityAnchor("settings.sound.upload")
            }
            if let n = model.soundNotice { line(n, secondary: true) }
            line("Plays when something new lands in the inbox. Picking one plays it. Your own sound is kept on lasso, so every Mac using it gets it.", secondary: true)
        }
    }

    @ViewBuilder private var notifySection: some View {
        if let n = model.notify() {
            ForEach(n.kinds) { k in notifyRow(k, on: n.wants(k.id)) }
            line("On this Mac, a kind that is off makes no sound. The inbox still lists everything waiting.", secondary: true)
        } else {
            line("This lasso has no notification kinds yet: every arrival sounds.", secondary: true)
        }
    }

    /// ⌥⇧O and ⌃⇧O by default, each the user's to record anew or turn off.
    @ViewBuilder private var newAgentSection: some View {
        ForEach([ShortcutSlot.newAgent, .newScratch], id: \.self) { slot in
            let sc = model.shortcuts[slot] ?? slot.fallback
            HStack(spacing: Theme.space("m")) {
                Text(slot.title).font(Theme.Fonts.body.font).foregroundStyle(Theme.Text.primary.color)
                Spacer()
                choice(model.recordingSlot == slot ? "Press keys…" : (sc == .off ? "Record…" : sc.label), on: model.recordingSlot == slot,
                       label: "\(slot.title): \(sc == .off ? "no shortcut" : sc.label). Record another") {
                    model.startRecording(slot)
                }
                choice("Off", on: sc == .off, label: "\(slot.title): no shortcut") { model.choose(.off, for: slot) }
            }
            .parityAnchor("settings.shortcut[\(slot.rawValue)]")
        }
        line(
            model.recordingSlot.map { $0 != .inbox } == true
                ? "Press the combination, with ⌘, ⌥ or ⌃. esc cancels."
                : "These work from any app. While a LassoDesktop window has the keyboard, ⌘O and ⇧⌘O do the same.",
            secondary: true)
    }

    private func notifyRow(_ k: NotifyKind, on: Bool) -> some View {
        HStack(alignment: .top, spacing: Theme.space("m")) {
            VStack(alignment: .leading, spacing: Theme.space("hair")) {
                Text(k.label).font(Theme.Fonts.body.font).foregroundStyle(Theme.Text.primary.color)
                Text(k.description).font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Spacer()
            choice(on ? "On" : "Off", on: on, label: "\(k.label): \(on ? "on, tap to turn off" : "off, tap to turn on")") {
                model.setNotify(k.id, !on)
            }
        }
        .parityAnchor("settings.notify[\(k.id)]")
    }

    private func choice(_ title: String, on: Bool, label: String, _ act: @escaping () -> Void) -> some View {
        PillButton(title: title, primary: on, label: label, action: act)
            .accessibilityAddTraits(on ? .isSelected : [])
    }

    @ViewBuilder private var permissionsSection: some View {
        let p = model.permissions
        permissionRow(
            "Accessibility", "Only for a double-tap inbox shortcut, which reads modifier keys in every app.", p.accessibility,
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
