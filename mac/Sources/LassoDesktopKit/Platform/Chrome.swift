import AppKit
import Combine
import SwiftUI

/// The panel every surface beside the rail sits on.
struct PanelBackground: View {
    var body: some View {
        let shape = RoundedRectangle(cornerRadius: Theme.Panel.radius.pt, style: .continuous)
        shape.fill(Theme.Panel.surface.color)
            .overlay(shape.strokeBorder(Theme.Panel.border.color, lineWidth: 1))
    }
}

extension View {
    /// Size, surface, border and clipping of a side panel.
    func panelChrome(_ size: CGSize, anchor: String) -> some View {
        frame(width: size.width, height: size.height)
            .background(PanelBackground())
            .clipShape(RoundedRectangle(cornerRadius: Theme.Panel.radius.pt, style: .continuous))
            .parityAnchor(anchor)
    }
}

/// A round icon button (close, paging chevrons).
struct RoundButton: View {
    let glyph: Glyph
    var enabled = true
    var size: CGFloat = Theme.Control.round.number("size")
    var fill: Color = Theme.Control.round.color("fill")
    let label: String
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            glyph.view(size: size * Theme.icon.number("round_ratio"), weight: .semibold)
                .foregroundStyle(enabled ? Theme.Text.primary.color : Theme.Text.secondary.color)
                .frame(width: size, height: size)
                .background(Circle().fill(enabled ? fill : Theme.Control.round.color("disabled")))
                .contentShape(Circle())
        }
        .buttonStyle(.plain)
        .disabled(!enabled)
        .accessibilityLabel(label)
    }
}

/// A capsule text button: the footer's Kill agent / Acknowledge / Retry.
struct PillButton: View {
    let title: String
    var primary = false
    var textColor: Color = Theme.Text.primary.color
    /// Off: greyed (secondary text, the plain fill, never the primary one)
    /// and not clickable, with `hint` saying why.
    var enabled = true
    var hint: String?
    /// Its action is in flight: a small spinner before the title.
    var busy = false
    let label: String
    let action: () -> Void

    var body: some View {
        let p = Theme.Control.pill
        let lit = primary && enabled
        let ink = !enabled ? Theme.Text.secondary.color : lit ? Theme.Control.pillPrimary.color("text") : textColor
        Button(action: action) {
            HStack(spacing: Theme.space("m")) {
                if busy { ProgressView().controlSize(.small).tint(ink) }
                Text(title).font(Theme.Fonts.label.font).foregroundStyle(ink)
            }
            .padding(.horizontal, p.number("pad_h"))
            .frame(height: p.number("height"))
            .background(Capsule().fill(lit ? Theme.Control.pillPrimary.color("fill") : p.color("fill")))
            .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .disabled(!enabled)
        .accessibilityLabel(label)
        .accessibilityHint(enabled ? "" : hint ?? "")
    }
}

/// The composer row (ref a2/a5): a round paperclip button, the field, and a
/// round send button, which is Stop while the agent works and nothing is
/// typed or attached. The field grows with the message, the buttons staying
/// on its last line; attachments show over it (`ComposerAttachments`).
struct ComposerRow<Model: ComposerModel>: View {
    @ObservedObject var model: Model
    let attach: () -> Void

    var body: some View {
        let c = Theme.Control.composer
        let h = c.number("height")
        VStack(alignment: .leading, spacing: Theme.Control.composerAttachment.number("gap")) {
            if !model.attachments.isEmpty {
                ComposerAttachments(model: model)
                    .padding(.leading, h + c.number("gap"))
            }
            HStack(alignment: .bottom, spacing: c.number("gap")) {
                RoundButton(
                    glyph: .lucide(.paperclip), size: h, fill: c.color("fill"), label: "Attach a file (\(Keys.attach.legend))",
                    action: attach
                )
                ComposerField(model: model)
                    .padding(.leading, c.number("field_pad_l"))
                    .padding(.trailing, c.number("field_pad_r"))
                    .frame(minHeight: h)
                    // A capsule on one line, a rounded box as it grows.
                    .background(RoundedRectangle(cornerRadius: h / 2, style: .continuous).fill(c.color("fill")))
                    .parityAnchor("composer.field")
                if model.showsStop {
                    // The web chat's Stop: a filled square while the agent works.
                    RoundButton(
                        glyph: .lucide(.square, filled: true), enabled: !model.stopping, size: h, fill: c.color("fill"),
                        label: "\(model.stopLabel) (⌘.)", action: model.stop
                    )
                    .parityAnchor("composer.stop")
                } else {
                    RoundButton(
                        // The phone's send: an arrow, a little bolder (ReplyBox).
                        glyph: .lucide(.arrowUp, stroke: 2.5), enabled: model.canSend, size: h, fill: c.color("fill"),
                        label: "Send (⏎)", action: model.send
                    )
                }
            }
        }
        .parityAnchor("composer")
    }
}

/// The composer's attachments, over its field: an image as its thumbnail,
/// any other file as its icon and name, each with an x that takes it out of
/// the message. One still uploading is dimmed under a spinner.
struct ComposerAttachments<Model: ComposerModel>: View {
    @ObservedObject var model: Model

    var body: some View {
        let t = Theme.Control.composerAttachment
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: t.number("gap")) {
                ForEach(model.attachments) { a in tile(a) }
            }
        }
        .frame(height: t.number("size"))
    }

    private func tile(_ a: ComposerAttachment) -> some View {
        let t = Theme.Control.composerAttachment
        let size = t.number("size")
        let shape = RoundedRectangle(cornerRadius: t.number("radius"), style: .continuous)
        return ZStack(alignment: .topTrailing) {
            Group {
                if let image = a.thumbnail {
                    Image(nsImage: image).resizable().scaledToFill()
                        .frame(width: size, height: size)
                } else {
                    HStack(spacing: Theme.space("s")) {
                        LucideIcon(.file, size: Theme.Fonts.body.number("size"))
                            .foregroundStyle(Theme.Text.secondary.color)
                        Text(a.name)
                            .font(Theme.Fonts.small.font)
                            .foregroundStyle(Theme.Text.primary.color)
                            .lineLimit(2)
                            .truncationMode(.middle)
                        Spacer(minLength: 0)
                    }
                    .padding(.leading, Theme.space("m"))
                    .padding(.trailing, t.number("remove") + t.number("remove_inset"))
                    .frame(width: t.number("file_w"), height: size)
                }
            }
            .background(shape.fill(Theme.Control.composer.color("fill")))
            .clipShape(shape)
            .opacity(a.path == nil ? t.number("pending") : 1)
            .overlay { if a.path == nil { ProgressView().controlSize(.small) } }
            Button { model.removeAttachment(a.id) } label: {
                LucideIcon(.x, size: t.number("remove") * Theme.icon.number("round_ratio"), stroke: 2.5)
                    .foregroundStyle(Theme.Text.primary.color)
                    .frame(width: t.number("remove"), height: t.number("remove"))
                    .background(Circle().fill(Theme.Panel.surface.color))
                    .contentShape(Circle())
            }
            .buttonStyle(.plain)
            .padding(t.number("remove_inset"))
            .accessibilityLabel("Remove \(a.name)")
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel(a.thumbnail != nil ? "Attached image \(a.name)" : "Attached \(a.name)")
    }
}

/// A one-line text field that takes the cursor as it appears, with its text
/// selected (a rename in place). ⏎ submits and esc cancels, unless the
/// panel's own key handler takes them first. A click anywhere outside it, or
/// its window losing the keyboard, submits too (as Finder's rename does).
struct InlineField: NSViewRepresentable {
    @Binding var text: String
    let placeholder: String
    let label: String
    var font: NSFont = Theme.Fonts.name.nsFont
    var onSubmit: () -> Void = {}
    var onCancel: () -> Void = {}

    func makeCoordinator() -> Coordinator { Coordinator(text: $text, submit: onSubmit, cancel: onCancel) }

    static func dismantleNSView(_ f: NSTextField, coordinator: Coordinator) { coordinator.stopWatching() }

    func makeNSView(context: Context) -> NSTextField {
        let f = NSTextField(string: text)
        f.isBordered = false
        f.isBezeled = false
        f.drawsBackground = false
        f.focusRingType = .none
        f.font = font
        f.textColor = Theme.Text.primary.nsColor
        f.placeholderAttributedString = NSAttributedString(
            string: placeholder, attributes: [.foregroundColor: Theme.Text.placeholder.nsColor, .font: font])
        f.delegate = context.coordinator
        f.setAccessibilityLabel(label)
        DispatchQueue.main.async {
            f.window?.makeKeyAndOrderFront(nil)
            f.window?.makeFirstResponder(f)
            f.currentEditor()?.selectAll(nil)
            context.coordinator.watch(f)
        }
        return f
    }

    func updateNSView(_ f: NSTextField, context: Context) {
        if f.stringValue != text { f.stringValue = text }
    }

    @MainActor
    final class Coordinator: NSObject, NSTextFieldDelegate {
        var text: Binding<String>
        let submit: () -> Void
        let cancel: () -> Void
        init(text: Binding<String>, submit: @escaping () -> Void, cancel: @escaping () -> Void) {
            self.text = text
            self.submit = submit
            self.cancel = cancel
        }
        func controlTextDidChange(_ n: Notification) {
            if let f = n.object as? NSTextField { text.wrappedValue = f.stringValue }
        }
        func control(_ control: NSControl, textView: NSTextView, doCommandBy selector: Selector) -> Bool {
            switch selector {
            case #selector(NSResponder.insertNewline(_:)): submit()
            case #selector(NSResponder.cancelOperation(_:)): cancel()
            default: return false
            }
            return true
        }

        private var monitor: Any?
        private var resign: NSObjectProtocol?

        /// End the edit on a click outside the field, in its window or any
        /// other of the app's, and when its window loses the keyboard (a click
        /// in another app). Submitting twice is harmless: the models guard it.
        func watch(_ f: NSTextField) {
            stopWatching()
            monitor = NSEvent.addLocalMonitorForEvents(matching: [.leftMouseDown, .rightMouseDown]) { [weak self, weak f] e in
                if let f, e.window !== f.window || !f.bounds.contains(f.convert(e.locationInWindow, from: nil)) {
                    self?.end(f)
                }
                return e
            }
            if let w = f.window {
                resign = NotificationCenter.default.addObserver(forName: NSWindow.didResignKeyNotification, object: w, queue: .main) {
                    [weak self, weak f] _ in
                    MainActor.assumeIsolated { if let f { self?.end(f) } }
                }
            }
        }

        private func end(_ f: NSTextField) {
            stopWatching()
            f.window?.makeFirstResponder(nil)
            submit()
        }

        func stopWatching() {
            if let m = monitor { NSEvent.removeMonitor(m) }
            if let r = resign { NotificationCenter.default.removeObserver(r) }
            monitor = nil
            resign = nil
        }
    }
}

/// The pointing hand over a renamable title: pushed once, popped once, however
/// the hover ends (a click swaps the title for a field under the pointer).
@MainActor
enum TitleCursor {
    private static var pushed = false

    static func hover(_ inside: Bool) {
        if inside, !pushed {
            NSCursor.pointingHand.push()
            pushed = true
        } else if !inside, pushed {
            NSCursor.pop()
            pushed = false
        }
    }
}

/// An agent's name, as a window or card shows it. A click (or right-click,
/// Rename…) turns it into a field: ⏎ keeps, esc cancels, empty clears the
/// label. The pointer is a pointing hand over a name that can be renamed.
struct AgentTitle: View {
    @ObservedObject var model: CardModel
    var font: Token = Theme.Fonts.title
    var anchor = "agent.title"

    var body: some View {
        let p = model.current
        if model.renaming {
            InlineField(
                text: $model.renameDraft, placeholder: p.displayName, label: "New name for \(p.displayName)", font: font.nsFont,
                onSubmit: { model.commitRename() }, onCancel: { model.cancelRename() }
            )
            .padding(.horizontal, Theme.space("s"))
            .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt).strokeBorder(Theme.accent.color, lineWidth: 1))
            .parityAnchor(anchor)
        } else {
            Text(p.displayName)
                .font(font.font)
                .foregroundStyle(Theme.Text.primary.color)
                .lineLimit(1)
                .contentShape(Rectangle())
                .onHover { TitleCursor.hover($0 && model.canRename) }
                .onDisappear { TitleCursor.hover(false) }
                .onTapGesture {
                    TitleCursor.hover(false)
                    model.startRename()
                }
                .contextMenu {
                    if model.canRename { Button("Rename…") { model.startRename() } }
                }
                .accessibilityAction(named: "Rename") { model.startRename() }
                .parityAnchor(anchor)
        }
    }
}
