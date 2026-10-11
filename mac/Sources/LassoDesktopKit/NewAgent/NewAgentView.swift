import AppKit
import SwiftUI

/// The New Agent composer: the prompt is the page, everything else is a chip
/// beneath it. A header with the kind (in a repository, or scratch), the
/// prompt with a preview of the name its first line gives the agent, one row
/// of chips (repository, branch, agent, model, thinking, machine), and a
/// footer with plan-first and Create (⌘⏎).
public struct NewAgentView: View {
    @ObservedObject var model: NewAgentModel

    public init(model: NewAgentModel) { self.model = model }

    private var n: Token { Theme.NewAgent.layout }

    public var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            header
                .padding(.horizontal, n.number("pad"))
                .padding(.top, n.number("pad"))
            prompt
                .padding(.horizontal, n.number("pad"))
                .padding(.top, n.number("gap"))
            namePreview
                .padding(.horizontal, n.number("pad") + Theme.space("xs"))
                .padding(.top, Theme.space("m"))
            chips
                .padding(.horizontal, n.number("pad"))
                .padding(.top, n.number("gap"))
            Spacer(minLength: 0)
            if let e = model.error { errorBanner(e).padding(.horizontal, n.number("pad")).padding(.bottom, Theme.space("m")) }
            footer
        }
        .panelChrome(Theme.NewAgent.layout.size2("size"), anchor: "new")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("New agent")
    }

    // MARK: header

    private var header: some View {
        HStack(alignment: .center, spacing: Theme.space("l")) {
            Text("New agent")
                .font(Theme.Fonts.title.font)
                .foregroundStyle(Theme.Text.primary.color)
            Spacer()
            KindSwitch(kind: model.kind) { k in
                guard k != model.kind else { return }
                model.kind = k
                Task { await model.load() }
            }
            RoundButton(symbol: "xmark", label: "Cancel (esc)") { model.onClose() }
        }
    }

    // MARK: prompt

    private var prompt: some View {
        let r = n.number("radius")
        return ZStack(alignment: .topLeading) {
            PromptEditor(text: $model.prompt, focused: $model.promptFocused)
            if model.prompt.isEmpty {
                Text(model.kind == .git ? "What should it do in this repository?" : "What should it explore?")
                    .font(Theme.Fonts.chat.font)
                    .foregroundStyle(Theme.Text.placeholder.color)
                    .padding(.leading, n.number("prompt_inset") + Theme.space("2xs"))
                    .padding(.top, n.number("prompt_inset"))
                    .allowsHitTesting(false)
            }
        }
        .frame(height: n.number("prompt_h"))
        .background(RoundedRectangle(cornerRadius: r, style: .continuous).fill(Theme.Panel.inner.color("fill")))
        .overlay(
            RoundedRectangle(cornerRadius: r, style: .continuous)
                .strokeBorder(model.promptFocused ? Theme.accent.color.opacity(n.number("lit")) : Theme.Panel.divider.color, lineWidth: 1)
        )
        .animation(.easeOut(duration: Theme.Motion.cardOpen.number("duration")), value: model.promptFocused)
        .parityAnchor("new.prompt")
    }

    /// What the agent will be called: the prompt's first line.
    private var namePreview: some View {
        HStack(spacing: Theme.space("s")) {
            Image(systemName: "tag").font(.system(size: Theme.icon.number("pin")))
            if model.title.isEmpty {
                Text("Its first line becomes the agent's name")
            } else {
                Text("Named").foregroundStyle(Theme.Text.secondary.color)
                Text(model.title).foregroundStyle(Theme.Text.primary.color).lineLimit(1).truncationMode(.tail)
            }
        }
        .font(Theme.Fonts.small.font)
        .foregroundStyle(Theme.Text.secondary.color)
        .accessibilityElement(children: .combine)
    }

    // MARK: chips

    private var chips: some View {
        VStack(alignment: .leading, spacing: Theme.space("s")) {
            // Where it runs.
            HStack(spacing: Theme.space("s")) {
            if model.kind == .git {
                Chip(id: "repo", icon: "folder", value: model.repos.first { $0.path == model.repo }?.name ?? "Repository", label: "Repository", model: model) {
                    ChipList(
                        model: model, searchable: true,
                        items: model.repos.map { .init(value: $0.path, title: $0.name, detail: ($0.path as NSString).abbreviatingWithTildeInPath) },
                        selected: model.repo
                    ) { model.repo = $0 }
                }
                Chip(id: "branch", icon: "arrow.triangle.branch", value: model.baseBranch.isEmpty ? "Base branch" : model.baseBranch, label: "Base branch", model: model) {
                    ChipList(model: model, searchable: true, items: model.branches.map { .init(value: $0, title: $0) }, selected: model.baseBranch) {
                        model.baseBranch = $0
                    }
                }
            } else {
                StaticChip(icon: "sparkles", value: "A fresh folder")
            }
            if model.hosts.count > 1 {
                Chip(id: "host", icon: "desktopcomputer", value: model.host, label: "Machine", model: model) {
                    ChipList(model: model, searchable: model.hosts.count > 8, items: model.hosts.map { .init(value: $0, title: $0) }, selected: model.host) {
                        model.host = $0
                    }
                }
            }
            }
            // Who runs it.
            HStack(spacing: Theme.space("s")) {
            Chip(id: "agent", icon: agentIcon, value: model.harness?.label ?? "Agent", label: "Agent", model: model) {
                ChipList(model: model, searchable: false, items: model.harnesses.map { .init(value: $0.id, title: $0.label) }, selected: model.agent) {
                    model.agent = $0
                    model.model = ""
                    model.effort = ""
                }
            }
            Chip(id: "model", icon: "cpu", value: model.model.isEmpty ? "Default model" : model.model, label: "Model", isDefault: model.model.isEmpty, model: model) {
                ChipList(
                    model: model, searchable: false,
                    items: [.init(value: "", title: "Default")] + (model.harness?.model_suggestions ?? []).map { .init(value: $0, title: $0) },
                    selected: model.model
                ) { model.model = $0 }
            }
            if let levels = model.harness?.effort_levels, !levels.isEmpty {
                Chip(id: "effort", icon: "brain", value: model.effort.isEmpty ? "Default thinking" : model.effort.capitalized, label: "Thinking effort", isDefault: model.effort.isEmpty, model: model) {
                    ChipList(
                        model: model, searchable: false,
                        items: [.init(value: "", title: "Default")] + levels.map { .init(value: $0, title: $0.capitalized) },
                        selected: model.effort
                    ) { model.effort = $0 }
                }
            }
            }
        }
        .parityAnchor("new.chips")
    }

    private var agentIcon: String {
        switch model.agent {
        case "claude": "asterisk"
        case "codex": "chevron.left.forwardslash.chevron.right"
        case "omp", "pi": "circle.hexagongrid"
        default: "terminal"
        }
    }

    // MARK: footer

    private func errorBanner(_ e: String) -> some View {
        HStack(alignment: .top, spacing: Theme.space("s")) {
            Image(systemName: "exclamationmark.circle.fill")
            Text(e).fixedSize(horizontal: false, vertical: true)
        }
        .font(Theme.Fonts.small.font)
        .foregroundStyle(Theme.dangerText.color)
        .padding(Theme.space("m"))
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt).fill(Theme.dangerText.color.opacity(n.number("tint"))))
    }

    private var footer: some View {
        HStack(spacing: Theme.space("l")) {
            if model.harness?.supports_plan_mode == true {
                Switch(on: model.planMode, label: "Plan first") { model.planMode.toggle() }
            }
            Spacer()
            Button { model.onClose() } label: {
                Text("Cancel").font(Theme.Fonts.label.font).foregroundStyle(Theme.Text.secondary.color)
                    .padding(.horizontal, Theme.space("m"))
                    .frame(height: n.number("button_h"))
                    .contentShape(Rectangle())
            }
            .buttonStyle(RailPress())
            .accessibilityLabel("Cancel (esc)")
            CreateButton(enabled: model.canCreate, busy: model.busy) { model.create() }
                .parityAnchor("new.create")
        }
        .padding(.horizontal, n.number("pad"))
        .padding(.vertical, Theme.space("xl"))
        .background(alignment: .top) { Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair")) }
    }
}

// MARK: - pieces

/// Two kinds side by side, the chosen one lifted.
struct KindSwitch: View {
    let kind: NewAgentModel.Kind
    let choose: (NewAgentModel.Kind) -> Void

    var body: some View {
        let n = Theme.NewAgent.layout
        HStack(spacing: Theme.space("2xs")) {
            ForEach(NewAgentModel.Kind.allCases, id: \.self) { k in
                let on = k == kind
                Button { choose(k) } label: {
                    Text(k.label)
                        .font(Theme.Fonts.small.font.weight(on ? .semibold : .regular))
                        .foregroundStyle(on ? Theme.Text.primary.color : Theme.Text.secondary.color)
                        .padding(.horizontal, Theme.space("l"))
                        .frame(height: n.number("switch_h"))
                        .background {
                            if on {
                                Capsule().fill(Theme.Control.round.color("fill"))
                                    .overlay(Capsule().fill(Theme.Rail.hoverFill.color).opacity(n.number("hover")))
                                    .overlay(Capsule().strokeBorder(Theme.Panel.divider.color, lineWidth: 1))
                                    .shadow(color: .black.opacity(n.number("tint") * 3), radius: Theme.space("xs"), y: Theme.space("hair"))
                            }
                        }
                        .contentShape(Capsule())
                }
                .buttonStyle(.plain)
                .accessibilityLabel(k.label)
                .accessibilityAddTraits(on ? .isSelected : [])
            }
        }
        .padding(Theme.space("2xs"))
        .background(Capsule().fill(Theme.Panel.inner.color("fill")))
        .animation(.easeOut(duration: Theme.Motion.cardOpen.number("duration")), value: kind)
        .parityAnchor("new.kind")
    }
}

/// Which chip or picker row the pointer is on (one value: the pointer is
/// over one thing).
@MainActor
final class ChipHover: ObservableObject {
    static let shared = ChipHover()
    @Published var id: String?
}

/// A setting as a chip: its icon, its value, a small chevron; a click opens
/// its picker beneath it.
struct Chip<Content: View>: View {
    let id: String
    let icon: String
    let value: String
    let label: String
    /// The value is a default nobody chose: shown quieter than a choice.
    var isDefault = false
    @ObservedObject var model: NewAgentModel
    @ObservedObject var hover = ChipHover.shared
    @ViewBuilder var content: () -> Content

    var body: some View {
        let n = Theme.NewAgent.layout
        let open = model.openChip == id
        let lit = open || hover.id == id
        Button { model.openChip = open ? nil : id } label: {
            HStack(spacing: Theme.space("s")) {
                Image(systemName: icon).font(.system(size: Theme.icon.number("pin"), weight: .medium))
                    .foregroundStyle(Theme.Text.secondary.color)
                Text(value).font(Theme.Fonts.small.font)
                    .foregroundStyle(isDefault ? Theme.Text.secondary.color : Theme.Text.primary.color).lineLimit(1)
                Image(systemName: "chevron.down").font(.system(size: n.number("chevron"), weight: .semibold))
                    .foregroundStyle(Theme.Text.secondary.color)
            }
            .padding(.horizontal, Theme.space("l"))
            .frame(height: n.number("chip_h"))
            .background {
                ZStack {
                    Capsule().fill(Theme.Control.round.color("fill"))
                    Capsule().fill(Theme.Rail.hoverFill.color).opacity(lit ? n.number("hover") : 0)
                    Capsule().strokeBorder(open ? Theme.accent.color.opacity(n.number("lit")) : Theme.Panel.divider.color, lineWidth: 1)
                }
            }
            .clipShape(Capsule())
            .contentShape(Capsule())
        }
        .buttonStyle(RailPress())
        .onHover { inside in
            if inside { hover.id = id } else if hover.id == id { hover.id = nil }
        }
        .accessibilityLabel("\(label): \(value)")
        .popover(
            isPresented: Binding(get: { model.openChip == id }, set: { if !$0, model.openChip == id { model.openChip = nil } }),
            arrowEdge: .bottom, content: content)
        .parityAnchor("new.chip[\(id)]")
    }
}

/// A chip that says what it is and opens nothing.
struct StaticChip: View {
    let icon: String
    let value: String

    var body: some View {
        HStack(spacing: Theme.space("s")) {
            Image(systemName: icon).font(.system(size: Theme.icon.number("pin"), weight: .medium))
            Text(value).font(Theme.Fonts.small.font)
        }
        .foregroundStyle(Theme.Text.secondary.color)
        .padding(.horizontal, Theme.space("l"))
        .frame(height: Theme.NewAgent.layout.number("chip_h"))
        .overlay(Capsule().strokeBorder(Theme.Panel.divider.color, lineWidth: 1))
        .accessibilityElement(children: .combine)
    }
}

/// A chip's picker: a filter for long lists, the choices, the current one ticked.
struct ChipList: View {
    struct Item: Identifiable {
        var value: String
        var title: String
        var detail: String? = nil
        var id: String { value }
    }

    @ObservedObject var model: NewAgentModel
    let searchable: Bool
    let items: [Item]
    let selected: String
    let choose: (String) -> Void
    @ObservedObject var hover = ChipHover.shared

    var body: some View {
        let n = Theme.NewAgent.layout
        let q = model.chipFilter.trimmingCharacters(in: .whitespaces).lowercased()
        let matches = items.filter { q.isEmpty || $0.title.lowercased().contains(q) || ($0.detail ?? "").lowercased().contains(q) }
        VStack(alignment: .leading, spacing: 0) {
            if searchable {
                HStack(spacing: Theme.space("s")) {
                    Image(systemName: "magnifyingglass").foregroundStyle(Theme.Text.secondary.color)
                    TextField("Filter", text: $model.chipFilter)
                        .textFieldStyle(.plain)
                        .foregroundStyle(Theme.Text.primary.color)
                        .onSubmit { if let first = matches.first { pick(first.value) } }
                }
                .font(Theme.Fonts.body.font)
                .padding(.horizontal, Theme.space("l"))
                .frame(height: n.number("chip_h") + Theme.space("s"))
                .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt).fill(Theme.Panel.inner.color("fill")))
                .padding(Theme.space("m"))
                Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair"))
            }
            ScrollView(.vertical) {
                VStack(alignment: .leading, spacing: Theme.space("hair")) {
                    ForEach(Array(matches.prefix(80))) { item in
                        let rowID = "row:" + item.value
                        Button { pick(item.value) } label: {
                            HStack(spacing: Theme.space("m")) {
                                VStack(alignment: .leading, spacing: Theme.space("hair")) {
                                    Text(item.title).font(Theme.Fonts.body.font).foregroundStyle(Theme.Text.primary.color).lineLimit(1)
                                    if let d = item.detail {
                                        Text(d).font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color).lineLimit(1)
                                            .truncationMode(.middle)
                                    }
                                }
                                Spacer(minLength: Theme.space("m"))
                                if item.value == selected {
                                    Image(systemName: "checkmark").font(.system(size: Theme.icon.number("pin"), weight: .semibold))
                                        .foregroundStyle(Theme.accent.color)
                                }
                            }
                            .padding(.horizontal, Theme.space("l"))
                            .padding(.vertical, Theme.space("s"))
                            .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt).fill(hover.id == rowID ? Theme.Row.hover.color : .clear))
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        .onHover { inside in
                            if inside { hover.id = rowID } else if hover.id == rowID { hover.id = nil }
                        }
                    }
                    if matches.isEmpty {
                        Text("Nothing matches").font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color)
                            .padding(Theme.space("l"))
                    }
                }
                .padding(Theme.space("s"))
            }
        }
        .frame(width: Theme.Agent.picker.size2("size").width)
        .frame(maxHeight: Theme.Agent.picker.size2("size").height)
        .fixedSize(horizontal: false, vertical: true)
        .background(Theme.Panel.surface.color)
    }

    private func pick(_ v: String) {
        choose(v)
        model.openChip = nil
    }
}

/// A small on/off switch with its label.
struct Switch: View {
    let on: Bool
    let label: String
    let toggle: () -> Void

    var body: some View {
        let n = Theme.NewAgent.layout
        Button(action: toggle) {
            HStack(spacing: Theme.space("m")) {
                ZStack(alignment: on ? .trailing : .leading) {
                    Capsule().fill(on ? Theme.accent.color : Theme.Control.round.color("fill"))
                        .overlay(Capsule().strokeBorder(on ? .clear : Theme.Panel.divider.color, lineWidth: 1))
                        .frame(width: n.number("switch_w"), height: n.number("switch_track"))
                    Circle().fill(on ? Theme.Control.pillPrimary.color("fill") : Theme.Text.secondary.color)
                        .frame(width: n.number("switch_track") - Theme.space("xs"), height: n.number("switch_track") - Theme.space("xs"))
                        .padding(Theme.space("2xs"))
                }
                .animation(.easeOut(duration: Theme.Motion.cardOpen.number("duration")), value: on)
                Text(label).font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.primary.color)
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(label)
        .accessibilityValue(on ? "on" : "off")
        .parityAnchor("new.plan")
    }
}

/// Create: the primary button, its shortcut set inside it.
struct CreateButton: View {
    let enabled: Bool
    let busy: Bool
    let action: () -> Void

    var body: some View {
        let n = Theme.NewAgent.layout
        let ink = Theme.Control.pillPrimary.color("text")
        Button(action: action) {
            HStack(spacing: Theme.space("m")) {
                if busy {
                    ProgressView().controlSize(.small).tint(ink)
                }
                Text(busy ? "Creating…" : "Create agent").font(Theme.Fonts.label.font)
                Text("⌘⏎")
                    .font(Theme.Card.keycapFont.font)
                    .padding(.horizontal, Theme.space("xs"))
                    .padding(.vertical, Theme.space("2xs"))
                    .background(RoundedRectangle(cornerRadius: Theme.Card.keycapRadius.pt).fill(ink.opacity(n.number("tint"))))
            }
            .foregroundStyle(ink)
            .padding(.horizontal, Theme.space("xl"))
            .frame(height: n.number("button_h"))
            .background(Capsule().fill(Theme.Control.pillPrimary.color("fill")))
            .opacity(enabled || busy ? 1 : n.number("disabled"))
            .contentShape(Capsule())
        }
        .buttonStyle(RailPress())
        .disabled(!enabled)
        .accessibilityLabel(busy ? "Creating the agent" : "Create the agent (⌘⏎)")
    }
}

/// Lays chips left to right, wrapping to a new line when the width runs out.
struct FlowLayout: Layout {
    var spacing: CGFloat
    var lineSpacing: CGFloat

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let width = proposal.width ?? .infinity
        var x: CGFloat = 0, y: CGFloat = 0, line: CGFloat = 0, widest: CGFloat = 0
        for s in subviews {
            let size = s.sizeThatFits(.unspecified)
            if x > 0, x + size.width > width {
                y += line + lineSpacing
                x = 0
                line = 0
            }
            x += size.width + spacing
            line = max(line, size.height)
            widest = max(widest, x - spacing)
        }
        return CGSize(width: proposal.width ?? widest, height: y + line)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var x = bounds.minX, y = bounds.minY, line: CGFloat = 0
        for s in subviews {
            let size = s.sizeThatFits(.unspecified)
            if x > bounds.minX, x + size.width > bounds.maxX {
                y += line + lineSpacing
                x = bounds.minX
                line = 0
            }
            s.place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(size))
            x += size.width + spacing
            line = max(line, size.height)
        }
    }
}

/// The prompt: a multi-line text view that takes the cursor when the form
/// opens (the Command Line Tools have no focus API SwiftUI can use).
struct PromptEditor: NSViewRepresentable {
    @Binding var text: String
    @Binding var focused: Bool

    func makeCoordinator() -> Coordinator { Coordinator(text: $text, focused: $focused) }

    func makeNSView(context: Context) -> NSScrollView {
        let scroll = NSTextView.scrollableTextView()
        scroll.drawsBackground = false
        scroll.hasVerticalScroller = true
        scroll.autohidesScrollers = true
        let tv = scroll.documentView as! NSTextView
        let inset = Theme.NewAgent.layout.number("prompt_inset")
        tv.textContainerInset = NSSize(width: inset - Theme.space("xs"), height: inset)
        tv.drawsBackground = false
        tv.isRichText = false
        tv.allowsUndo = true
        tv.font = Theme.Fonts.chat.nsFont
        tv.textColor = Theme.Text.primary.nsColor
        tv.insertionPointColor = Theme.accent.nsColor
        tv.delegate = context.coordinator
        tv.setAccessibilityLabel("Prompt")
        DispatchQueue.main.async { tv.window?.makeFirstResponder(tv) }
        return scroll
    }

    func updateNSView(_ scroll: NSScrollView, context: Context) {
        guard let tv = scroll.documentView as? NSTextView, tv.string != text else { return }
        tv.string = text
    }

    @MainActor
    final class Coordinator: NSObject, NSTextViewDelegate {
        var text: Binding<String>
        var focused: Binding<Bool>
        init(text: Binding<String>, focused: Binding<Bool>) {
            self.text = text
            self.focused = focused
        }

        func textDidChange(_ n: Notification) {
            if let tv = n.object as? NSTextView { text.wrappedValue = tv.string }
        }

        func textDidBeginEditing(_ n: Notification) { focused.wrappedValue = true }
        func textDidEndEditing(_ n: Notification) { focused.wrappedValue = false }
    }
}

/// The form's panel: centred high on the screen, with the keyboard.
@MainActor
final class NewAgentController {
    let panel: RailPanel
    let model: NewAgentModel

    init(model: NewAgentModel) {
        self.model = model
        let size = Theme.NewAgent.layout.size2("size")
        let host = NSHostingView(rootView: NewAgentView(model: model))
        host.frame = NSRect(origin: .zero, size: size)
        panel = RailPanel(content: host, size: size)
        panel.hasShadow = true
        panel.level = .floating
        panel.onKey = { [weak self] e in self?.handle(e) ?? false }
    }

    func show() {
        if let v = NSScreen.main?.visibleFrame {
            let s = panel.frame.size
            panel.setFrameOrigin(NSPoint(x: (v.midX - s.width / 2).rounded(), y: (v.midY - s.height / 2 + v.height / 8).rounded()))
        }
        panel.makeKeyAndOrderFront(nil)
        Task { await model.load() }
    }

    func close() { panel.orderOut(nil) }

    /// esc closes an open picker, else cancels; ⌘⏎ creates; the rest is the form's.
    private func handle(_ e: NSEvent) -> Bool {
        if Keys.esc.matches(e) {
            if model.openChip != nil { model.openChip = nil } else { model.onClose() }
            return true
        }
        if e.modifierFlags.contains(.command), e.keyCode == KeyCode.returnKey {
            model.create()
            return true
        }
        return false
    }
}
