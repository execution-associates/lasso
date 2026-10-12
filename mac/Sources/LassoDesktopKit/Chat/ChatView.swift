import SwiftUI

/// A bot's chat (ref a5): your messages in bubbles on the right, the bot's
/// replies as plain text, its open question as numbered options, and the
/// composer under a "Talking to …" line.
public struct ChatView: View {
    @ObservedObject var model: ChatModel

    public init(model: ChatModel) { self.model = model }

    private var inset: CGFloat { Theme.Panel.inset.pt }
    private var textX: CGFloat { Theme.Layout.chat.number("text_x") }

    public var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                Spacer()
                Button { model.onClose() } label: {
                    Capsule().fill(Theme.Text.primary.color)
                        .frame(width: Theme.Layout.chat.size2("close_bar").width, height: Theme.Layout.chat.size2("close_bar").height)
                        .frame(width: Theme.Layout.chat.size2("close_hit").width, height: Theme.Layout.chat.size2("close_hit").height)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Close the chat (esc)")
            }
            .padding(.top, Theme.Layout.chat.number("top"))
            .padding(.trailing, inset)

            conversation

            pending

            Text(model.notice ?? "Talking to \(model.bot.name)\(stateSuffix)")
                .font(Theme.Fonts.small.font)
                .foregroundStyle(Theme.Text.secondary.color)
                .lineLimit(1)
                .padding(.leading, Theme.Panel.inset.pt + Theme.Control.composer.number("height") + Theme.Control.composer.number("gap") + Theme.Control.composer.number("field_pad_l"))
                .padding(.bottom, Theme.space("s"))
                .parityAnchor("chat.talking")
            ComposerRow(model: model, attach: model.attachFile)
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
                VStack(alignment: .leading, spacing: Theme.Layout.chat.number("message_gap")) {
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
                .padding(.vertical, Theme.space("m"))
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
            Text(item.text ?? "")
                .font(Theme.Fonts.chat.font)
                    .foregroundStyle(Theme.Text.primary.color)
                    .textSelection(.enabled)
                    .padding(.horizontal, b.number("pad_h"))
                    .padding(.vertical, b.number("pad_v"))
                    .background(RoundedRectangle(cornerRadius: b.number("radius"), style: .continuous).fill(b.color("fill")))
                    .parityAnchor("chat.bubble[\(item.id)]")
                    .frame(maxWidth: .infinity, alignment: .trailing)
            .padding(.leading, Theme.Layout.chat.number("bubble_inset"))
        } else {
            ChatRow(item: item, font: Theme.Fonts.chat.font)
        }
    }

    /// The bot's open question or approval, as numbered options.
    @ViewBuilder private var pending: some View {
        let opts = model.pendingOptions
        if let p = model.pending, !opts.isEmpty {
            VStack(alignment: .leading, spacing: Theme.space("s")) {
                Text("? " + [p.title, p.message].compactMap { $0 }.joined(separator: ": "))
                    .font(Theme.Fonts.chat.font)
                    .foregroundStyle(Theme.Text.primary.color)
                    .lineLimit(2)
                ForEach(Array(opts.enumerated()), id: \.offset) { i, label in
                    let on = (model.highlighted ?? 0) == i
                    Button { model.highlighted = i } label: {
                        HStack(spacing: Theme.space("s")) {
                            Text("\(i + 1).").foregroundStyle(Theme.Text.secondary.color)
                            Text(label).foregroundStyle(Theme.Text.primary.color).lineLimit(1)
                            Spacer(minLength: 8)
                            if on {
                                Image(systemName: "arrow.right")
                                    .font(.system(size: Theme.icon.number("step"), weight: .semibold))
                                    .foregroundStyle(Theme.Text.secondary.color)
                            }
                        }
                        .font(Theme.Fonts.chat.font)
                        .padding(.vertical, Theme.space("2xs"))
                        .padding(.horizontal, Theme.space("s"))
                        .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt).fill(on ? Theme.Row.selected.color : .clear))
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Option \(i + 1): \(label)")
                }
            }
            .padding(.horizontal, textX - 6)
            .padding(.bottom, Theme.space("xl"))
            .parityAnchor("chat.pending")
        }
    }
}
