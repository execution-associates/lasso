import AppKit
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
                    section("Inbox") { inboxSection }
                    section("Permissions") { permissionsSection }
                }
                .padding(.horizontal, pad)
                .padding(.vertical, Theme.space("2xl"))
            }
            footer
        }
        .panelChrome(Theme.Card.size.size, anchor: "settings")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Settings")
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

    @ViewBuilder private var inboxSection: some View {
        let sc = model.shortcut
        VStack(alignment: .leading, spacing: Theme.space("s")) {
            Text("Shortcut").font(Theme.Fonts.body.font).foregroundStyle(Theme.Text.primary.color)
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

            Text("Sound").font(Theme.Fonts.body.font).foregroundStyle(Theme.Text.primary.color)
                .padding(.top, Theme.space("s"))
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

    private func choice(_ title: String, on: Bool, label: String, _ act: @escaping () -> Void) -> some View {
        PillButton(title: title, primary: on, label: label, action: act)
            .accessibilityAddTraits(on ? .isSelected : [])
    }

    @ViewBuilder private var permissionsSection: some View {
        let p = model.permissions
        permissionRow(
            "Screen Recording", "S attaches a screenshot to a message.", p.screenRecording,
            action: p.screenRecording == .granted ? nil : "Allow"
        ) { model.allowScreenRecording() }
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
