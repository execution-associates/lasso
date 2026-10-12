import AppKit
import SwiftUI

/// The first-run panel: the app's mark and what this step is for, then the
/// step itself (an address with examples, a password, a sign-in under way,
/// or the lasso it reached and three tips), then the step's buttons.
public struct FirstRunView: View {
    @ObservedObject var model: FirstRunModel

    public init(model: FirstRunModel) { self.model = model }

    private var o: Token { Theme.Onboarding.layout }
    private var pad: CGFloat { o.number("pad") }

    public var body: some View {
        VStack(spacing: 0) {
            hero
            Group {
                switch model.step {
                case .checking: waiting("Asking \(LassoConnector.shown(model.configured())) and this Mac…")
                case .connect: connect
                case .password: passwordForm
                case .signingIn: waiting("Waiting for the browser…")
                case .connected: tipsCard
                }
            }
            .padding(.horizontal, pad)
            .padding(.top, o.number("section_gap"))
            Spacer(minLength: 0)
            footer
        }
        .overlay(alignment: .topTrailing) {
            RoundButton(glyph: .window(.close), label: model.step == .connected ? "Done (esc)" : "Close (esc); the inbox brings this back") {
                model.dismiss()
            }
            .padding(Theme.space("2xl"))
        }
        .panelChrome(o.size2("size"), anchor: "onboarding")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Connect Lasso")
        .animation(.easeOut(duration: Theme.Motion.cardOpen.number("duration")), value: model.step)
    }

    // MARK: hero

    private var title: String {
        switch model.step {
        case .checking: "Looking for your lasso"
        case .connect: "Connect this Mac to your lasso"
        case .password: "Sign in to \(model.pending.map(LassoConnector.shown) ?? "lasso")"
        case .signingIn: "Finish signing in in the window that opened"
        case .connected: "Connected to \(model.connected?.name ?? "lasso")"
        }
    }

    private var subtitle: String {
        switch model.step {
        case .checking:
            "One moment."
        case .connect:
            "lasso runs your agents on one of your machines. Tell this app where it is, and they come to this Mac's screen edge."
        case .password:
            "This lasso asks for the user and password in its UI_AUTH setting. They stay in this Mac's Keychain."
        case .signingIn:
            "\(model.pending.map(LassoConnector.shown) ?? "lasso") signs you in through your browser. This app carries on once you are through."
        case .connected:
            [model.connected?.version, model.connected?.address].compactMap { $0 }.joined(separator: " · ")
        }
    }

    private var badge: (icon: Lucide, color: Color)? {
        switch model.step {
        case .connected: (.check, Theme.State.done.color)
        case .password, .signingIn: (.lock, Theme.accent.color)
        case .checking, .connect: nil
        }
    }

    /// The mark, the title and why: the part that moves the panel.
    private var hero: some View {
        VStack(spacing: 0) {
            AppMark(badge: badge)
            Text(title)
                .font(Theme.Fonts.display.font)
                .foregroundStyle(Theme.Text.primary.color)
                .multilineTextAlignment(.center)
                .fixedSize(horizontal: false, vertical: true)
                .padding(.top, o.number("title_gap"))
                .parityAnchor("onboarding.title")
            Text(subtitle)
                .font(Theme.Fonts.body.font)
                .foregroundStyle(Theme.Text.secondary.color)
                .multilineTextAlignment(.center)
                .lineSpacing(Theme.space("2xs"))
                .fixedSize(horizontal: false, vertical: true)
                .padding(.top, Theme.space("m"))
        }
        .padding(.horizontal, pad + Theme.space("xl"))
        .padding(.top, pad)
        .frame(maxWidth: .infinity)
        .contentShape(Rectangle())
        .gesture(
            DragGesture(minimumDistance: Theme.Rail.drag.number("min_distance"), coordinateSpace: .global)
                .onChanged { _ in model.onDrag(false) }
                .onEnded { _ in model.onDrag(true) })
    }

    // MARK: step 1: the address

    private var connect: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: Theme.space("m")) {
                fieldBox(.address) {
                    LucideIcon(.link, size: Theme.icon.number("control"))
                        .foregroundStyle(model.focus == .address ? Theme.accent.color : Theme.Text.secondary.color)
                    OnboardingField(
                        text: $model.address, focus: $model.focus, field: .address, placeholder: "lasso's address",
                        label: "lasso's address") { model.connect() }
                }
                .parityAnchor("onboarding.address")
                ActionButton(title: model.busy ? "Connecting…" : "Connect", primary: true, busy: model.busy,
                             enabled: !model.address.trimmingCharacters(in: .whitespaces).isEmpty || model.busy,
                             label: "Connect to the lasso at this address (⏎)") { model.connect() }
                    .parityAnchor("onboarding.connect")
            }
            if let n = model.note { NoteBanner(text: n).padding(.top, Theme.space("m")) }
            sectionTitle("Where does yours run?")
                .padding(.top, o.number("section_gap"))
            GroupCard {
                ForEach(Array(FirstRunModel.examples.enumerated()), id: \.element.id) { i, e in
                    exampleRow(e)
                    if i < FirstRunModel.examples.count - 1 { Hairline() }
                }
            }
            .parityAnchor("onboarding.examples")
            .padding(.top, Theme.space("m"))
        }
    }

    private func exampleRow(_ e: FirstRunModel.Example) -> some View {
        let lit = model.hoveredExample == e.id
        return Button { model.use(e) } label: {
            HStack(spacing: Theme.space("l")) {
                IconTile(icon: e.icon)
                VStack(alignment: .leading, spacing: Theme.space("2xs")) {
                    HStack(alignment: .firstTextBaseline, spacing: Theme.space("m")) {
                        Text(e.title).font(Theme.Fonts.body.font).foregroundStyle(Theme.Text.primary.color)
                        Spacer(minLength: Theme.space("m"))
                        Text(e.address)
                            .font(Theme.Fonts.terminal.font)
                            .foregroundStyle(lit ? Theme.accent.color : Theme.Text.secondary.color)
                            .lineLimit(1)
                            .truncationMode(.middle)
                    }
                    Text(e.detail).font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            .padding(.horizontal, Theme.space("xl"))
            .padding(.vertical, Theme.space("l"))
            .background(Rectangle().fill(lit ? Theme.Row.hover.color : .clear))
            .contentShape(Rectangle())
        }
        .buttonStyle(RailPress())
        .onHover { inside in
            if inside { model.hoveredExample = e.id } else if model.hoveredExample == e.id { model.hoveredExample = nil }
        }
        .accessibilityLabel("\(e.title): use \(e.address) as the address")
        .parityAnchor("onboarding.example[\(e.id)]")
    }

    // MARK: step 2: a password

    private var passwordForm: some View {
        VStack(alignment: .leading, spacing: 0) {
            GroupCard {
                credentialRow(.user, .user) {
                    OnboardingField(
                        text: $model.user, focus: $model.focus, field: .user, placeholder: "User", label: "UI_AUTH user",
                        onTab: { model.focus = .password }) { model.primary() }
                }
                Hairline()
                credentialRow(.keyRound, .password) {
                    OnboardingField(
                        text: $model.password, focus: $model.focus, field: .password, placeholder: "Password", secure: true,
                        label: "UI_AUTH password", onTab: { model.focus = .user }) { model.submitPassword() }
                }
            }
            .overlay(
                RoundedRectangle(cornerRadius: o.number("radius"), style: .continuous)
                    .strokeBorder(model.focus == nil ? .clear : Theme.accent.color.opacity(o.number("lit")), lineWidth: 1))
            if let n = model.note { NoteBanner(text: n).padding(.top, Theme.space("m")) }
        }
    }

    private func credentialRow<F: View>(_ icon: Lucide, _ f: FirstRunModel.Field, @ViewBuilder field: () -> F) -> some View {
        HStack(spacing: Theme.space("l")) {
            LucideIcon(icon, size: Theme.icon.number("control"))
                .foregroundStyle(model.focus == f ? Theme.accent.color : Theme.Text.secondary.color)
                .frame(width: Theme.icon.number("control") + Theme.space("xs"))
            field()
        }
        .padding(.horizontal, Theme.space("xl"))
        .frame(height: o.number("field_h"))
    }

    // MARK: waits

    private func waiting(_ what: String) -> some View {
        HStack(spacing: Theme.space("l")) {
            ProgressView().controlSize(.small).tint(Theme.Text.secondary.color)
            Text(what).font(Theme.Fonts.body.font).foregroundStyle(Theme.Text.secondary.color)
                .lineLimit(1).truncationMode(.middle)
        }
        .frame(maxWidth: .infinity)
        .padding(.top, o.number("section_gap"))
        .accessibilityElement(children: .combine)
    }

    // MARK: step 3: connected

    private var tipsCard: some View {
        VStack(alignment: .leading, spacing: 0) {
            sectionTitle("Three things to know")
            GroupCard {
                let tips = model.tips
                ForEach(Array(tips.enumerated()), id: \.element.id) { i, t in
                    tipRow(t)
                    if i < tips.count - 1 { Hairline() }
                }
            }
            .parityAnchor("onboarding.tips")
            .padding(.top, Theme.space("m"))
        }
    }

    private func tipRow(_ t: FirstRunModel.Tip) -> some View {
        HStack(alignment: .top, spacing: Theme.space("l")) {
            RailTile(icon: t.icon)
            VStack(alignment: .leading, spacing: Theme.space("2xs")) {
                HStack(spacing: Theme.space("s")) {
                    Text(t.title).font(Theme.Fonts.body.font.weight(.semibold)).foregroundStyle(Theme.Text.primary.color)
                    Spacer(minLength: Theme.space("s"))
                    ForEach(t.keys, id: \.self) { KeyLabel(keys: $0) }
                }
                Text(t.detail).font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color)
                    .lineSpacing(Theme.space("hair"))
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .padding(.horizontal, Theme.space("xl"))
        .padding(.vertical, Theme.space("xl"))
        .accessibilityElement(children: .combine)
        .parityAnchor("onboarding.tip[\(t.id)]")
    }

    // MARK: pieces

    private func sectionTitle(_ s: String) -> some View {
        Text(s)
            .font(Theme.Fonts.header.font)
            .foregroundStyle(Theme.Text.secondary.color)
            .padding(.horizontal, Theme.space("xs"))
            .accessibilityAddTraits(.isHeader)
    }

    /// A one-line field in a rounded box, lit while it has the keyboard.
    private func fieldBox<C: View>(_ f: FirstRunModel.Field, @ViewBuilder _ content: () -> C) -> some View {
        let shape = RoundedRectangle(cornerRadius: o.number("radius"), style: .continuous)
        return HStack(spacing: Theme.space("m")) { content() }
            .padding(.horizontal, Theme.space("xl"))
            .frame(height: o.number("field_h"))
            .background(shape.fill(Theme.Panel.inner.color("fill")))
            .overlay(
                shape.strokeBorder(
                    model.focus == f ? Theme.accent.color.opacity(o.number("lit")) : Theme.Panel.divider.color, lineWidth: 1))
    }

    @ViewBuilder private var footer: some View {
        switch model.step {
        case .connect:
            Text("esc puts this away; the inbox on the rail brings it back.")
                .font(Theme.Fonts.small.font)
                .foregroundStyle(Theme.Text.placeholder.color)
                .multilineTextAlignment(.center)
                .frame(maxWidth: .infinity)
                .padding(.horizontal, pad)
                .padding(.bottom, o.number("footer_pad"))
        case .checking:
            EmptyView()
        case .password:
            buttons {
                ActionButton(title: "Back", label: "Back to the address") { model.back() }
                Spacer()
                ActionButton(title: model.busy ? "Signing in…" : "Sign in", primary: true, busy: model.busy,
                             label: "Sign in with this user and password (⏎)") { model.submitPassword() }
            }
        case .signingIn:
            buttons {
                Spacer()
                ActionButton(title: "Cancel", label: "Stop signing in") { model.cancelSignIn() }
            }
        case .connected:
            buttons {
                ActionButton(title: "Open Settings", label: "Open Settings") { model.openSettings() }
                    .parityAnchor("onboarding.settings")
                Spacer()
                ActionButton(title: "Done", primary: true, label: "Done (⏎)") { model.done() }
                    .parityAnchor("onboarding.done")
            }
        }
    }

    private func buttons<C: View>(@ViewBuilder _ content: () -> C) -> some View {
        HStack(spacing: Theme.space("l")) { content() }
            .padding(.horizontal, pad)
            .padding(.vertical, o.number("footer_pad"))
            .background(alignment: .top) { Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair")) }
    }
}

// MARK: - pieces

/// The app's icon over a soft glow of the brand colour, with a small badge
/// for the step (a check once connected, a lock while signing in).
struct AppMark: View {
    let badge: (icon: Lucide, color: Color)?

    var body: some View {
        let o = Theme.Onboarding.layout
        let side = o.number("icon")
        let b = o.number("badge")
        ZStack(alignment: .bottomTrailing) {
            Image(nsImage: NSApplication.shared.applicationIconImage)
                .resizable()
                .interpolation(.high)
                .frame(width: side, height: side)
                .background(
                    RadialGradient(
                        colors: [Theme.accent.color.opacity(o.number("glow")), Theme.accent.color.opacity(0)], center: .center,
                        startRadius: 0, endRadius: o.number("glow_radius"))
                        .frame(width: 2 * o.number("glow_radius"), height: 2 * o.number("glow_radius"))
                        .allowsHitTesting(false))
                .parityAnchor("onboarding.icon")
            if let badge {
                Circle().fill(badge.color)
                    .frame(width: b, height: b)
                    .overlay(
                        // Bold, as the web's small checks are (strokeWidth 3).
                        LucideIcon(badge.icon, size: o.number("badge_glyph"), stroke: 3)
                            .foregroundStyle(Theme.Panel.surface.color))
                    .padding(Theme.space("2xs"))
                    .background(Circle().fill(Theme.Panel.surface.color))
                    .parityAnchor("onboarding.badge")
                    .transition(.scale.combined(with: .opacity))
            }
        }
        .accessibilityHidden(true)
    }
}

/// A grouped card: rows split by hairlines, Settings' look.
struct GroupCard<C: View>: View {
    @ViewBuilder let content: () -> C

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: Theme.Onboarding.layout.number("radius"), style: .continuous)
        VStack(alignment: .leading, spacing: 0) { content() }
            .background(shape.fill(Theme.Panel.inner.color("fill")))
            .overlay(shape.strokeBorder(Theme.Panel.divider.color, lineWidth: 1))
            .clipShape(shape)
    }
}

struct Hairline: View {
    var body: some View {
        Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair")).padding(.leading, Theme.space("xl"))
    }
}

/// An example's icon on a small tile in the brand colour.
struct IconTile: View {
    let icon: Lucide

    var body: some View {
        let o = Theme.Onboarding.layout
        LucideIcon(icon, size: Theme.icon.number("control"))
            .foregroundStyle(Theme.accent.color)
            .frame(width: o.number("row_icon"), height: o.number("row_icon"))
            .background(
                RoundedRectangle(cornerRadius: o.number("row_icon_radius"), style: .continuous)
                    .fill(Theme.accent.color.opacity(o.number("tint"))))
    }
}

/// A rail control in miniature, so a tip looks like the thing it names.
struct RailTile: View {
    let icon: Lucide

    var body: some View {
        let side = Theme.Onboarding.layout.number("row_icon")
        ZStack {
            GroupBackground()
            LucideIcon(icon, size: Theme.icon.number("control"))
                .foregroundStyle(Theme.Rail.icon.textColor)
        }
        .frame(width: side, height: side)
    }
}

/// A shortcut as set now, named in the tip as plain text (no keycap).
struct KeyLabel: View {
    let keys: String

    var body: some View {
        Text(keys)
            .font(Theme.Fonts.small.font)
            .foregroundStyle(Theme.Text.secondary.color)
            .frame(minHeight: Theme.Onboarding.layout.number("key_h"))
            .accessibilityLabel("Shortcut \(keys)")
    }
}

/// What went wrong, under the field, kept until the next try.
struct NoteBanner: View {
    let text: String

    var body: some View {
        HStack(alignment: .top, spacing: Theme.space("s")) {
            LucideIcon(.triangleAlert, size: Theme.Fonts.small.number("size"))
            Text(text).fixedSize(horizontal: false, vertical: true)
        }
        .font(Theme.Fonts.small.font)
        .foregroundStyle(Theme.dangerText.color)
        .padding(Theme.space("m"))
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
            RoundedRectangle(cornerRadius: Theme.Row.radius.pt)
                .fill(Theme.dangerText.color.opacity(Theme.Onboarding.layout.number("tint"))))
        .parityAnchor("onboarding.note")
    }
}

/// A footer button: primary (solid) or quiet.
struct ActionButton: View {
    let title: String
    var primary = false
    var busy = false
    var enabled = true
    let label: String
    let action: () -> Void

    var body: some View {
        let o = Theme.Onboarding.layout
        let ink = primary ? Theme.Control.pillPrimary.color("text") : Theme.Text.primary.color
        Button(action: action) {
            HStack(spacing: Theme.space("m")) {
                if busy { ProgressView().controlSize(.small).tint(ink) }
                Text(title).font(Theme.Fonts.label.font)
            }
            .foregroundStyle(ink)
            .padding(.horizontal, Theme.space("2xl"))
            .frame(height: o.number("button_h"))
            .background(Capsule().fill(primary ? Theme.Control.pillPrimary.color("fill") : Theme.Control.round.color("fill")))
            .opacity(enabled ? 1 : Theme.NewAgent.layout.number("disabled"))
            .contentShape(Capsule())
        }
        .buttonStyle(RailPress())
        .disabled(!enabled)
        .accessibilityLabel(label)
    }
}

/// A one-line field that follows the model's focus: it takes the keyboard
/// when its field is the focused one (the Command Line Tools have no focus
/// API SwiftUI can use), says so when clicked into, ⏎ submits, ⇥ moves on.
struct OnboardingField: NSViewRepresentable {
    @Binding var text: String
    @Binding var focus: FirstRunModel.Field?
    let field: FirstRunModel.Field
    let placeholder: String
    var secure = false
    let label: String
    var onTab: () -> Void = {}
    let onSubmit: () -> Void

    func makeCoordinator() -> Coordinator { Coordinator(self) }

    func makeNSView(context: Context) -> NSTextField {
        let f: NSTextField = secure ? NSSecureTextField() : NSTextField()
        f.isBordered = false
        f.isBezeled = false
        f.drawsBackground = false
        f.focusRingType = .none
        f.font = Theme.Fonts.body.nsFont
        f.textColor = Theme.Text.primary.nsColor
        f.cell?.usesSingleLineMode = true
        f.cell?.isScrollable = true
        f.placeholderAttributedString = NSAttributedString(
            string: placeholder, attributes: [.foregroundColor: Theme.Text.placeholder.nsColor, .font: Theme.Fonts.body.nsFont])
        f.stringValue = text
        f.delegate = context.coordinator
        f.setAccessibilityLabel(label)
        claim(f)
        return f
    }

    func updateNSView(_ f: NSTextField, context: Context) {
        context.coordinator.parent = self
        if f.stringValue != text { f.stringValue = text }
        claim(f)
    }

    /// Take the keyboard when this field is the focused one and lacks it.
    private func claim(_ f: NSTextField) {
        guard focus == field else { return }
        DispatchQueue.main.async {
            guard let w = f.window, f.currentEditor() == nil else { return }
            w.makeFirstResponder(f)
            if let e = f.currentEditor() { e.selectedRange = NSRange(location: f.stringValue.utf16.count, length: 0) }
        }
    }

    @MainActor
    final class Coordinator: NSObject, NSTextFieldDelegate {
        var parent: OnboardingField
        init(_ p: OnboardingField) { parent = p }

        func controlTextDidChange(_ n: Notification) {
            if let f = n.object as? NSTextField { parent.text = f.stringValue }
        }

        func controlTextDidBeginEditing(_ n: Notification) {
            if parent.focus != parent.field { parent.focus = parent.field }
        }

        func control(_ control: NSControl, textView: NSTextView, doCommandBy selector: Selector) -> Bool {
            switch selector {
            case #selector(NSResponder.insertNewline(_:)): parent.onSubmit()
            case #selector(NSResponder.insertTab(_:)), #selector(NSResponder.insertBacktab(_:)): parent.onTab()
            default: return false
            }
            return true
        }
    }
}

/// The panel, centred high on the screen, with the keyboard in its field;
/// its header moves it.
@MainActor
final class FirstRunController {
    private(set) var panel: RailPanel
    let model: FirstRunModel
    private var dragFrom: (pointer: NSPoint, origin: NSPoint)?
    /// Counts opens, so a close's fade that ends after a reopen leaves it up.
    private var opens = 0

    init(model: FirstRunModel) {
        self.model = model
        let size = Theme.Onboarding.layout.size2("size")
        let host = NSHostingView(rootView: FirstRunView(model: model))
        host.frame = NSRect(origin: .zero, size: size)
        panel = RailPanel(content: host, size: size)
        panel.hasShadow = true
        panel.level = .floating
        panel.onKey = { [weak self] e in self?.handle(e) ?? false }
        model.onDrag = { [weak self] ended in self?.drag(ended) }
    }

    private(set) var isOpen = false
    var isKey: Bool { panel.isKeyWindow }

    /// `takeKey` false (a hover on the inbox) shows it without the keyboard.
    func show(takeKey: Bool = true) {
        opens += 1
        isOpen = true
        let appearing = !panel.isVisible
        if appearing {
            let v = Screens.underPointer
            let s = panel.frame.size
            panel.setFrameOrigin(NSPoint(x: (v.midX - s.width / 2).rounded(), y: (v.midY - s.height / 2 + v.height / 8).rounded()))
            panel.alphaValue = 0
        }
        // Ordered in first: the fade is the panel's that is showing.
        panel.orderIn(takeKey: takeKey) { panel = $0 }
        if appearing {
            NSAnimationContext.runAnimationGroup { ctx in
                ctx.duration = Theme.Motion.cardOpen.number("duration")
                ctx.timingFunction = CAMediaTimingFunction(name: .easeOut)
                panel.animator().alphaValue = 1
            }
        }
    }

    func close() {
        guard isOpen else { return }
        isOpen = false
        let at = opens
        NSAnimationContext.runAnimationGroup({ ctx in
            ctx.duration = Theme.Motion.cardClose.number("duration")
            panel.animator().alphaValue = 0
        }, completionHandler: { [weak self] in
            MainActor.assumeIsolated {
                guard let self, self.opens == at else { return }
                self.panel.orderOut(nil)
                self.panel.alphaValue = 1
            }
        })
    }

    private func drag(_ ended: Bool) {
        if ended {
            dragFrom = nil
            return
        }
        let p = NSEvent.mouseLocation
        if dragFrom == nil { dragFrom = (p, panel.frame.origin) }
        guard let from = dragFrom else { return }
        panel.setFrameOrigin(NSPoint(x: from.origin.x + p.x - from.pointer.x, y: from.origin.y + p.y - from.pointer.y))
    }

    /// esc puts it away (Done, once connected); ⏎ is the step's main button,
    /// unless an input method is still composing.
    private func handle(_ e: NSEvent) -> Bool {
        if Keys.esc.matches(e) {
            model.dismiss()
            return true
        }
        if e.keyCode == KeyCode.returnKey || e.keyCode == KeyCode.keypadEnter,
           e.modifierFlags.intersection([.command, .option, .control, .shift]).isEmpty
        {
            if (panel.firstResponder as? NSTextView)?.hasMarkedText() == true { return false }
            model.primary()
            return true
        }
        return false
    }
}
