import AppKit
import SwiftUI

/// What a composer edits: the card's and the chat's models.
@MainActor
public protocol ComposerModel: ObservableObject {
    var composer: String { get set }
    var composerFocused: Bool { get set }
    var focusRequest: Int { get }
    var composerPlaceholder: String { get }
    func send()
}

/// The composer's text field: an NSTextField, because the card needs to give
/// it focus on demand, take it away on esc, and send on Return, and the
/// focus API SwiftUI offers for that is a macro the Command Line Tools lack.
struct ComposerField<Model: ComposerModel>: NSViewRepresentable {
    @ObservedObject var model: Model

    func makeCoordinator() -> Coordinator { Coordinator(model: model) }

    func makeNSView(context: Context) -> NSTextField {
        let f = NSTextField()
        f.isBordered = false
        f.drawsBackground = false
        f.focusRingType = .none
        f.font = .systemFont(ofSize: Theme.Fonts.tree.number("size"))
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
                .foregroundColor: Theme.Text.secondary.nsColor,
                .font: NSFont.systemFont(ofSize: Theme.Fonts.tree.number("size")),
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
