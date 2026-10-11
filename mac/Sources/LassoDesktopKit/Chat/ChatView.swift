import SwiftUI

/// The assistant: a conversation with a lasso bot, in the card's language
/// (same surface, rows, composer and keycaps).
public struct ChatView: View {
    @ObservedObject var model: ChatModel
    let recording: RecordingModel?

    public init(model: ChatModel, recording: RecordingModel? = nil) {
        self.model = model
        self.recording = recording
    }

    private var pad: CGFloat { Theme.Card.pad.pt }

    public var body: some View {
        let size = Theme.Card.size.size
        let bot = model.bot
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                Text(bot.name)
                    .font(Theme.Fonts.name.font)
                    .foregroundStyle(Theme.Text.primary.color)
                Text("assistant")
                    .font(Theme.Fonts.secondLine.font)
                    .foregroundStyle(Theme.Text.secondary.color)
                Spacer(minLength: 8)
                Button { model.onClose() } label: { Keycap(Keys.esc) }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Close (esc)")
            }
            .padding(.horizontal, pad)
            .frame(height: Theme.Card.headerHeight.pt)

            HStack(spacing: 6) {
                Text(identity(bot))
                    .font(Theme.Fonts.secondLine.font)
                    .foregroundStyle(Theme.Text.secondary.color)
                    .lineLimit(1)
                Text(bot.host)
                    .font(.system(size: Theme.Card.machineChip.number("font_size")))
                    .foregroundStyle(Theme.Text.primary.color)
                    .padding(.horizontal, Theme.Card.machineChip.number("pad_h"))
                    .padding(.vertical, Theme.Card.machineChip.number("pad_v"))
                    .background(
                        RoundedRectangle(cornerRadius: Theme.Card.machineChip.number("radius"))
                            .fill(RGBA(parsing: Theme.Card.machineChip.value["fill"]?.string ?? "")?.color ?? .clear))
                Spacer(minLength: 0)
            }
            .padding(.horizontal, pad)
            .padding(.bottom, Theme.Card.sectionGap.pt)

            conversation
            pendingSteps
            composer
            footer
        }
        .frame(width: size.width, height: size.height)
        .background(Theme.Card.surface.color)
        .clipShape(RoundedRectangle(cornerRadius: Theme.Card.radius.pt, style: .continuous))
        .parityAnchor("chat")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Chat with \(bot.name)")
    }

    private func identity(_ bot: BotView) -> String {
        let state: String =
            switch bot.state {
            case "working": "working"
            case "blocked": bot.waiting_for.map { "needs you: \($0)" } ?? "needs you"
            case "stopped": "stopped"
            case "starting": "starting"
            default: "idle"
            }
        let model = bot.live_model?.name ?? bot.model
        return [state, bot.runtime, model ?? ""].filter { !$0.isEmpty }.joined(separator: " · ")
    }

    private var conversation: some View {
        ScrollViewReader { proxy in
            ScrollView(.vertical) {
                VStack(alignment: .leading, spacing: 6) {
                    if let chat = model.chat {
                        ForEach(chat.items.suffix(80)) { item in
                            VStack(alignment: .leading, spacing: 2) {
                                ChatRow(item: item)
                                if let m = item.meta, let engine = m["engine"] {
                                    Text("voice · \(engine)\(m["model"].map { " · \($0)" } ?? "")")
                                        .font(Theme.Fonts.secondLine.font)
                                        .foregroundStyle(Theme.Text.secondary.color)
                                        .accessibilityLabel("Dictated with \(engine)")
                                }
                            }
                            .id(item.id)
                        }
                        if chat.items.isEmpty {
                            placeholder("Say hello: type, or hold V to talk.")
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
        .parityAnchor("chat.conversation")
    }

    private func placeholder(_ s: String) -> some View {
        Text(s)
            .font(Theme.Fonts.terminal.font)
            .foregroundStyle(Theme.Text.secondary.color)
    }

    @ViewBuilder private var pendingSteps: some View {
        let opts = model.pendingOptions
        if let p = model.pending, !opts.isEmpty {
            VStack(alignment: .leading, spacing: 2) {
                Text([p.title, p.message].compactMap { $0 }.joined(separator: ": "))
                    .font(Theme.Fonts.secondLine.font)
                    .foregroundStyle(Theme.Text.secondary.color)
                    .lineLimit(2)
                    .padding(.horizontal, Theme.Row.pad.number("h"))
                ForEach(Array(opts.enumerated()), id: \.offset) { i, label in
                    let on = (model.highlighted ?? 0) == i
                    Button { model.highlighted = i } label: {
                        HStack(spacing: 8) {
                            Text("\(i + 1)")
                                .font(Theme.Fonts.terminal.font)
                                .foregroundStyle(Theme.Text.secondary.color)
                            Text(label)
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
                    .accessibilityLabel("Option \(i + 1): \(label)")
                }
            }
            .padding(.horizontal, pad - Theme.Row.pad.number("h"))
            .padding(.bottom, Theme.Card.sectionGap.pt)
        }
    }

    private var composer: some View {
        ChatComposer(model: model, recording: recording ?? RecordingModel.inert)
            .padding(.horizontal, pad)
            .padding(.bottom, Theme.Card.sectionGap.pt)
    }

    private var footer: some View {
        HStack(spacing: 10) {
            if model.bot.state == "working" || model.chat?.running == true {
                Button { model.stop() } label: {
                    Text("Stop")
                        .font(Theme.Fonts.tree.font)
                        .foregroundStyle(Theme.dangerText.color)
                        .padding(.vertical, Theme.Row.pad.number("v") - 2)
                        .padding(.horizontal, Theme.Row.pad.number("h"))
                        .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt).fill(Theme.dangerFill.color))
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Stop the assistant's turn")
            }
            Text(model.notice ?? "")
                .font(Theme.Fonts.secondLine.font)
                .foregroundStyle(Theme.Text.secondary.color)
                .lineLimit(1)
                .frame(maxWidth: .infinity, alignment: .leading)
            if let recording {
                Text("Voice: \(recording.engineName)")
                    .font(Theme.Fonts.secondLine.font)
                    .foregroundStyle(Theme.Text.secondary.color)
                    .accessibilityLabel("Voice engine \(recording.engineName)")
            }
        }
        .padding(.horizontal, pad)
        .frame(height: Theme.Card.footerHeight.pt)
        .frame(maxWidth: .infinity)
        .background(Theme.Card.footer.color)
        .parityAnchor("chat.footer")
    }
}

/// The chat's composer, watching the voice turn: while it records or
/// transcribes, the strip takes the field's place and the mic shows it.
struct ChatComposer: View {
    @ObservedObject var model: ChatModel
    @ObservedObject var recording: RecordingModel

    var body: some View {
        let c = Theme.Card.composer
        let active = recording.isRecording || recording.phase == .transcribing
        HStack(spacing: 8) {
            iconButton("camera.viewfinder", Keys.attach) { model.attachScreenshot() }
            ZStack(alignment: .leading) {
                ComposerField(model: model).opacity(active ? 0 : 1)
                RecordingStrip(model: recording)
            }
            .frame(height: c.number("height"))
            .padding(.horizontal, 8)
            .background(
                RoundedRectangle(cornerRadius: c.number("radius"))
                    .fill(RGBA(parsing: c.value["fill"]?.string ?? "")?.color ?? .clear))
            .parityAnchor("chat.composer")
            iconButton(recording.isRecording ? "mic.fill" : "mic", Keys.record) { model.record() }
            iconButton("arrow.up", Keys.send) { model.send() }
        }
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
        .accessibilityLabel("\(key.action) (\(key.legend))")
    }
}
