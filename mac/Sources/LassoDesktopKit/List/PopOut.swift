import AppKit
import SwiftUI

// Pop out (PLAN 16): a column row's own window, with the column and its
// preview put away. A plain click opens the window and keeps the column, so
// several can be opened in a row; pop out is the "done here" version. It is
// on the hovered row (in place of its age or state), in the row's preview
// header, on ⌘⏎ and on ⌘-click, in the agents, Bots and Files columns alike.

enum PopOut {
    static let label = "Pop out"
    static let help = "Open its window and put the column away (⌘⏎)"

    /// A click on a row asked to pop out (⌘ held).
    @MainActor static var commandClick: Bool { NSEvent.modifierFlags.contains(.command) }

    /// ⌘⏎ (or ⌘ with the keypad's enter).
    static func isKey(_ e: NSEvent) -> Bool {
        (e.keyCode == KeyCode.returnKey || e.keyCode == KeyCode.keypadEnter)
            && e.modifierFlags.intersection([.command, .control, .option, .shift]) == .command
    }
}

/// The row's pop-out glyph, as small as the row's own controls.
struct PopOutRowButton: View {
    let name: String
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            LucideIcon(.squareArrowOutUpRight, size: Theme.icon.number("control"))
                .foregroundStyle(Theme.Text.secondary.color)
                .frame(width: Theme.List.row.number("action_w"), height: Theme.List.popOut.number("button_h"))
                .contentShape(Rectangle())
        }
        .buttonStyle(RailPress())
        .accessibilityLabel("\(PopOut.label) \(name)")
        .help(PopOut.help)
    }
}

/// A row's second line: what it is on the left, who it came from on the
/// right. The right side keeps its full width up to `cap` of the line (more
/// when the left needs less), then truncates, so neither can push the row
/// wider than its column.
struct LeadTrail: Layout {
    var cap: CGFloat = Theme.List.popOut.number("trail_cap")
    var spacing: CGFloat

    /// The two sides' widths on a line `width` wide, given what each would
    /// take whole.
    static func split(width: CGFloat, lead: CGFloat, trail: CGFloat, spacing: CGFloat, cap: CGFloat) -> (CGFloat, CGFloat) {
        let room = max(0, width - spacing)
        let t = min(trail, max(room * cap, room - lead))
        return (max(0, room - t), t)
    }

    func widths(_ width: CGFloat, _ subviews: Subviews) -> (CGFloat, CGFloat) {
        guard subviews.count == 2 else { return (width, 0) }
        return Self.split(
            width: width, lead: subviews[0].sizeThatFits(.unspecified).width,
            trail: subviews[1].sizeThatFits(.unspecified).width, spacing: spacing, cap: cap)
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let ideal = subviews.reduce(0) { $0 + $1.sizeThatFits(.unspecified).width } + spacing
        let w = proposal.width.flatMap { $0.isFinite ? $0 : nil } ?? ideal
        let h = subviews.map { $0.sizeThatFits(ProposedViewSize(width: w, height: nil)).height }.max() ?? 0
        return CGSize(width: w, height: h)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        guard subviews.count == 2 else {
            subviews.first?.place(at: bounds.origin, proposal: ProposedViewSize(bounds.size))
            return
        }
        let (l, t) = widths(bounds.width, subviews)
        subviews[0].place(at: CGPoint(x: bounds.minX, y: bounds.midY), anchor: .leading, proposal: ProposedViewSize(width: l, height: nil))
        subviews[1].place(at: CGPoint(x: bounds.maxX, y: bounds.midY), anchor: .trailing, proposal: ProposedViewSize(width: t, height: nil))
    }
}
