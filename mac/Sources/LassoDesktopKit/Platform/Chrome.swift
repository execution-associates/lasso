import AppKit
import Combine
import SwiftUI

/// Keycaps are hidden (the references show none) and appear while the
/// reveal modifier (Theme.revealKeys, ⌘) is held, over the control they
/// drive. The parity harness turns them on to check legend against binding.
@MainActor
public final class KeyHints: ObservableObject {
    public static let shared = KeyHints()
    @Published public var visible = false
    private var monitor: Any?

    public func start() {
        guard monitor == nil else { return }
        monitor = NSEvent.addLocalMonitorForEvents(matching: .flagsChanged) { [weak self] e in
            MainActor.assumeIsolated { self?.visible = e.modifierFlags.contains(.command) }
            return e
        }
    }
}

struct KeyHint: ViewModifier {
    let binding: KeyBinding
    var alignment: Alignment = .bottomTrailing
    @ObservedObject var hints = KeyHints.shared

    func body(content: Content) -> some View {
        content.overlay(alignment: alignment) {
            if hints.visible {
                Keycap(binding).offset(x: 6, y: 6)
            }
        }
    }
}

extension View {
    func keyHint(_ b: KeyBinding, _ alignment: Alignment = .bottomTrailing) -> some View {
        modifier(KeyHint(binding: b, alignment: alignment))
    }
}

/// A keycap: drawn from a KeyBinding, never from a string, so its legend is
/// the key the handler matches.
public struct Keycap: View {
    let binding: KeyBinding

    public init(_ binding: KeyBinding) { self.binding = binding }

    public var body: some View {
        let min = Theme.Card.keycapMinSize.size
        Text(binding.legend)
            .font(.system(size: Theme.Card.keycapFont.number("size"), weight: Theme.Card.keycapFont.weight))
            .foregroundStyle(Theme.Text.primary.color)
            .padding(.horizontal, Theme.Card.keycapPadH.pt)
            .frame(minWidth: min.width, minHeight: min.height)
            .background(RoundedRectangle(cornerRadius: Theme.Card.keycapRadius.pt).fill(Theme.Card.keycapFill.color))
            .parityAnchor("keycap[\(binding.id)]")
            .accessibilityHidden(true)
    }
}

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
    let symbol: String
    var enabled = true
    var size: CGFloat = Theme.Control.round.number("size")
    var fill: Color = Theme.Control.round.color("fill")
    let label: String
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Image(systemName: symbol)
                .font(.system(size: size * 0.42, weight: .semibold))
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

/// A capsule text button: the footer's Kill agent / Discard / Retry.
struct PillButton: View {
    let title: String
    var primary = false
    var textColor: Color = Theme.Text.primary.color
    let label: String
    let action: () -> Void

    var body: some View {
        let p = Theme.Control.pill
        Button(action: action) {
            Text(title)
                .font(Theme.Fonts.label.font)
                .foregroundStyle(primary ? Theme.Control.pillPrimary.color("text") : textColor)
                .padding(.horizontal, p.number("pad_h"))
                .frame(height: p.number("height"))
                .background(Capsule().fill(primary ? Theme.Control.pillPrimary.color("fill") : p.color("fill")))
                .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(label)
    }
}

/// The composer row (ref a2/a5): a round camera button, the field with the
/// microphone inside it, a round send button.
struct ComposerRow<Model: ComposerModel>: View {
    @ObservedObject var model: Model
    @ObservedObject var recording: RecordingModel
    let attach: () -> Void
    let record: () -> Void

    var body: some View {
        let c = Theme.Control.composer
        let h = c.number("height")
        let active = recording.isRecording || recording.phase == .transcribing
        let canSend = !model.composer.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        HStack(spacing: c.number("gap")) {
            RoundButton(symbol: "camera", size: h, fill: c.color("fill"), label: "Attach a screenshot (S)", action: attach)
                .keyHint(Keys.attach)
            HStack(spacing: 8) {
                ZStack(alignment: .leading) {
                    ComposerField(model: model).opacity(active ? 0 : 1)
                    RecordingStrip(model: recording)
                }
                Button(action: record) {
                    Image(systemName: recording.isRecording ? "mic.fill" : "mic")
                        .font(.system(size: 14))
                        .foregroundStyle(recording.isRecording ? Theme.Recording.ring.color : Theme.Text.primary.color)
                        .frame(width: 20, height: h)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel(recording.isRecording ? "Stop recording (V)" : "Record (V)")
                .keyHint(Keys.record)
            }
            .padding(.leading, 14)
            .padding(.trailing, 12)
            .frame(height: h)
            .background(Capsule().fill(c.color("fill")))
            .parityAnchor("composer.field")
            RoundButton(
                symbol: "arrow.up", enabled: canSend, size: h, fill: c.color("fill"), label: "Send (⏎)",
                action: model.send
            )
            .keyHint(Keys.send)
        }
        .parityAnchor("composer")
    }
}
