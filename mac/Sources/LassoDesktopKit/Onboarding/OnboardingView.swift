import SwiftUI

/// Onboarding, in the card's language.
public struct OnboardingView: View {
    @ObservedObject var model: OnboardingModel
    @ObservedObject var setup: AssistantSetup
    @ObservedObject var fleet: FleetModel

    public init(model: OnboardingModel) {
        self.model = model
        setup = model.setup
        fleet = model.setup.fleet
    }

    private var pad: CGFloat { Theme.Panel.inset.pt }

    public var body: some View {
        let _ = model.tick
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                Text("Set up Lasso")
                    .font(Theme.Fonts.title.font)
                    .foregroundStyle(Theme.Text.primary.color)
                Spacer()
                RoundButton(symbol: "xmark", label: "Close setup (esc)") { model.finish() }
                    .keyHint(Keys.esc)
            }
            .padding(.horizontal, pad)
            .padding(.top, Theme.Layout.card.number("top"))

            ScrollView(.vertical) {
                VStack(alignment: .leading, spacing: Theme.space("4xl")) {
                    section("lasso") { lassoSection }
                    section("Assistant") { assistantSection }
                    section("Voice") { voiceSection }
                    section("Permissions") { permissionsSection }
                }
                .padding(.horizontal, pad)
                .padding(.vertical, Theme.space("2xl"))
            }

            Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair"))
            HStack(spacing: Theme.space("l")) {
                Text(model.notice ?? setup.status ?? "")
                    .font(Theme.Fonts.small.font)
                    .foregroundStyle(Theme.Text.secondary.color)
                    .lineLimit(2)
                    .frame(maxWidth: .infinity, alignment: .leading)
                PillButton(title: "Done", primary: true, label: "Done (⏎)") { model.finish() }
                    .keyHint(Keys.send)
            }
            .padding(.horizontal, pad)
            .padding(.vertical, Theme.space("2xl"))
            .parityAnchor("onboarding.footer")
        }
        .panelChrome(Theme.Card.size.size, anchor: "onboarding")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Set up Lasso")
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

    // MARK: sections

    @ViewBuilder private var lassoSection: some View {
        if let v = model.version() {
            line("\(model.baseURL) · lasso \(v.lasso_version)")
            if let h = model.usableHosts() { line("\(h.usable) of \(h.total) machines usable", secondary: true) }
        } else {
            line("\(model.baseURL)")
            line("Not answering yet. Is the tunnel up?", secondary: true)
        }
    }

    @ViewBuilder private var assistantSection: some View {
        if let bot = setup.assistant {
            HStack(spacing: Theme.space("m")) {
                AgentGlyph(slot: .agent(botPane(bot)))
                line("\(bot.name) · \(bot.runtime) · \(bot.state)")
            }
            line(setup.shared ? "Shared with lasso's web client" : "Saved on this Mac: this lasso cannot share the choice", secondary: true)
        } else {
            line("No assistant chosen yet. Pick a bot, or make one.", secondary: true)
            ForEach(fleet.bots) { bot in
                Button { Task { await setup.choose(bot.name) } } label: {
                    HStack(spacing: Theme.space("m")) {
                        AgentGlyph(slot: .agent(botPane(bot)))
                        Text(bot.name).font(Theme.Fonts.name.font).foregroundStyle(Theme.Text.primary.color)
                        Text("\(bot.runtime) · \(bot.state)")
                            .font(Theme.Fonts.small.font)
                            .foregroundStyle(Theme.Text.secondary.color)
                        Spacer()
                    }
                    .padding(.horizontal, Theme.space("xl"))
                    .frame(height: Theme.List.row.number("height"))
                    .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt).fill(Theme.Panel.inner.color("fill")))
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Use \(bot.name) as the assistant")
            }
            PillButton(
                title: setup.working ? "Creating…" : "Create an assistant bot", primary: true,
                label: "Create an assistant bot on lasso"
            ) { Task { await setup.createDefault() } }
            .disabled(setup.working)
            .parityAnchor("onboarding.create")
        }
    }

    /// A bot drawn as a fleet row's glyph.
    private func botPane(_ b: BotView) -> HostPane {
        HostPane(
            host: b.host, host_label: b.host, pane_id: "", workspace_id: "", workspace_label: b.name, tab_id: "",
            tab_label: "", cwd: "", agent: b.runtime,
            agent_status: b.state == "blocked" ? "blocked" : (b.state == "working" ? "working" : "idle"), has_agent: true,
            focused: false)
    }

    @ViewBuilder private var voiceSection: some View {
        HStack(spacing: Theme.space("2xs")) {
            ForEach(VoiceEngine.allCases, id: \.self) { e in
                let on = model.engine == e
                Button { model.select(e) } label: {
                    Text(e == .parakeet ? "Parakeet" : e.displayName)
                        .font(Theme.Fonts.body.font)
                        .foregroundStyle(Theme.Text.primary.color)
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, Theme.space("xs"))
                        .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt - 2).fill(on ? Theme.accent.color : .clear))
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .parityAnchor(on ? "onboarding.segment.selected" : "onboarding.segment[\(e.rawValue)]")
                .accessibilityLabel("\(e.displayName)\(on ? ", selected" : "")")
            }
        }
        .padding(Theme.space("2xs"))
        .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt).fill(Theme.Panel.inner.color("fill")))
        switch model.engine {
        case .lasso:
            if let t = model.transcribe {
                line(
                    t.available
                        ? "lasso transcribes with \(t.provider ?? "?")\(t.model.map { " (\($0))" } ?? ""); this Mac holds no key."
                        : (t.reason ?? "lasso cannot transcribe right now."), secondary: true)
            } else {
                line(model.transcribeNote ?? "Asking lasso…", secondary: true)
            }
        case .groq, .openai:
            let set = model.keyAccount.map(Keychain.isSet) ?? false
            line(set ? "\(model.engine.displayName) key is in the Keychain. Audio goes from this Mac to \(model.engine.displayName)." : "Add your \(model.engine.displayName) API key. It is kept in the Keychain.", secondary: true)
            HStack(spacing: Theme.space("m")) {
                SecureField("API key", text: $model.keyDraft)
                    .textFieldStyle(.plain)
                    .font(Theme.Fonts.body.font)
                    .foregroundStyle(Theme.Text.primary.color)
                    .padding(.horizontal, Theme.space("2xl"))
                    .frame(height: Theme.Control.composer.number("height"))
                    .background(Capsule().fill(Theme.Control.composer.color("fill")))
                    .onSubmit { model.saveKey() }
                    .accessibilityLabel("\(model.engine.displayName) API key")
                PillButton(title: "Save", label: "Save the key") { model.saveKey() }
                if set {
                    PillButton(title: "Remove", textColor: Theme.dangerText.color, label: "Remove the key") {
                        model.removeKey()
                    }
                }
            }
        case .parakeet:
            line("On-device Parakeet is not built yet.", secondary: true)
        }
    }

    @ViewBuilder private var permissionsSection: some View {
        let p = model.permissions
        permissionRow(
            "Microphone", "Hold V to talk to your assistant.", p.microphone, action: p.microphone == .granted ? nil : "Allow"
        ) { model.allowMicrophone() }
        permissionRow(
            "Screen Recording", "S attaches a screenshot to a message.", p.screenRecording,
            action: p.screenRecording == .granted ? nil : "Allow"
        ) { model.allowScreenRecording() }
        permissionRow(
            "Accessibility", "Optional: only for push-to-talk while another app is in front.", p.accessibility,
            action: p.accessibility == .granted ? nil : "Settings"
        ) { p.openAccessibilitySettings() }
    }

    private func permissionRow(
        _ name: String, _ why: String, _ state: PermissionState, action: String?, _ act: @escaping () -> Void
    ) -> some View {
        HStack(alignment: .top, spacing: Theme.space("m")) {
            Circle()
                .fill(state == .granted ? Theme.State.done.color : (state == .denied ? Theme.State.blocked.color : Theme.State.idle.color))
                .frame(width: Theme.space("m"), height: Theme.space("m"))
                .parityAnchor("onboarding.permission[\(name)]")
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
