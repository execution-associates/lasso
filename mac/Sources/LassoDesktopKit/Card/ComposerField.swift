import AppKit
import SwiftUI

/// What a composer edits: the card's and the chat's models.
@MainActor
public protocol ComposerModel: ObservableObject, AttachingModel {
    var composer: String { get set }
    var composerFocused: Bool { get set }
    var focusRequest: Int { get }
    var composerPlaceholder: String { get }
    func send()
}

/// The composer's text field: an NSTextField, because the card needs to give
/// it focus on demand, take it away on esc, and send on Return, and the
/// focus API SwiftUI offers for that is a macro the Command Line Tools lack.
/// Files and images pasted or dropped on it are uploaded to the agent's
/// machine (ComposerTextField), never pasted as a path on this Mac.
struct ComposerField<Model: ComposerModel>: NSViewRepresentable {
    @ObservedObject var model: Model

    func makeCoordinator() -> Coordinator { Coordinator(model: model) }

    func makeNSView(context: Context) -> NSTextField {
        let f = ComposerTextField()
        f.attachTarget = model
        f.isBordered = false
        f.drawsBackground = false
        f.focusRingType = .none
        f.font = Theme.Fonts.body.nsFont
        f.textColor = Theme.Text.primary.nsColor
        f.lineBreakMode = .byTruncatingTail
        f.cell?.usesSingleLineMode = true
        f.delegate = context.coordinator
        f.setAccessibilityLabel("Message")
        placeholder(f)
        return f
    }

    func updateNSView(_ f: NSTextField, context: Context) {
        context.coordinator.model = model
        (f as? ComposerTextField)?.attachTarget = model
        if f.stringValue != model.composer { f.stringValue = model.composer }
        placeholder(f)
        if context.coordinator.focusSeen != model.focusRequest {
            context.coordinator.focusSeen = model.focusRequest
            DispatchQueue.main.async {
                f.window?.makeFirstResponder(f)
                f.currentEditor()?.moveToEndOfDocument(nil)
            }
        }
    }

    private func placeholder(_ f: NSTextField) {
        f.placeholderAttributedString = NSAttributedString(
            string: model.composerPlaceholder,
            attributes: [
                .foregroundColor: Theme.Text.placeholder.nsColor,
                .font: Theme.Fonts.body.nsFont,
            ])
    }

    @MainActor
    final class Coordinator: NSObject, NSTextFieldDelegate {
        var model: Model
        var focusSeen = 0

        init(model: Model) { self.model = model }

        func controlTextDidChange(_ n: Notification) {
            if let f = n.object as? NSTextField { model.composer = f.stringValue }
        }

        func controlTextDidBeginEditing(_ n: Notification) { model.composerFocused = true }
        func controlTextDidEndEditing(_ n: Notification) { model.composerFocused = false }

        func control(_ control: NSControl, textView: NSTextView, doCommandBy sel: Selector) -> Bool {
            switch sel {
            case #selector(NSResponder.insertNewline(_:)):
                model.send()
                return true
            case #selector(NSResponder.cancelOperation(_:)):
                // esc leaves the field first; a second esc closes the card.
                control.window?.makeFirstResponder(nil)
                return true
            default:
                return false
            }
        }
    }
}

/// The composer's NSTextField: its editor (the text view that has the keys
/// while you type) is an AttachingTextView, so ⌘V and Edit › Paste of a
/// file or an image upload it; and a drop on the field when it is not being
/// edited does the same.
final class ComposerTextField: NSTextField {
    override class var cellClass: AnyClass? {
        get { ComposerCell.self }
        set {}
    }

    weak var attachTarget: AttachingModel? {
        didSet { (cell as? ComposerCell)?.attachTarget = attachTarget }
    }

    override init(frame: NSRect) {
        super.init(frame: frame)
        registerForDraggedTypes(Attachments.dropTypes)
    }

    required init?(coder: NSCoder) {
        super.init(coder: coder)
        registerForDraggedTypes(Attachments.dropTypes)
    }

    override func draggingEntered(_ sender: NSDraggingInfo) -> NSDragOperation {
        AttachmentInput.accepts(sender.draggingPasteboard) ? .copy : []
    }

    override func draggingUpdated(_ sender: NSDraggingInfo) -> NSDragOperation {
        AttachmentInput.accepts(sender.draggingPasteboard) ? .copy : []
    }

    override func performDragOperation(_ sender: NSDraggingInfo) -> Bool {
        AttachmentInput.take(sender.draggingPasteboard, caret: nil, model: attachTarget)
    }
}

/// Hands its field the attaching editor in place of the window's shared one.
final class ComposerCell: NSTextFieldCell {
    weak var attachTarget: AttachingModel? {
        didSet { editor?.attachTarget = attachTarget }
    }

    private var editor: AttachingTextView?

    override func fieldEditor(for controlView: NSView) -> NSTextView? {
        if let editor { return editor }
        let t = AttachingTextView()
        t.isFieldEditor = true
        t.attachTarget = attachTarget
        editor = t
        return t
    }
}
