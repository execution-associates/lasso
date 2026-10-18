import AppKit
import SwiftUI

/// What holds attachments shown as previews (`ComposerAttachments`): a
/// composer, and the New form's prompt.
@MainActor
public protocol AttachmentHolder: ObservableObject {
    /// Files attached to the message: previews, never text, their paths
    /// added to what is sent.
    var attachments: [ComposerAttachment] { get set }
}

extension AttachmentHolder {
    /// A preview's x: that file stays out of the message.
    public func removeAttachment(_ id: ComposerAttachment.ID) { attachments.removeAll { $0.id == id } }
}

/// What a composer edits: the card's and the chat's models.
@MainActor
public protocol ComposerModel: AttachmentHolder, AttachingModel {
    /// The words typed. Attachments are never in it.
    var composer: String { get set }
    var composerFocused: Bool { get set }
    var focusRequest: Int { get }
    var composerPlaceholder: String { get }
    func send()
    /// Stop shows in Send's place (the agent works, the composer is empty).
    var showsStop: Bool { get }
    /// Stop was pressed and the turn has not ended yet.
    var stopping: Bool { get }
    /// "Stop <name>", its accessibility label.
    var stopLabel: String { get }
    func stop()
    /// Return leaves the field once the message is on its way, so the
    /// one-letter keys act again (the inbox card).
    var leavesFieldOnSend: Bool { get }
}

extension ComposerModel {
    public var leavesFieldOnSend: Bool { false }

    /// What Send delivers: the words typed, then each attachment's path.
    public var composedMessage: String { Attachments.message(composer, attachments) }
    /// An attachment is still on its way to the agent's machine: Send waits for it.
    public var uploading: Bool { attachments.contains { $0.path == nil } }
    /// Send would deliver something now.
    public var canSend: Bool { !uploading && !composedMessage.isEmpty }
    /// Nothing typed and nothing attached.
    public var composerIsEmpty: Bool {
        composer.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && attachments.isEmpty
    }

    /// The unsent message, nil when there is none. One still uploading is
    /// not kept: its upload finishes into this model, not the draft.
    public var draft: ComposerDraft? {
        get {
            let landed = attachments.filter { $0.path != nil }
            guard !composer.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !landed.isEmpty else { return nil }
            return ComposerDraft(text: composer, attachments: landed)
        }
        set {
            composer = newValue?.text ?? ""
            attachments = newValue?.attachments ?? []
        }
    }
}

/// The composer's text: an NSTextView in a scroll view, because the card
/// needs to give it focus on demand, take it away on esc, and send on
/// Return, and the focus API SwiftUI offers for that is a macro the Command
/// Line Tools lack. It grows with the message (`control.composer.grow`):
/// ⌘Return sends and Return starts a new line, or with Settings' Return
/// sends on, Return sends (`ComposerReturn`), and pasted text keeps its
/// newlines. Files and images pasted or dropped on it are uploaded
/// to the agent's machine and shown as attachments over it, never as text.
struct ComposerField<Model: ComposerModel>: NSViewRepresentable {
    @ObservedObject var model: Model

    func makeCoordinator() -> Coordinator { Coordinator(model: model) }

    func makeNSView(context: Context) -> NSScrollView {
        let t = ComposerTextField(usingTextLayoutManager: false)
        t.attachTarget = model
        t.isRichText = false
        t.importsGraphics = false
        t.allowsUndo = true
        t.drawsBackground = false
        t.font = ComposerLayout.font
        t.textColor = Theme.Text.primary.nsColor
        t.insertionPointColor = Theme.Text.primary.nsColor
        t.typingAttributes = [.font: ComposerLayout.font, .foregroundColor: Theme.Text.primary.nsColor]
        t.isVerticallyResizable = true
        t.isHorizontallyResizable = false
        t.autoresizingMask = [.width]
        // At least the row's height, so a click anywhere on the field lands in it.
        t.minSize = NSSize(width: 0, height: Theme.Control.composer.number("height"))
        t.maxSize = NSSize(width: CGFloat.greatestFiniteMagnitude, height: .greatestFiniteMagnitude)
        t.textContainer?.widthTracksTextView = true
        t.textContainer?.lineFragmentPadding = ComposerLayout.textPad
        t.textContainerInset = NSSize(width: 0, height: ComposerLayout.inset)
        t.delegate = context.coordinator
        t.setAccessibilityLabel("Message")
        t.updateDragTypeRegistration()
        let c = context.coordinator
        t.onReturn = { [weak c, weak t] in
            guard let c, let t else { return }
            c.submit(t)
        }
        t.onFocus = { [weak c] on in c?.model.composerFocused = on }
        let s = NSScrollView()
        s.drawsBackground = false
        s.borderType = .noBorder
        s.hasVerticalScroller = true
        s.hasHorizontalScroller = false
        s.autohidesScrollers = true
        s.scrollerStyle = .overlay
        s.documentView = t
        placeholder(t)
        return s
    }

    func updateNSView(_ s: NSScrollView, context: Context) {
        context.coordinator.model = model
        guard let t = s.documentView as? ComposerTextField else { return }
        t.attachTarget = model
        if t.string != model.composer {
            t.string = model.composer
            t.needsDisplay = true
        }
        // A greyed composer (a stopped bot's) takes no typing.
        t.isEditable = context.environment.isEnabled
        placeholder(t)
        if context.coordinator.focusSeen != model.focusRequest {
            context.coordinator.focusSeen = model.focusRequest
            DispatchQueue.main.async {
                t.window?.makeFirstResponder(t)
                t.moveToEndOfDocument(nil)
            }
        }
    }

    /// Its height for its text at the width offered: one line is the row's
    /// height, and it grows to `max_lines` before it scrolls.
    func sizeThatFits(_ proposal: ProposedViewSize, nsView s: NSScrollView, context: Context) -> CGSize? {
        let offered = proposal.width.flatMap { $0.isFinite && $0 > 0 ? $0 : nil }
        let width = offered ?? (s.bounds.width > 0 ? s.bounds.width : ComposerLayout.fallbackWidth)
        return CGSize(width: proposal.width ?? width, height: ComposerLayout.height(model.composer, width: width))
    }

    private func placeholder(_ t: ComposerTextField) {
        guard t.placeholder?.string != model.composerPlaceholder else { return }
        t.placeholder = NSAttributedString(
            string: model.composerPlaceholder,
            attributes: [
                .foregroundColor: Theme.Text.placeholder.nsColor,
                .font: ComposerLayout.font,
            ])
        t.setAccessibilityPlaceholderValue(model.composerPlaceholder)
    }

    @MainActor
    final class Coordinator: NSObject, NSTextViewDelegate {
        var model: Model
        var focusSeen = 0

        init(model: Model) { self.model = model }

        func textDidChange(_ n: Notification) {
            guard let t = n.object as? NSTextView else { return }
            model.composer = t.string
            t.enclosingScrollView?.invalidateIntrinsicContentSize()
        }

        /// Send (⌘Return, or Return when it sends), and the inbox card's
        /// leaves the field once there was something to send.
        func submit(_ t: NSTextView) {
            let sending = model.canSend
            model.send()
            if sending, model.leavesFieldOnSend { t.window?.makeFirstResponder(nil) }
        }

        func textView(_ t: NSTextView, doCommandBy sel: Selector) -> Bool {
            switch sel {
            case #selector(NSResponder.insertNewline(_:)) where SettingsModel.returnSendsOn():
                submit(t)
                return true
            case #selector(NSResponder.cancelOperation(_:)), #selector(NSTextView.complete(_:)):
                // esc leaves the field first; a second esc closes the card.
                t.window?.makeFirstResponder(nil)
                return true
            case #selector(NSResponder.insertTab(_:)):
                // Tab moves on, as it did from a one-line field.
                t.window?.selectNextKeyView(nil)
                return true
            case #selector(NSResponder.insertBacktab(_:)):
                t.window?.selectPreviousKeyView(nil)
                return true
            default:
                return false
            }
        }
    }
}

/// What Return does in the composer. By default ⌘Return sends and Return
/// on its own, or with Shift or Option, starts a new line, as in the web
/// chat. With `returnSends` (Settings › General) Return on its own sends
/// and Shift- or Option-Return starts a new line. Any other combination is
/// the system's (nil).
enum ComposerReturn: Equatable {
    case send, newline

    init?(_ flags: NSEvent.ModifierFlags, returnSends: Bool = false) {
        let m = flags.intersection([.shift, .option, .command, .control])
        if m.isEmpty {
            self = returnSends ? .send : .newline
        } else if m.isSubset(of: [.shift, .option]) {
            self = .newline
        } else if m == .command, !returnSends {
            self = .send
        } else {
            return nil
        }
    }
}

/// The composer's text layout: the body font, set in from the field's
/// edges, and the height the field takes for a message.
@MainActor
enum ComposerLayout {
    static var font: NSFont { Theme.Fonts.body.nsFont }
    static var textPad: CGFloat { Theme.Control.composerGrow.number("text_pad") }
    static var maxLines: CGFloat { Theme.Control.composerGrow.number("max_lines") }
    /// Before the field has a width: only for a measure nothing offered one.
    static let fallbackWidth: CGFloat = 240

    static var lineHeight: CGFloat { NSLayoutManager().defaultLineHeight(for: font) }

    /// Above and below the text: one line sits in the middle of the row's height.
    static var inset: CGFloat { max(0, (Theme.Control.composer.number("height") - lineHeight) / 2) }

    /// The field's height for `text` at `width`, laid out as the text view
    /// lays it out (TextKit 1, the same font and padding): from one line (the
    /// row's height) to `max_lines`, past which it scrolls.
    static func height(_ text: String, width: CGFloat) -> CGFloat {
        let storage = NSTextStorage(string: text, attributes: [.font: font])
        let layout = NSLayoutManager()
        storage.addLayoutManager(layout)
        let container = NSTextContainer(size: NSSize(width: max(width, 1), height: .greatestFiniteMagnitude))
        container.lineFragmentPadding = textPad
        layout.addTextContainer(container)
        layout.ensureLayout(for: container)
        let line = lineHeight
        // A trailing newline's empty last line is the extra line fragment.
        let used = max(layout.usedRect(for: container).maxY, layout.extraLineFragmentRect.maxY)
        let lines = min(max(used, line), line * maxLines)
        return max(Theme.Control.composer.number("height"), (lines + 2 * inset).rounded(.up))
    }
}

/// The composer's text view: an AttachingTextView, so ⌘V and Edit › Paste
/// of a file or an image, and a drop of one, upload it. It is plain text
/// that imports no graphics, so an image never lands in the text itself.
/// Return is decided here, before the key bindings (`ComposerReturn`), with
/// an input method's marked text left to commit.
final class ComposerTextField: AttachingTextView {
    var onReturn: (() -> Void)?
    var onFocus: ((Bool) -> Void)?
    /// Drawn while there is no text (a text view has no placeholder of its own).
    var placeholder: NSAttributedString? {
        didSet { if string.isEmpty { needsDisplay = true } }
    }

    override func keyDown(with e: NSEvent) {
        if !hasMarkedText(), e.keyCode == KeyCode.returnKey || e.keyCode == KeyCode.keypadEnter,
           let r = ComposerReturn(e.modifierFlags, returnSends: SettingsModel.returnSendsOn())
        {
            switch r {
            case .send: onReturn?()
            case .newline: super.insertNewline(nil)
            }
            return
        }
        super.keyDown(with: e)
    }

    override func becomeFirstResponder() -> Bool {
        let ok = super.becomeFirstResponder()
        if ok { onFocus?(true) }
        return ok
    }

    override func resignFirstResponder() -> Bool {
        let ok = super.resignFirstResponder()
        if ok { onFocus?(false) }
        return ok
    }

    override func didChangeText() {
        super.didChangeText()
        // The placeholder comes and goes with the first character.
        needsDisplay = true
    }

    override func draw(_ dirtyRect: NSRect) {
        super.draw(dirtyRect)
        guard string.isEmpty, let placeholder else { return }
        let o = textContainerOrigin
        let pad = textContainer?.lineFragmentPadding ?? 0
        let rect = NSRect(
            x: o.x + pad, y: o.y, width: max(0, bounds.width - o.x - 2 * pad), height: ComposerLayout.lineHeight)
        placeholder.draw(with: rect, options: [.usesLineFragmentOrigin, .truncatesLastVisibleLine])
    }
}
