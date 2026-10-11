import SwiftUI

/// A keycap: drawn from a KeyBinding, never from a string, so its legend is
/// the key the card's handler matches.
public struct Keycap: View {
    let binding: KeyBinding

    public init(_ binding: KeyBinding) { self.binding = binding }

    public var body: some View {
        let min = Theme.Card.keycapMinSize.size
        Text(binding.legend)
            .font(.system(size: Theme.Card.keycapFont.number("size"), weight: Theme.Card.keycapFont.weight))
            .foregroundStyle(Theme.Text.primary.color)
            .padding(.horizontal, Theme.Card.keycapPadH.pt)
            .frame(minWidth: min.width, minHeight: min.height)
            .background(RoundedRectangle(cornerRadius: Theme.Card.keycapRadius.pt).fill(Theme.Card.keycapFill.color))
            .parityAnchor("keycap[\(binding.id)]")
            .accessibilityHidden(true)
    }
}

/// "3m ago" from a unix-millisecond timestamp.
public func relativeAge(_ ms: Int64?, now: Date) -> String? {
    guard let ms else { return nil }
    let s = max(0, Int(now.timeIntervalSince1970 - Double(ms) / 1000))
    switch s {
    case ..<60: return "\(s)s ago"
    case ..<3600: return "\(s / 60)m ago"
    case ..<86400: return "\(s / 3600)h ago"
    default: return "\(s / 86400)d ago"
    }
}

/// The identity line's opening words: what the agent is doing, and since when.
public func statusPhrase(_ p: HostPane, now: Date) -> String {
    let age = relativeAge(p.transcript_at, now: now)
    switch p.state {
    case .done: return age.map { "Finished \($0)" } ?? "Finished"
    case .blocked: return age.map { "Asked \($0)" } ?? "Needs you"
    case .working: return age.map { "Working, updated \($0)" } ?? "Working"
    case .idle: return age.map { "Idle, last active \($0)" } ?? "Idle"
    }
}

public struct CardView: View {
    @ObservedObject var model: CardModel
    @ObservedObject var fleet: FleetModel

    public init(model: CardModel) {
        self.model = model
        fleet = model.fleet
    }

    private var pad: CGFloat { Theme.Card.pad.pt }

    public var body: some View {
        let size = Theme.Card.size.size
        let pane = model.current
        ZStack(alignment: .topLeading) {
            VStack(spacing: 0) {
                header(pane)
                identity(pane)
                    .padding(.horizontal, pad)
                    .padding(.bottom, Theme.Card.sectionGap.pt)
                Group {
                    conversation
                    statusLine
                }
                .id(pane.key)
                .transition(
                    .asymmetric(
                        insertion: .opacity.combined(with: .offset(y: 12 * CGFloat(model.pageDirection))),
                        removal: .opacity))
                steps
                composer
                footer
            }
            unreadBar
                .padding(.leading, Theme.Card.unreadInset.pt)
                .padding(.top, Theme.Card.headerHeight.pt)
        }
        .frame(width: size.width, height: size.height)
        .background(Theme.Card.surface.color)
        .clipShape(RoundedRectangle(cornerRadius: Theme.Card.radius.pt, style: .continuous))
        .parityAnchor("card")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("\(pane.displayName), \(pane.state.label)")
    }

    // MARK: header and identity

    private func header(_ pane: HostPane) -> some View {
        HStack(spacing: 8) {
            Text(pane.displayName)
                .font(Theme.Fonts.name.font)
                .foregroundStyle(Theme.Text.primary.color)
                .lineLimit(1)
            Spacer(minLength: 8)
            Button { model.onClose() } label: { Keycap(Keys.esc) }
                .buttonStyle(.plain)
                .accessibilityLabel("Close (esc)")
        }
        .padding(.horizontal, pad)
        .frame(height: Theme.Card.headerHeight.pt)
    }

    private func identity(_ pane: HostPane) -> some View {
        let repo = pane.repo_label ?? pane.repo ?? pane.workspace_label
        return HStack(spacing: 6) {
            Text([statusPhrase(pane, now: model.now()), pane.agent, repo].filter { !$0.isEmpty }.joined(separator: " · "))
                .font(Theme.Fonts.secondLine.font)
                .foregroundStyle(Theme.Text.secondary.color)
                .lineLimit(1)
            Text(pane.host_label)
                .font(.system(size: Theme.Card.machineChip.number("font_size")))
                .foregroundStyle(Theme.Text.primary.color)
                .padding(.horizontal, Theme.Card.machineChip.number("pad_h"))
                .padding(.vertical, Theme.Card.machineChip.number("pad_v"))
                .background(
                    RoundedRectangle(cornerRadius: Theme.Card.machineChip.number("radius"))
                        .fill(RGBA(parsing: Theme.Card.machineChip.value["fill"]?.string ?? "")?.color ?? .clear)
                )
                .parityAnchor("card.machine_chip")
                .accessibilityLabel("on \(pane.host_label)")
            Spacer(minLength: 0)
        }
        .parityAnchor("card.identity")
    }

    // MARK: unread bar

    private var unreadBar: some View {
        let seg = Theme.Card.unreadSegment
        return VStack(spacing: Theme.Card.unreadGap.pt) {
            ForEach(Array(model.pages.enumerated()), id: \.element.key) { i, p in
                RoundedRectangle(cornerRadius: seg.number("w") / 2)
                    .fill(segmentColor(p))
                    .frame(width: seg.number("w"), height: seg.number("h"))
                    .parityAnchor("card.unread_bar.segment[\(i)]")
            }
        }
        .accessibilityElement()
        .accessibilityLabel("\(model.pages.filter { !model.read.contains($0.key) }.count) unread cards")
    }

    private func segmentColor(_ p: HostPane) -> Color {
        if p.key == model.pane.key { return Theme.Card.unreadCurrent.color }
        if model.read.contains(p.key) { return Theme.Card.unreadRead.color }
        return RGBA(parsing: Theme.Card.unreadSegment.value["color"]?.string ?? "")?.color ?? Theme.accent.color
    }

    // MARK: conversation

    private var conversation: some View {
        ScrollViewReader { proxy in
            ScrollView(.vertical) {
                LazyVStack(alignment: .leading, spacing: 6) {
                    if let chat = model.chat {
                        ForEach(chat.items.suffix(80)) { item in
                            ChatRow(item: item).id(item.id)
                        }
                        if chat.items.isEmpty {
                            placeholder(chat.unavailable.map { "No conversation (\($0.reason))" } ?? "No messages yet")
                        }
                    } else if let err = model.loadError {
                        placeholder("Could not read the conversation: \(err)")
                    } else {
                        placeholder("Loading…")
                    }
                }
                .padding(.horizontal, pad)
                .padding(.vertical, 4)
            }
            .onChange(of: model.chat?.items.last?.id) { _, last in
                if let last { proxy.scrollTo(last, anchor: .bottom) }
            }
            .onAppear {
                if let last = model.chat?.items.last?.id { proxy.scrollTo(last, anchor: .bottom) }
            }
        }
        .frame(maxHeight: .infinity)
        .parityAnchor("card.conversation")
    }

    private func placeholder(_ s: String) -> some View {
        Text(s)
            .font(Theme.Fonts.terminal.font)
            .foregroundStyle(Theme.Text.secondary.color)
    }

    private var statusLine: some View {
        let c = model.chat
        var parts: [String] = []
        if let m = c?.model, !m.isEmpty { parts.append(m) }
        if let t = c?.tokens, t > 0 { parts.append("\(t.formatted()) tokens") }
        parts.append(c?.running == true ? "working" : model.current.state.label)
        return HStack(spacing: 6) {
            Circle().fill(StateStyle.color(model.current.state)).frame(width: 6, height: 6)
            Text(parts.joined(separator: " · "))
                .font(Theme.Fonts.secondLine.font)
                .foregroundStyle(Theme.Text.secondary.color)
                .lineLimit(1)
            Spacer(minLength: 0)
        }
        .padding(.horizontal, pad)
        .padding(.vertical, 6)
        .accessibilityElement(children: .combine)
    }

    // MARK: next steps

    @ViewBuilder private var steps: some View {
        let steps = model.nextSteps
        if !steps.isEmpty {
            VStack(alignment: .leading, spacing: 2) {
                if let q = model.openQuestion?.question {
                    Text(q)
                        .font(Theme.Fonts.secondLine.font)
                        .foregroundStyle(Theme.Text.secondary.color)
                        .lineLimit(2)
                        .padding(.horizontal, Theme.Row.pad.number("h"))
                }
                ForEach(steps) { step in
                    let on = (model.highlighted ?? 0) == step.index
                    Button { model.highlighted = step.index } label: {
                        HStack(spacing: 8) {
                            Text("\(step.index + 1)")
                                .font(Theme.Fonts.terminal.font)
                                .foregroundStyle(Theme.Text.secondary.color)
                            Text(step.label)
                                .font(Theme.Fonts.tree.font)
                                .foregroundStyle(Theme.Text.primary.color)
                                .lineLimit(1)
                            Spacer(minLength: 8)
                            if on {
                                Keycap(Keys.editStep)
                                Keycap(Keys.takeStep)
                            }
                        }
                        .padding(.vertical, Theme.Row.pad.number("v"))
                        .padding(.horizontal, Theme.Row.pad.number("h"))
                        .background(
                            RoundedRectangle(cornerRadius: Theme.Row.radius.pt)
                                .fill(on ? Theme.Row.selected.color : .clear))
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Step \(step.index + 1): \(step.label)")
                    .accessibilityHint("→ answers with it, ↳ edits it first")
                }
            }
            .padding(.horizontal, pad - Theme.Row.pad.number("h"))
            .padding(.bottom, Theme.Card.sectionGap.pt)
            .parityAnchor("card.steps")
        }
    }

    // MARK: composer

    private var composer: some View {
        let c = Theme.Card.composer
        return HStack(spacing: 8) {
            iconButton("camera.viewfinder", Keys.attach) { model.attachScreenshot() }
            ComposerField(model: model)
                .frame(height: c.number("height"))
                .padding(.horizontal, 8)
                .background(
                    RoundedRectangle(cornerRadius: c.number("radius"))
                        .fill(RGBA(parsing: c.value["fill"]?.string ?? "")?.color ?? .clear))
                .parityAnchor("card.composer")
            iconButton("mic", Keys.record) { model.record() }
            iconButton("arrow.up", Keys.send) { model.send() }
        }
        .padding(.horizontal, pad)
        .padding(.bottom, Theme.Card.sectionGap.pt)
    }

    private func iconButton(_ symbol: String, _ key: KeyBinding, _ action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: 4) {
                RailIcon(symbol: symbol)
                Keycap(key)
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(model.busy)
        .accessibilityLabel("\(key.action) (\(key.legend))")
    }

    // MARK: footer

    private var footer: some View {
        HStack(spacing: 10) {
            Button { model.kill() } label: {
                HStack(spacing: 6) {
                    Text(model.killArmed ? "Press X again" : "Kill agent")
                        .font(Theme.Fonts.tree.font)
                        .foregroundStyle(Theme.dangerText.color)
                    Keycap(Keys.kill)
                }
                .padding(.vertical, Theme.Row.pad.number("v") - 2)
                .padding(.horizontal, Theme.Row.pad.number("h"))
                .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt).fill(Theme.dangerFill.color))
                .parityAnchor("action.kill")
            }
            .buttonStyle(.plain)
            .accessibilityLabel(model.killArmed ? "Confirm kill agent (X)" : "Kill agent (X)")
            if model.actions.canDismiss {
                Button { model.discard() } label: {
                    HStack(spacing: 6) {
                        Text("Discard")
                            .font(Theme.Fonts.tree.font)
                            .foregroundStyle(Theme.Text.primary.color)
                        Keycap(Keys.discard)
                    }
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Discard card (E)")
            }
            Text(model.notice ?? "")
                .font(Theme.Fonts.secondLine.font)
                .foregroundStyle(Theme.Text.secondary.color)
                .lineLimit(1)
                .frame(maxWidth: .infinity, alignment: .leading)
            Button { model.page(1) } label: { Keycap(Keys.next) }
                .buttonStyle(.plain)
                .accessibilityLabel("Next card (J)")
            Button { model.page(-1) } label: { Keycap(Keys.previous) }
                .buttonStyle(.plain)
                .accessibilityLabel("Previous card (K)")
        }
        .padding(.horizontal, pad)
        .frame(height: Theme.Card.footerHeight.pt)
        .frame(maxWidth: .infinity)
        .background(Theme.Card.footer.color)
        .parityAnchor("card.footer")
    }
}

/// One row of the conversation: user and agent text in the terminal face,
/// a tool call as a one-line card.
struct ChatRow: View {
    let item: ChatItem

    var body: some View {
        switch item.kind {
        case "tool":
            if let t = item.tool { ToolRow(tool: t) }
        case "marker":
            Text("— \(item.marker ?? "marker") —")
                .font(Theme.Fonts.terminal.font)
                .foregroundStyle(Theme.Text.secondary.color)
        case "user", "incoming":
            Text("› " + (item.text ?? ""))
                .font(Theme.Fonts.terminal.font)
                .foregroundStyle(Theme.Text.primary.color)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        default:
            Text(item.text ?? "")
                .font(Theme.Fonts.terminal.font)
                .foregroundStyle(item.thinking == true ? Theme.Text.secondary.color : Theme.Text.primary.color)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}

struct ToolRow: View {
    let tool: ChatTool

    var body: some View {
        let r = Theme.Card.toolRow
        let state: AgentState =
            switch tool.state {
            case "running": .working
            case "error": .blocked
            default: .done
            }
        HStack(spacing: 6) {
            Circle().fill(StateStyle.color(state)).frame(width: 6, height: 6)
            Text(tool.title ?? tool.name ?? "tool")
                .foregroundStyle(Theme.Text.primary.color)
            Text(tool.subject ?? tool.command ?? "")
                .foregroundStyle(Theme.Text.secondary.color)
                .truncationMode(.middle)
            Spacer(minLength: 4)
            Text(tool.result_line ?? tool.state ?? "")
                .foregroundStyle(Theme.Text.secondary.color)
        }
        .font(Theme.Fonts.terminal.font)
        .lineLimit(1)
        .padding(.horizontal, Theme.Row.pad.number("h"))
        .frame(height: r.number("height"))
        .background(
            RoundedRectangle(cornerRadius: r.number("radius"))
                .fill(RGBA(parsing: r.value["fill"]?.string ?? "")?.color ?? .clear))
        .accessibilityElement(children: .combine)
    }
}
