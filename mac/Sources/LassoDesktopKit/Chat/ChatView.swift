import SwiftUI

/// The assistant (ref a5): your messages in bubbles on the right, the bot's
/// replies as plain text, its open question as numbered options, and the
/// composer under a "Talking to …" line.
public struct ChatView: View {
    @ObservedObject var model: ChatModel
    let recording: RecordingModel

    public init(model: ChatModel, recording: RecordingModel? = nil) {
        self.model = model
        self.recording = recording ?? RecordingModel.inert
    }

    private var inset: CGFloat { Theme.Panel.inset.pt }
    private var textX: CGFloat { 22 }

    public var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                Spacer()
                Button { model.onClose() } label: {
                    Capsule().fill(Theme.Text.primary.color).frame(width: 16, height: 2.5)
                        .frame(width: 28, height: 24)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Hide the chat (esc)")
                .keyHint(Keys.esc)
            }
            .padding(.top, 10)
            .padding(.trailing, inset)

            conversation

            pending

            Text(model.notice ?? "Talking to \(model.bot.name)\(stateSuffix)")
                .font(Theme.Fonts.small.font)
                .foregroundStyle(Theme.Text.secondary.color)
                .lineLimit(1)
                .padding(.leading, textX + 34 + Theme.Control.composer.number("gap") + 14)
                .padding(.bottom, 6)
                .parityAnchor("chat.talking")
            ComposerRow(model: model, recording: recording, attach: model.attachScreenshot, record: model.record)
                .padding(.horizontal, inset)
                .padding(.bottom, inset)
        }
        .panelChrome(Theme.Card.size.size, anchor: "chat")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Chat with \(model.bot.name)")
    }

    private var stateSuffix: String {
        switch model.bot.state {
        case "working": " · working"
        case "blocked": " · waiting on you"
        case "stopped": " · stopped"
        case "starting": " · starting"
        default: ""
        }
    }

    private var conversation: some View {
        ScrollViewReader { proxy in
            ScrollView(.vertical) {
                VStack(alignment: .leading, spacing: 14) {
                    if let chat = model.chat {
                        ForEach(chat.items.suffix(80)) { item in
                            message(item).id(item.id)
                        }
                        if chat.items.isEmpty {
                            hint("Say hello: type, or hold V to talk.")
                        }
                    } else if let err = model.loadError {
                        hint("Could not read the conversation: \(err)")
                    } else {
                        hint("Loading…")
                    }
                }
                .padding(.horizontal, textX)
                .padding(.vertical, 8)
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
        .parityAnchor("chat.conversation")
    }

    private func hint(_ s: String) -> some View {
        Text(s).font(Theme.Fonts.chat.font).foregroundStyle(Theme.Text.secondary.color)
    }

    @ViewBuilder private func message(_ item: ChatItem) -> some View {
        let b = Theme.Chat.bubble
        if item.kind == "user" {
            VStack(alignment: .trailing, spacing: 4) {
                Text(item.text ?? "")
                    .font(Theme.Fonts.chat.font)
                    .foregroundStyle(Theme.Text.primary.color)
                    .textSelection(.enabled)
                    .padding(.horizontal, b.number("pad_h"))
                    .padding(.vertical, b.number("pad_v"))
                    .background(RoundedRectangle(cornerRadius: b.number("radius"), style: .continuous).fill(b.color("fill")))
                    .parityAnchor("chat.bubble[\(item.id)]")
                if let m = item.meta, let engine = m["engine"] {
                    Text("voice · \(engine)\(m["model"].map { " · \($0)" } ?? "")")
                        .font(Theme.Fonts.small.font)
                        .foregroundStyle(Theme.Text.secondary.color)
                        .accessibilityLabel("Dictated with \(engine)")
                }
            }
            .frame(maxWidth: .infinity, alignment: .trailing)
            .padding(.leading, 60)
        } else {
            ChatRow(item: item, font: Theme.Fonts.chat.font)
        }
    }

    /// The bot's open question or approval, as numbered options.
    @ViewBuilder private var pending: some View {
        let opts = model.pendingOptions
        if let p = model.pending, !opts.isEmpty {
            VStack(alignment: .leading, spacing: 6) {
                Text("? " + [p.title, p.message].compactMap { $0 }.joined(separator: ": "))
                    .font(Theme.Fonts.chat.font)
                    .foregroundStyle(Theme.Text.primary.color)
                    .lineLimit(2)
                ForEach(Array(opts.enumerated()), id: \.offset) { i, label in
                    let on = (model.highlighted ?? 0) == i
                    Button { model.highlighted = i } label: {
                        HStack(spacing: 6) {
                            Text("\(i + 1).").foregroundStyle(Theme.Text.secondary.color)
                            Text(label).foregroundStyle(Theme.Text.primary.color).lineLimit(1)
                            Spacer(minLength: 8)
                            if on {
                                Image(systemName: "arrow.right")
                                    .font(.system(size: 11, weight: .semibold))
                                    .foregroundStyle(Theme.Text.secondary.color)
                                    .keyHint(Keys.takeStep)
                            }
                        }
                        .font(Theme.Fonts.chat.font)
                        .padding(.vertical, 2)
                        .padding(.horizontal, 6)
                        .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt).fill(on ? Theme.Row.selected.color : .clear))
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Option \(i + 1): \(label)")
                    .keyHint(Keys.editStep, .topTrailing)
                }
            }
            .padding(.horizontal, textX - 6)
            .padding(.bottom, 12)
            .parityAnchor("chat.pending")
        }
    }
}
