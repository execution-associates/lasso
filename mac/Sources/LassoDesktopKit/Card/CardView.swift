import SwiftUI

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

/// An agent's small avatar: a symbol by agent kind, with its state as a
/// dot on the corner (ref a2 identity line, a3 rows).
struct AgentAvatar: View {
    let agent: String
    let state: AgentState
    var size: CGFloat = Theme.List.row.number("avatar")

    var body: some View {
        let symbol =
            switch agent {
            case "claude": "asterisk"
            case "codex": "chevron.left.forwardslash.chevron.right"
            case "omp", "pi": "circle.hexagongrid"
            default: "terminal"
            }
        Image(systemName: symbol)
            .font(.system(size: size * Theme.avatar.number("glyph"), weight: .semibold))
            .foregroundStyle(Theme.Text.primary.color)
            .frame(width: size, height: size)
            .background(RoundedRectangle(cornerRadius: size * Theme.avatar.number("radius")).fill(Theme.Control.round.color("fill")))
            .overlay(alignment: .bottomTrailing) {
                Circle()
                    .fill(StateStyle.color(state))
                    .frame(width: size * Theme.avatar.number("dot"), height: size * Theme.avatar.number("dot"))
                    .overlay(Circle().strokeBorder(Theme.Panel.surface.color, lineWidth: Theme.avatar.number("ring")))
                    .offset(x: size * Theme.avatar.number("dot_offset"), y: size * Theme.avatar.number("dot_offset"))
            }
            .accessibilityHidden(true)
    }
}

public struct CardView: View {
    @ObservedObject var model: CardModel
    @ObservedObject var fleet: FleetModel

    public init(model: CardModel) {
        self.model = model
        fleet = model.fleet
    }

    private var inset: CGFloat { Theme.Panel.inset.pt }
    private var contentX: CGFloat { Theme.Card.contentX.pt }

    public var body: some View {
        let pane = model.current
        let pages = model.pages
        let index = pages.firstIndex { $0.key == pane.key } ?? 0
        ZStack(alignment: .topLeading) {
            VStack(alignment: .leading, spacing: 0) {
                header(pane)
                identity(pane)
                    .padding(.top, Theme.Layout.card.number("identity_gap"))
                Group {
                    conversation
                        .killingDim(model, failure: true)
                }
                .id(pane.key)
                .transition(
                    .asymmetric(
                        insertion: .opacity.combined(with: .offset(y: 12 * CGFloat(model.pageDirection))),
                        removal: .opacity))
                .padding(.top, Theme.Layout.card.number("box_gap"))
                ComposerRow(model: model, attach: model.attachFile)
                .padding(.top, Theme.Layout.card.number("composer_gap"))
                Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair"))
                    .parityAnchor("card.divider")
                    .padding(.top, Theme.Layout.card.number("divider_gap"))
                footer
                    .padding(.top, Theme.Layout.card.number("footer_gap"))
            }
            .padding(.leading, contentX)
            .padding(.trailing, inset)
            .padding(.top, Theme.Layout.card.number("top"))
            .padding(.bottom, Theme.Layout.card.number("bottom"))

            // The paging column: K above, J below, then the unread bar.
            VStack(spacing: Theme.Layout.card.number("column_gap")) {
                RoundButton(symbol: "chevron.up", enabled: pages.count > 1 && index > 0, label: "Previous card (K)") {
                    model.page(-1)
                }
                RoundButton(
                    symbol: "chevron.down", enabled: pages.count > 1 && index < pages.count - 1, label: "Next card (J)"
                ) { model.page(1) }
                unreadBar(pages)
                    .padding(.top, Theme.Layout.card.number("unread_top"))
            }
            .padding(.leading, Theme.Layout.card.number("column_x"))
            .padding(.top, Theme.Layout.card.number("top"))
        }
        .panelChrome(Theme.Card.size.size, anchor: "card")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("\(pane.displayName), \(pane.state.label)")
    }

    // MARK: header and identity

    private func header(_ pane: HostPane) -> some View {
        HStack(spacing: Theme.space("m")) {
            AgentTitle(model: model, anchor: "card.title")
                .padding(.leading, Theme.Card.titleX.pt - contentX)
            Spacer(minLength: 8)
            if model.canShowInHerdr {
                RoundButton(symbol: "terminal", label: "Show \(pane.displayName) in herdr (T)") { model.showInHerdr() }
                    .help("Show \(pane.displayName) in herdr")
                    .parityAnchor("card.herdr")
            }
            RoundButton(symbol: "xmark", label: "Close (esc)") { model.onClose() }
                .parityAnchor("card.close")
        }
        .frame(height: Theme.Control.round.number("size"))
    }

    private func identity(_ pane: HostPane) -> some View {
        let group = pane.repo_label ?? pane.repo ?? pane.workspace_label
        return HStack(spacing: Theme.space("m")) {
            AgentAvatar(agent: pane.agent, state: pane.state, size: Theme.Layout.card.number("avatar"))
            Text([statusPhrase(pane, now: model.now()), group].filter { !$0.isEmpty }.joined(separator: " · "))
                .font(Theme.Fonts.body.font)
                .foregroundStyle(Theme.Text.secondary.color)
                .lineLimit(1)
            // Always named: "local" is lasso's machine, rarely this Mac.
            if !pane.host_label.isEmpty {
                Text(pane.host_label)
                    .font(Theme.Fonts.small.font)
                    .foregroundStyle(Theme.Text.primary.color)
                    .padding(.horizontal, Theme.Card.machineChip.number("pad_h"))
                    .padding(.vertical, Theme.Card.machineChip.number("pad_v"))
                    .background(
                        RoundedRectangle(cornerRadius: Theme.Card.machineChip.number("radius"))
                            .fill(Theme.Card.machineChip.color("fill"))
                    )
                    .parityAnchor("card.machine_chip")
                    .accessibilityLabel("on \(pane.host_label)")
            }
            Spacer(minLength: 0)
        }
        .padding(.leading, Theme.Card.titleX.pt - contentX)
        .parityAnchor("card.identity")
    }

    // MARK: unread bar

    private func unreadBar(_ pages: [HostPane]) -> some View {
        let seg = Theme.Card.unreadSegment
        let dot = Theme.Card.unreadDot
        return VStack(spacing: Theme.Card.unreadGap.pt) {
            ForEach(Array(pages.enumerated()), id: \.element.key) { i, p in
                Group {
                    if p.key == model.pane.key {
                        Capsule().fill(seg.color("color")).frame(width: seg.number("w"), height: seg.number("h"))
                    } else {
                        Circle()
                            .fill(dot.color("color").opacity(model.read.contains(p.key) ? 0.45 : 1))
                            .frame(width: dot.number("size"), height: dot.number("size"))
                    }
                }
                .parityAnchor("card.unread_bar.segment[\(i)]")
            }
        }
        .frame(width: Theme.Control.round.number("size"))
        .accessibilityElement()
        .accessibilityLabel("Card \((pages.firstIndex { $0.key == model.pane.key } ?? 0) + 1) of \(pages.count)")
    }

    // MARK: conversation

    private var conversation: some View {
        let inner = Theme.Panel.inner
        return ScrollViewReader { proxy in
            ScrollView(.vertical) {
                VStack(alignment: .leading, spacing: Theme.space("s")) {
                    if let chat = model.chat {
                        ForEach(chat.items.suffix(80)) { item in
                            ChatRow(item: item).id(item.id)
                        }
                        if let p = model.pending { PendingRow(pending: p) }
                        if let says = model.phase.says {
                            WaitingRow(text: says)
                        } else if chat.items.isEmpty {
                            placeholder(chat.unavailable.map { "No conversation (\($0.reason))" } ?? "No messages yet")
                        }
                    } else if let err = model.loadError {
                        placeholder("Could not read the conversation: \(err)")
                    } else {
                        WaitingRow(text: "Loading the conversation")
                    }
                }
                .padding(inner.number("pad"))
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .onChange(of: model.chat?.items.last?.id) { _, last in
                if let last { proxy.scrollTo(last, anchor: .bottom) }
            }
            .onAppear {
                if let last = model.chat?.items.last?.id { proxy.scrollTo(last, anchor: .bottom) }
            }
        }
        .frame(maxHeight: .infinity)
        .background(RoundedRectangle(cornerRadius: inner.number("radius"), style: .continuous).fill(inner.color("fill")))
        .clipShape(RoundedRectangle(cornerRadius: inner.number("radius"), style: .continuous))
        .parityAnchor("card.conversation")
    }

    private func placeholder(_ s: String) -> some View {
        Text(s)
            .font(Theme.Fonts.body.font)
            .foregroundStyle(Theme.Text.secondary.color)
    }

    // MARK: footer: next steps and the end actions

    private var footer: some View {
        let steps = model.nextSteps
        return VStack(alignment: .leading, spacing: Theme.space("l")) {
            HStack(spacing: Theme.space("l")) {
                Text("Next steps")
                    .font(Theme.Fonts.label.font)
                    .foregroundStyle(Theme.Text.secondary.color)
                Spacer(minLength: 8)
                if !model.isBot {
                    PillButton(
                        title: model.killing ? "Killing…" : model.killArmed ? "Confirm kill" : "Kill agent",
                        textColor: Theme.dangerText.color, enabled: !model.killing, busy: model.killing,
                        label: model.killing
                            ? "Killing \(model.current.displayName)"
                            : model.killArmed ? "Confirm kill agent (X)" : "Kill agent (X)"
                    ) { model.kill() }
                    .parityAnchor("action.kill")
                }
                if model.actions.canAcknowledge {
                    PillButton(
                        title: "Acknowledge", primary: true, enabled: model.canAcknowledgeNow,
                        hint: "Send or clear your reply first", label: "Acknowledge (\(Keys.acknowledge.legend))"
                    ) { model.acknowledge() }
                    .parityAnchor("action.acknowledge")
                }
            }
            .padding(.leading, Theme.Card.titleX.pt - contentX)
            if steps.isEmpty, !model.blockedWithoutChoices, !model.allowsCustom {
                Text(model.notice ?? (model.chat == nil ? " " : "No open question to answer."))
                    .font(Theme.Fonts.body.font)
                    .foregroundStyle(Theme.Text.secondary.color)
                    .lineLimit(1)
                    .padding(.leading, Theme.Card.titleX.pt - contentX)
            } else if steps.isEmpty {
                AnswerExtras(model: model)
            } else {
                VStack(alignment: .leading, spacing: Theme.space("2xs")) {
                    ForEach(steps.prefix(3)) { step in
                        let on = (model.highlighted ?? 0) == step.index
                        Button { model.highlighted = step.index } label: {
                            HStack(spacing: Theme.space("m")) {
                                Text("\(step.index + 1).")
                                    .foregroundStyle(Theme.Text.secondary.color)
                                Text(step.label)
                                    .foregroundStyle(Theme.Text.primary.color)
                                    .lineLimit(1)
                                    .layoutPriority(1)
                                if let d = step.detail, !d.isEmpty {
                                    Text(d)
                                        .foregroundStyle(Theme.Text.secondary.color)
                                        .lineLimit(1)
                                }
                                Spacer(minLength: 8)
                                if on {
                                    // The selected choice: → answers with it, ↳ edits it.
                                    Image(systemName: "arrow.right")
                                        .font(.system(size: Theme.icon.number("step"), weight: .semibold))
                                        .foregroundStyle(Theme.Text.secondary.color)
                                }
                            }
                            .font(Theme.Fonts.body.font)
                            .padding(.vertical, Theme.space("2xs"))
                            .padding(.horizontal, Theme.Card.titleX.pt - contentX)
                            .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt).fill(on ? Theme.Row.selected.color : .clear))
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel("Step \(step.index + 1): \(step.label)")
                        .accessibilityHint("Right arrow answers with it; tab edits it first")
                    }
                }
                .opacity(model.killing ? Theme.Card.killing.number("dim") : 1)
                .parityAnchor("card.steps")
            }
            // With choices or the answer fields on screen, the line above that
            // carries notices is not drawn, so an answer, send or stop that
            // failed would leave no trace. Say it under them.
            if !(steps.isEmpty && !model.blockedWithoutChoices && !model.allowsCustom), let n = model.notice {
                Text(n)
                    .font(Theme.Fonts.small.font)
                    .foregroundStyle(Theme.Text.secondary.color)
                    .lineLimit(2)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.leading, Theme.Card.titleX.pt - contentX)
                    .accessibilityLabel(n)
                    .parityAnchor("card.notice")
            }
        }
        .parityAnchor("card.footer")
    }
}

/// One row of a conversation: user and agent text in the body face, a tool
/// call as a one-line card.
struct ChatRow: View {
    let item: ChatItem
    var font: Font = Theme.Fonts.body.font

    var body: some View {
        switch item.kind {
        case "tool":
            if let ask = item.tool?.ask { AskRow(ask: ask, font: font) } else if let t = item.tool { ToolRow(tool: t) }
        case "marker":
            Text("— \(item.marker ?? "marker") —")
                .font(font)
                .foregroundStyle(Theme.Text.secondary.color)
        case "user" where TaskNotification.parse(item.text ?? "") != nil:
            TaskNotificationRow(id: item.id, note: TaskNotification.parse(item.text ?? "")!)
        case "incoming":
            ChannelCard(id: item.id, msg: ChannelMessage.parse(item.text ?? "", source: item.source))
        case "user":
            Text("› " + (item.text ?? ""))
                .font(font)
                .foregroundStyle(Theme.Text.primary.color)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        default:
            if item.thinking == true {
                Text(item.text ?? "")
                    .font(font)
                    .foregroundStyle(Theme.Text.secondary.color)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
            } else {
                MarkdownView(text: item.text ?? "", font: font).frame(maxWidth: .infinity, alignment: .leading)
            }
        }
    }
}

/// A question put to the human, in the agent's own words: its header, the
/// question, and once answered, what was picked. The options themselves are
/// the footer's next steps.
struct AskRow: View {
    let ask: ChatTool.Ask
    var font: Font = Theme.Fonts.body.font

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.space("s")) {
            ForEach(Array(ask.questions.enumerated()), id: \.offset) { _, q in
                VStack(alignment: .leading, spacing: Theme.space("2xs")) {
                    if let h = q.header, !h.isEmpty {
                        Text(h)
                            .font(Theme.Fonts.label.font)
                            .foregroundStyle(Theme.Text.secondary.color)
                    }
                    Text(q.question ?? "")
                        .font(font)
                        .foregroundStyle(Theme.Text.primary.color)
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    if let picked = q.selected, !picked.isEmpty {
                        Text("→ " + picked.joined(separator: ", "))
                            .font(font)
                            .foregroundStyle(Theme.Text.secondary.color)
                    } else if let c = q.custom, !c.isEmpty {
                        Text("→ " + c)
                            .font(font)
                            .foregroundStyle(Theme.Text.secondary.color)
                    }
                }
            }
        }
        .parityAnchor("card.ask")
        .accessibilityElement(children: .combine)
    }
}

/// What an omp bot waits on (`pending_ui`): its title and message.
struct PendingRow: View {
    let pending: ChatPayload.PendingUI
    var font: Font = Theme.Fonts.body.font

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.space("2xs")) {
            Text(pending.title ?? "Waiting on you")
                .font(font)
                .foregroundStyle(Theme.Text.primary.color)
            if let m = pending.message, !m.isEmpty {
                Text(m)
                    .font(font)
                    .foregroundStyle(Theme.Text.secondary.color)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .textSelection(.enabled)
        .accessibilityElement(children: .combine)
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
        HStack(spacing: Theme.space("s")) {
            Circle().fill(StateStyle.color(state)).frame(width: r.number("dot"), height: r.number("dot"))
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
        .padding(.horizontal, r.number("pad_h"))
        .frame(height: r.number("height"))
        .background(RoundedRectangle(cornerRadius: r.number("radius")).fill(RGBA(parsing: r.value["fill"]?.string ?? "")?.color ?? .clear))
        .accessibilityElement(children: .combine)
    }
}
