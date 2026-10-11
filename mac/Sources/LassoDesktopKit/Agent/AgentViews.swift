import AppKit
import SwiftUI

// The pieces an agent's window and its hover preview share: the header, the
// conversation (tool runs folded into one line, messages as bubbles), the
// open question's choices and the footer.

/// Who the harness is, as a person would say it.
func harnessName(_ agent: String) -> String {
    switch agent {
    case "claude": "Claude Code"
    case "omp": "omp"
    case "codex": "Codex"
    case "pi": "pi"
    default: agent.isEmpty ? "Agent" : agent
    }
}

/// "claude-opus-5-5" → "Opus 5.5"; anything else as given.
func modelName(_ model: String?) -> String? {
    guard let m = model, !m.isEmpty else { return nil }
    let parts = m.split(separator: "-").map(String.init)
    guard parts.first == "claude", parts.count >= 3 else { return m }
    let digits = parts.dropFirst(2).prefix { Int($0) != nil }
    return parts[1].capitalized + (digits.isEmpty ? "" : " " + digits.joined(separator: "."))
}

/// The state, the harness, where it runs: "idle · Claude Code in herdr · Agents".
func agentStatusLine(_ p: HostPane) -> String {
    var parts = [p.state.label, "\(harnessName(p.agent)) in herdr", p.group]
    if p.host != "local" { parts.append("on \(p.host_label)") }
    return parts.filter { !$0.isEmpty }.joined(separator: " · ")
}

/// The header: avatar, name, status line, and whatever the caller puts at
/// the end (the window's buttons, the preview's state dot).
struct AgentHeader<Trailing: View>: View {
    let pane: HostPane
    @ViewBuilder var trailing: () -> Trailing

    var body: some View {
        let w = Theme.Agent.window
        HStack(spacing: Theme.space("l")) {
            AgentAvatar(agent: pane.agent, state: pane.state, size: w.number("avatar"))
            VStack(alignment: .leading, spacing: Theme.space("2xs")) {
                Text(pane.displayName)
                    .font(Theme.Fonts.title.font)
                    .foregroundStyle(Theme.Text.primary.color)
                    .lineLimit(1)
                    .parityAnchor("agent.title")
                Text(agentStatusLine(pane))
                    .font(Theme.Fonts.small.font)
                    .foregroundStyle(Theme.Text.secondary.color)
                    .lineLimit(1)
            }
            Spacer(minLength: Theme.space("m"))
            trailing()
        }
        .padding(.horizontal, Theme.Panel.inset.pt)
        .frame(height: w.number("header_h"))
        .accessibilityElement(children: .contain)
    }
}

/// A run of tool calls folded into one line: "⚙ Read · Bash · swift build".
struct ToolRunRow: View {
    let tools: [ChatTool]

    var body: some View {
        let words = tools.map { t in [t.title ?? t.name ?? "tool", t.subject ?? ""].filter { !$0.isEmpty }.joined(separator: " ") }
        Text("⚙︎ " + words.joined(separator: " · "))
            .font(Theme.Fonts.small.font)
            .foregroundStyle(Theme.Text.secondary.color)
            .lineLimit(1)
            .truncationMode(.tail)
            .frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityLabel("Ran \(words.joined(separator: ", "))")
    }
}

/// A message: the agent's in a bubble across the width, inline markdown
/// (bold, code, links) rendered; yours in a bubble on the right.
struct MessageBubble: View {
    let text: String
    let mine: Bool

    var body: some View {
        let b = Theme.Chat.bubble
        Text(Self.styled(text))
            .font(Theme.Fonts.chat.font)
            .foregroundStyle(Theme.Text.primary.color)
            .textSelection(.enabled)
            .padding(.horizontal, b.number("pad_h"))
            .padding(.vertical, b.number("pad_v"))
            .frame(maxWidth: mine ? nil : .infinity, alignment: .leading)
            .background(
                RoundedRectangle(cornerRadius: b.number("radius"), style: .continuous)
                    .fill(mine ? b.color("fill") : Theme.Panel.inner.color("fill")))
            .padding(.leading, mine ? Theme.Layout.chat.number("bubble_inset") : 0)
            .frame(maxWidth: .infinity, alignment: mine ? .trailing : .leading)
    }
}

extension MessageBubble {
    /// Inline markdown (bold, code, links); bold through the face's own
    /// weight axis, semibold as the reference sets it.
    static func styled(_ text: String) -> AttributedString {
        var s =
            (try? AttributedString(markdown: text, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace)))
            ?? AttributedString(text)
        for run in s.runs where run.inlinePresentationIntent?.contains(.stronglyEmphasized) == true {
            s[run.range].font = Theme.Fonts.chat.font(weight: Theme.Fonts.label.weightNumber)
        }
        return s
    }
}

/// When a message was sent, as "Oct 7, 19:36".
func messageTime(_ at: String?) -> String? {
    guard let at else { return nil }
    let iso = ISO8601DateFormatter()
    iso.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    let date = iso.date(from: at) ?? ISO8601DateFormatter().date(from: at)
    guard let date else { return nil }
    let f = DateFormatter()
    f.setLocalizedDateFormatFromTemplate("MMM d HH:mm")
    return f.string(from: date)
}

/// One thing in the conversation, after folding.
enum ConversationEntry: Identifiable {
    case message(ChatItem, text: String, mine: Bool)
    case tools(id: String, [ChatTool])
    case ask(ChatItem, ChatTool.Ask)
    case marker(ChatItem)
    /// Files shown to the human: a SendUserFile call's, or the images an
    /// agent's message wrote as markdown. `version` is the item they came with.
    case media(id: String, version: String, [SharedFile])

    var id: String {
        switch self {
        case .message(let i, _, _), .ask(let i, _), .marker(let i): i.id
        case .tools(let id, _), .media(let id, _, _): id
        }
    }

    /// Fold consecutive tool calls into one entry, and lift media out of
    /// tool calls and messages; thinking is left out. `cwd` resolves a
    /// relative image path.
    static func fold(_ items: [ChatItem], cwd: String? = nil) -> [ConversationEntry] {
        var out: [ConversationEntry] = []
        var run: [ChatTool] = []
        var runID = ""
        func flush() {
            if !run.isEmpty { out.append(.tools(id: runID, run)) }
            run = []
        }
        for item in items {
            if item.kind == "tool", let t = item.tool {
                if let files = t.files, !files.isEmpty {
                    flush()
                    out.append(.media(id: item.id, version: item.id, files))
                } else if let ask = t.ask {
                    flush()
                    out.append(.ask(item, ask))
                } else {
                    if run.isEmpty { runID = item.id }
                    run.append(t)
                }
                continue
            }
            flush()
            switch item.kind {
            case "user", "incoming": out.append(.message(item, text: item.text ?? "", mine: true))
            case "marker": out.append(.marker(item))
            default:
                guard item.thinking != true, let raw = item.text, !raw.isEmpty else { break }
                let (text, images) = extractMarkdownImages(raw, cwd: cwd)
                if !text.isEmpty { out.append(.message(item, text: text, mine: false)) }
                if !images.isEmpty { out.append(.media(id: item.id + "#media", version: item.id, images)) }
            }
        }
        flush()
        return out
    }
}

/// The conversation, newest at the bottom, with the time under the last
/// message of each turn.
struct AgentConversation: View {
    let entries: [ConversationEntry]
    var pending: ChatPayload.PendingUI?
    /// The machine holding the transcript, whose files media are read from.
    var host = "local"

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView(.vertical) {
                VStack(alignment: .leading, spacing: Theme.Layout.chat.number("message_gap")) {
                    ForEach(Array(entries.enumerated()), id: \.element.id) { i, e in
                        entry(e, last: i == entries.count - 1 || isMessage(entries[i + 1]) != isMessage(e))
                            .id(e.id)
                    }
                    if let p = pending { PendingRow(pending: p) }
                    Color.clear.frame(height: Theme.space("hair")).id("end")
                }
                .padding(.horizontal, Theme.Panel.inset.pt)
                .padding(.vertical, Theme.space("m"))
            }
            .onAppear { proxy.scrollTo("end", anchor: .bottom) }
            .onChange(of: entries.last?.id) { _, _ in proxy.scrollTo("end", anchor: .bottom) }
            .onReceive(NotificationCenter.default.publisher(for: .lassoMediaLoaded)) { _ in
                proxy.scrollTo("end", anchor: .bottom)
            }
        }
        .parityAnchor("agent.conversation")
    }

    private func isMessage(_ e: ConversationEntry) -> Bool {
        if case .message = e { return true }
        return false
    }

    @ViewBuilder private func entry(_ e: ConversationEntry, last: Bool) -> some View {
        switch e {
        case .message(let item, let text, let mine):
            VStack(alignment: mine ? .trailing : .leading, spacing: Theme.space("xs")) {
                MessageBubble(text: text, mine: mine)
                if last, let t = messageTime(item.at) {
                    Text(t)
                        .font(Theme.Fonts.small.font)
                        .foregroundStyle(Theme.Text.secondary.color)
                        .padding(.horizontal, Theme.space("xs"))
                }
            }
            .frame(maxWidth: .infinity, alignment: mine ? .trailing : .leading)
        case .tools(_, let tools): ToolRunRow(tools: tools)
        case .media(_, let version, let files): SharedFilesView(files: files, host: host, version: version)
        case .ask(_, let ask): AskRow(ask: ask, font: Theme.Fonts.chat.font)
        case .marker(let item):
            Text("— \(item.marker ?? "marker") —")
                .font(Theme.Fonts.small.font)
                .foregroundStyle(Theme.Text.secondary.color)
        }
    }
}

/// The open question's choices. A click (or 1–9) only selects one; the
/// selected choice shows Answer, and that button (or →) sends it. An answer
/// cannot be taken back, and the preview appears under a moving pointer, so a
/// stray click must never be one.
struct StepList: View {
    @ObservedObject var model: CardModel

    var body: some View {
        let steps = model.nextSteps
        VStack(alignment: .leading, spacing: Theme.space("2xs")) {
            ForEach(steps) { step in
                    let on = (model.highlighted ?? 0) == step.index
                    HStack(spacing: Theme.space("m")) {
                        Button { model.highlighted = step.index } label: {
                            HStack(spacing: Theme.space("m")) {
                                Text("\(step.index + 1).").foregroundStyle(Theme.Text.secondary.color)
                                Text(step.label).foregroundStyle(Theme.Text.primary.color).lineLimit(1).layoutPriority(1)
                                if let d = step.detail, !d.isEmpty {
                                    Text(d).foregroundStyle(Theme.Text.secondary.color).lineLimit(1)
                                }
                                Spacer(minLength: Theme.space("m"))
                            }
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel("Choose: \(step.label)")
                        .accessibilityAddTraits(on ? .isSelected : [])
                        if on {
                            PillButton(title: "Answer", primary: true, label: "Answer: \(step.label)") { model.takeStep() }
                                .keyHint(Keys.takeStep)
                                .parityAnchor("agent.steps.answer")
                        }
                    }
                    .font(Theme.Fonts.body.font)
                    .padding(.vertical, Theme.space("2xs"))
                    .padding(.horizontal, Theme.space("m"))
                    .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt).fill(on ? Theme.Row.selected.color : .clear))
            }
            AnswerExtras(model: model)
        }
        .padding(.horizontal, Theme.Panel.inset.pt)
        .parityAnchor("agent.steps")
    }
}

/// Under the choices: "Other…" where the dialog takes the human's own words,
/// and, for a question lasso cannot read, the way to answer it in herdr.
struct AnswerExtras: View {
    @ObservedObject var model: CardModel

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.space("s")) {
            if model.blockedWithoutChoices {
                HStack(spacing: Theme.space("m")) {
                    Text("Waiting on you in the terminal, with no choices lasso can read.")
                        .font(Theme.Fonts.body.font)
                        .foregroundStyle(Theme.Text.secondary.color)
                        .fixedSize(horizontal: false, vertical: true)
                    Spacer(minLength: Theme.space("m"))
                    PillButton(title: "Focus in herdr", label: "Focus \(model.current.displayName) in herdr to answer it") {
                        model.focusInHerdr()
                    }
                }
                .parityAnchor("agent.blocked")
            } else if model.answeringCustom {
                HStack(spacing: Theme.space("m")) {
                    Text("Answering in your own words: type it below, ⏎ sends it to the question.")
                        .font(Theme.Fonts.small.font)
                        .foregroundStyle(Theme.Text.secondary.color)
                    Spacer(minLength: Theme.space("m"))
                    PillButton(title: "Cancel", label: "Back to a message") { model.answeringCustom = false }
                }
            } else if model.allowsCustom {
                Button { model.answerInOwnWords() } label: {
                    HStack(spacing: Theme.space("m")) {
                        Text("\(model.nextSteps.count + 1).").foregroundStyle(Theme.Text.secondary.color)
                        Text("Other: answer in your own words").foregroundStyle(Theme.Text.primary.color)
                        Spacer(minLength: Theme.space("m"))
                    }
                    .font(Theme.Fonts.body.font)
                    .padding(.vertical, Theme.space("2xs"))
                    .padding(.horizontal, Theme.space("m"))
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Answer in your own words")
                .parityAnchor("agent.steps.other")
            }
        }
    }
}

/// Under the composer: the folder it works in, and its model.
struct AgentFooter: View {
    let pane: HostPane
    let model: String?

    var body: some View {
        HStack(spacing: Theme.space("l")) {
            Label((pane.cwd as NSString).lastPathComponent, systemImage: "folder")
                .lineLimit(1)
            if let m = modelName(model) {
                Rectangle().fill(Theme.Panel.divider.color).frame(width: Theme.space("hair"), height: Theme.space("2xl"))
                Label(m, systemImage: "cpu").lineLimit(1)
            }
            Spacer(minLength: 0)
        }
        .font(Theme.Fonts.small.font)
        .foregroundStyle(Theme.Text.secondary.color)
        .padding(.horizontal, Theme.Panel.inset.pt + Theme.space("xl"))
        .accessibilityElement(children: .combine)
    }
}

/// The composer and footer at the bottom of a window or preview.
struct AgentComposer: View {
    @ObservedObject var model: CardModel

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.space("m")) {
            if let n = model.notice {
                Text(n)
                    .font(Theme.Fonts.small.font)
                    .foregroundStyle(Theme.Text.secondary.color)
                    .lineLimit(1)
                    .padding(.horizontal, Theme.Panel.inset.pt)
            }
            ComposerRow(model: model) { model.attachFile() }
                .padding(.horizontal, Theme.Panel.inset.pt)
            AgentFooter(pane: model.current, model: model.chat?.model)
        }
        .padding(.top, Theme.space("l"))
        .padding(.bottom, Theme.space("2xl"))
        .background(alignment: .top) { Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair")) }
    }
}
