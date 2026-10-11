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

    private var pad: CGFloat { Theme.Card.pad.pt }

    public var body: some View {
        let size = Theme.Card.size.size
        let _ = model.tick
        VStack(spacing: 0) {
            HStack {
                Text("Set up Lasso")
                    .font(Theme.Fonts.name.font)
                    .foregroundStyle(Theme.Text.primary.color)
                Spacer()
                Button { model.finish() } label: { Keycap(Keys.esc) }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Close setup (esc)")
            }
            .padding(.horizontal, pad)
            .frame(height: Theme.Card.headerHeight.pt)

            ScrollView(.vertical) {
                VStack(alignment: .leading, spacing: 14) {
                    section("lasso") { lassoSection }
                    section("Assistant") { assistantSection }
                    section("Voice") { voiceSection }
                    section("Permissions") { permissionsSection }
                }
                .padding(.horizontal, pad)
                .padding(.bottom, 12)
            }

            HStack(spacing: 10) {
                Text(model.notice ?? setup.status ?? "")
                    .font(Theme.Fonts.secondLine.font)
                    .foregroundStyle(Theme.Text.secondary.color)
                    .lineLimit(2)
                    .frame(maxWidth: .infinity, alignment: .leading)
                Button { model.finish() } label: {
                    HStack(spacing: 6) {
                        Text("Done")
                            .font(Theme.Fonts.tree.font)
                            .foregroundStyle(Theme.Text.primary.color)
                        Keycap(Keys.send)
                    }
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Done (⏎)")
            }
            .padding(.horizontal, pad)
            .frame(height: Theme.Card.footerHeight.pt)
            .background(Theme.Card.footer.color)
            .parityAnchor("onboarding.footer")
        }
        .frame(width: size.width, height: size.height)
        .background(Theme.Card.surface.color)
        .clipShape(RoundedRectangle(cornerRadius: Theme.Card.radius.pt, style: .continuous))
        .parityAnchor("onboarding")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Set up Lasso")
    }

    private func section<C: View>(_ title: String, @ViewBuilder _ content: () -> C) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(title)
                .font(
                    .system(size: Theme.List.groupHeaderFont.number("size"), weight: Theme.List.groupHeaderFont.weight))
                .foregroundStyle(Theme.List.groupHeaderFont.textColor)
                .accessibilityAddTraits(.isHeader)
            content()
        }
    }

    private func line(_ s: String, secondary: Bool = false) -> some View {
        Text(s)
            .font(secondary ? Theme.Fonts.secondLine.font : Theme.Fonts.tree.font)
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
            HStack(spacing: 8) {
                AgentGlyph(slot: .agent(botPane(bot)))
                line("\(bot.name) · \(bot.runtime) · \(bot.state)")
            }
            line(setup.shared ? "Shared with lasso's web client" : "Saved on this Mac: this lasso cannot share the choice", secondary: true)
        } else {
            line("No assistant chosen yet. Pick a bot, or make one.", secondary: true)
            ForEach(fleet.bots) { bot in
                Button { Task { await setup.choose(bot.name) } } label: {
                    HStack(spacing: 8) {
                        AgentGlyph(slot: .agent(botPane(bot)))
                        Text(bot.name).font(Theme.Fonts.name.font).foregroundStyle(Theme.Text.primary.color)
                        Text("\(bot.runtime) · \(bot.state)")
                            .font(Theme.Fonts.secondLine.font)
                            .foregroundStyle(Theme.Text.secondary.color)
                        Spacer()
                    }
                    .padding(.vertical, Theme.Row.pad.number("v"))
                    .padding(.horizontal, Theme.Row.pad.number("h"))
                    .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt).fill(Theme.Row.hover.color))
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Use \(bot.name) as the assistant")
            }
            Button { Task { await setup.createDefault() } } label: {
                Text(setup.working ? "Creating…" : "Create an assistant bot")
                    .font(Theme.Fonts.tree.font)
                    .foregroundStyle(Theme.Text.primary.color)
                    .padding(.vertical, Theme.Row.pad.number("v"))
                    .padding(.horizontal, Theme.Row.pad.number("h"))
                    .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt).fill(Theme.accent.color))
            }
            .buttonStyle(.plain)
            .disabled(setup.working)
            .parityAnchor("onboarding.create")
            .accessibilityLabel("Create an assistant bot on lasso")
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
        HStack(spacing: 2) {
            ForEach(VoiceEngine.allCases, id: \.self) { e in
                let on = model.engine == e
                Button { model.select(e) } label: {
                    Text(e == .parakeet ? "Parakeet" : e.displayName)
                        .font(Theme.Fonts.tree.font)
                        .foregroundStyle(Theme.Text.primary.color)
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 5)
                        .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt - 2).fill(on ? Theme.accent.color : .clear))
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .parityAnchor(on ? "onboarding.segment.selected" : "onboarding.segment[\(e.rawValue)]")
                .accessibilityLabel("\(e.displayName)\(on ? ", selected" : "")")
            }
        }
        .padding(2)
        .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt).fill(Theme.Row.hover.color))
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
            HStack(spacing: 8) {
                SecureField("API key", text: $model.keyDraft)
                    .textFieldStyle(.plain)
                    .font(Theme.Fonts.tree.font)
                    .foregroundStyle(Theme.Text.primary.color)
                    .padding(.horizontal, 8)
                    .frame(height: Theme.Card.composer.number("height"))
                    .background(
                        RoundedRectangle(cornerRadius: Theme.Card.composer.number("radius"))
                            .fill(RGBA(parsing: Theme.Card.composer.value["fill"]?.string ?? "")?.color ?? .clear))
                    .onSubmit { model.saveKey() }
                    .accessibilityLabel("\(model.engine.displayName) API key")
                Button("Save") { model.saveKey() }
                    .buttonStyle(.plain)
                    .font(Theme.Fonts.tree.font)
                    .foregroundStyle(Theme.Text.primary.color)
                    .accessibilityLabel("Save the key")
                if set {
                    Button("Remove") { model.removeKey() }
                        .buttonStyle(.plain)
                        .font(Theme.Fonts.tree.font)
                        .foregroundStyle(Theme.dangerText.color)
                        .accessibilityLabel("Remove the key")
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
        HStack(alignment: .top, spacing: 8) {
            Circle()
                .fill(state == .granted ? Theme.State.done.color : (state == .denied ? Theme.State.blocked.color : Theme.State.idle.color))
                .frame(width: 8, height: 8)
                .parityAnchor("onboarding.permission[\(name)]")
                .padding(.top, 5)
            VStack(alignment: .leading, spacing: 1) {
                HStack(spacing: 6) {
                    Text(name).font(Theme.Fonts.tree.font).foregroundStyle(Theme.Text.primary.color)
                    Text(state.label).font(Theme.Fonts.secondLine.font).foregroundStyle(Theme.Text.secondary.color)
                }
                Text(why).font(Theme.Fonts.secondLine.font).foregroundStyle(Theme.Text.secondary.color)
            }
            Spacer()
            if let action {
                Button(action, action: act)
                    .buttonStyle(.plain)
                    .font(Theme.Fonts.tree.font)
                    .foregroundStyle(Theme.Text.primary.color)
                    .padding(.horizontal, Theme.Row.pad.number("h"))
                    .padding(.vertical, 3)
                    .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt).fill(Theme.Card.keycapFill.color))
                    .accessibilityLabel("\(action): \(name)")
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel("\(name): \(state.label)")
    }
}
