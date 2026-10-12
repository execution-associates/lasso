import AppKit
import SwiftUI

/// The New form: the prompt is the page, everything else is a chip beneath
/// it. A header with the mode (an agent in a repository, a scratch agent, or
/// a terminal), the prompt with a preview of the name its first line gives
/// the agent, the chips (repository, branch, agent, model, thinking,
/// machine), and a footer with plan-first and Create (⌘⏎). As a terminal, the
/// prompt is the command, the chips are the machine and the working
/// directory, and the button is Run.
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
        .overlayPreferenceValue(ChipAnchors.self) { menuLayer($0) }
        .panelChrome(Theme.NewAgent.layout.size2("size"), anchor: "new")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("New")
    }

    // MARK: header

    private var header: some View {
        HStack(alignment: .center, spacing: Theme.space("l")) {
            Text("New")
                .font(Theme.Fonts.title.font)
                .foregroundStyle(Theme.Text.primary.color)
            Spacer()
            KindSwitch(kind: model.kind) { k in
                guard k != model.kind else { return }
                model.kind = k
                Task { await model.load() }
            }
            RoundButton(glyph: .window(.close), label: "Cancel (esc)") { model.onClose() }
        }
        // The header moves the form.
        .contentShape(Rectangle())
        .gesture(
            DragGesture(minimumDistance: Theme.Rail.drag.number("min_distance"), coordinateSpace: .global)
                .onChanged { _ in model.onDrag(false) }
                .onEnded { _ in model.onDrag(true) })
    }

    // MARK: prompt

    private var prompt: some View {
        let r = n.number("radius")
        return ZStack(alignment: .topLeading) {
            VStack(alignment: .leading, spacing: 0) {
                PromptEditor(text: $model.prompt, focused: $model.promptFocused, reclaim: model.openChip == nil, attach: model)
                if !model.attachments.isEmpty {
                    ComposerAttachments(model: model)
                        .padding(.horizontal, n.number("prompt_inset"))
                        .padding(.bottom, n.number("prompt_inset"))
                }
            }
            if model.prompt.isEmpty {
                Text(placeholder)
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

    private var placeholder: String {
        switch model.kind {
        case .git: "What should it do in this repository?"
        case .scratch: "What should it explore?"
        case .terminal: "A command to run, or nothing for a shell"
        }
    }

    /// What the agent will be called: the prompt's first line. A paste or a
    /// drop's progress, or why a file was refused, takes the line until you type.
    /// A terminal's line says how its command runs.
    private var namePreview: some View {
        HStack(spacing: Theme.space("s")) {
            LucideIcon(model.notice != nil ? .paperclip : model.kind == .terminal ? .squareTerminal : .tag, size: Theme.icon.number("pin"))
            if let notice = model.notice {
                Text(notice).lineLimit(1).truncationMode(.middle)
            } else if model.kind == .terminal {
                Text(model.commandFits
                    ? "Several lines run as one script, in a new tab of herdr."
                    : "At most \(NewAgentModel.maxCommand) characters").lineLimit(1)
            } else if model.title.isEmpty {
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

    @ViewBuilder private var chips: some View {
        if model.kind == .terminal { terminalChips } else { agentChips }
    }

    /// A terminal's: the machine and the directory its shell starts in.
    private var terminalChips: some View {
        HStack(spacing: Theme.space("s")) {
            if model.hosts.count > 1 {
                Chip(id: "host", icon: .server, value: model.hostName, label: "Machine", model: model)
            }
            HStack(spacing: Theme.space("s")) {
                LucideIcon(.folder, size: Theme.icon.number("pin"))
                    .foregroundStyle(Theme.Text.secondary.color)
                PlainField(
                    text: $model.cwd, placeholder: "Working directory (optional)",
                    label: "Working directory, optional: an absolute path or ~/… on \(model.hostName)", font: Theme.Fonts.small.nsFont,
                    onSubmit: { model.create() })
            }
            .padding(.horizontal, Theme.space("l"))
            .frame(height: n.number("chip_h"))
            .background(Capsule().fill(Theme.Control.round.color("fill")))
            .overlay(Capsule().strokeBorder(Theme.Panel.divider.color, lineWidth: 1))
            .parityAnchor("new.cwd")
        }
        .parityAnchor("new.chips")
    }

    private var agentChips: some View {
        VStack(alignment: .leading, spacing: Theme.space("s")) {
            // Where it runs.
            HStack(spacing: Theme.space("s")) {
                if model.kind == .git {
                    Chip(id: "repo", icon: .folderGit2, value: model.repos.first { $0.path == model.repo }?.name ?? "Repository", label: "Repository", model: model)
                    Chip(id: "branch", icon: .gitBranch, value: model.baseBranch.isEmpty ? "Base branch" : model.baseBranch, label: "Base branch", model: model)
                } else {
                    StaticChip(icon: .sparkles, value: "A fresh folder")
                }
                if model.hosts.count > 1 {
                    Chip(id: "host", icon: .server, value: model.hostName, label: "Machine", model: model)
                }
            }
            // Who runs it.
            HStack(spacing: Theme.space("s")) {
                Chip(id: "agent", icon: agentIcon, value: model.harness?.label ?? "Agent", label: "Agent", model: model)
                Chip(id: "model", icon: .cpu, value: model.model.isEmpty ? "Default model" : model.model, label: "Model", isDefault: model.model.isEmpty, model: model)
                if let levels = model.harness?.effort_levels, !levels.isEmpty {
                    Chip(id: "effort", icon: .brain, value: model.effort.isEmpty ? "Default thinking" : model.effort.capitalized, label: "Thinking effort", isDefault: model.effort.isEmpty, model: model)
                }
            }
        }
        .parityAnchor("new.chips")
    }

    /// The open chip's menu, drawn inside the form (a system popover from a
    /// floating panel lands wherever the screen has room, half off it at
    /// times). It opens toward the side with more room and stays inside.
    private func menuLayer(_ chips: [String: Anchor<CGRect>]) -> some View {
        GeometryReader { g in
            if let id = model.openChip, let a = chips[id] {
                let chip = g[a]
                // A click outside the menu closes it, or opens the chip it lands on.
                Color.clear.contentShape(Rectangle())
                    .onTapGesture(coordinateSpace: .local) { p in
                        let hit = chips.first { $0.key != id && g[$0.value].contains(p) }?.key
                        model.openChip = hit
                    }
                ChipMenu(model: model, chip: chip, bounds: g.size)
            }
        }
        .animation(.easeOut(duration: Theme.Motion.cardOpen.number("duration")), value: model.openChip)
    }

    private var agentIcon: Lucide { .agent(model.agent) }

    // MARK: footer

    private func errorBanner(_ e: String) -> some View {
        HStack(alignment: .top, spacing: Theme.space("s")) {
            LucideIcon(.triangleAlert, size: Theme.Fonts.small.number("size"))
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
            if model.kind != .terminal, model.harness?.supports_plan_mode == true {
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
            CreateButton(terminal: model.kind == .terminal, enabled: model.canCreate, busy: model.busy) { model.create() }
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

/// Where each chip is, for its menu.
struct ChipAnchors: PreferenceKey {
    static let defaultValue: [String: Anchor<CGRect>] = [:]
    static func reduce(value: inout [String: Anchor<CGRect>], nextValue: () -> [String: Anchor<CGRect>]) {
        value.merge(nextValue()) { $1 }
    }
}

/// A setting as a chip: its icon, its value, a small chevron; a click opens
/// its menu.
struct Chip: View {
    let id: String
    let icon: Lucide
    let value: String
    let label: String
    /// The value is a default nobody chose: shown quieter than a choice.
    var isDefault = false
    @ObservedObject var model: NewAgentModel
    @ObservedObject var hover = ChipHover.shared

    var body: some View {
        let n = Theme.NewAgent.layout
        let open = model.openChip == id
        let lit = open || hover.id == id
        Button { model.openChip = open ? nil : id } label: {
            HStack(spacing: Theme.space("s")) {
                LucideIcon(icon, size: Theme.icon.number("pin"))
                    .foregroundStyle(open ? Theme.accent.color : Theme.Text.secondary.color)
                Text(value).font(Theme.Fonts.small.font)
                    .foregroundStyle(isDefault ? Theme.Text.secondary.color : Theme.Text.primary.color).lineLimit(1)
                LucideIcon(.chevronDown, size: n.number("chevron"))
                    .foregroundStyle(Theme.Text.secondary.color)
                    .rotationEffect(.degrees(open ? 180 : 0))
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
        .animation(.easeOut(duration: Theme.Motion.cardOpen.number("duration")), value: open)
        .accessibilityLabel("\(label): \(value)")
        .accessibilityAddTraits(open ? .isSelected : [])
        .anchorPreference(key: ChipAnchors.self, value: .bounds) { [id: $0] }
        .parityAnchor("new.chip[\(id)]")
    }
}

/// A chip that says what it is and opens nothing.
struct StaticChip: View {
    let icon: Lucide
    let value: String

    var body: some View {
        HStack(spacing: Theme.space("s")) {
            LucideIcon(icon, size: Theme.icon.number("pin"))
            Text(value).font(Theme.Fonts.small.font)
        }
        .foregroundStyle(Theme.Text.secondary.color)
        .padding(.horizontal, Theme.space("l"))
        .frame(height: Theme.NewAgent.layout.number("chip_h"))
        .overlay(Capsule().strokeBorder(Theme.Panel.divider.color, lineWidth: 1))
        .accessibilityElement(children: .combine)
    }
}

/// A chip's menu: a filter for long lists, the choices with the current one
/// ticked, ↑/↓ and ⏎ (NewAgentController) or the pointer to pick. Placed
/// beside its chip, toward the side with more room, inside the form.
struct ChipMenu: View {
    @ObservedObject var model: NewAgentModel
    let chip: CGRect
    let bounds: CGSize

    var body: some View {
        let n = Theme.NewAgent.layout
        let size = Theme.Agent.picker.size2("size")
        let edge = Theme.space("m")
        let gap = n.number("menu_gap")
        let below = bounds.height - chip.maxY - gap - edge
        let above = chip.minY - gap - edge
        let up = below < n.number("menu_min") && above > below
        let room = min(size.height, up ? above : below)
        let x = min(max(chip.minX, edge), bounds.width - edge - size.width)
        let shape = RoundedRectangle(cornerRadius: n.number("menu_radius"), style: .continuous)
        menu(height: room)
            .frame(width: size.width)
            .background(shape.fill(Theme.Panel.surface.color))
            .overlay(shape.strokeBorder(Theme.Panel.divider.color, lineWidth: 1))
            .clipShape(shape)
            .shadow(color: .black.opacity(n.number("menu_shadow_a")), radius: n.number("menu_shadow"), y: n.number("menu_shadow_y"))
            .frame(width: size.width, height: room, alignment: up ? .bottom : .top)
            .position(x: x + size.width / 2, y: up ? chip.minY - gap - room / 2 : chip.maxY + gap + room / 2)
            .transition(.opacity.combined(with: .scale(scale: 1 - n.number("tint") / 3, anchor: up ? .bottom : .top)))
            .accessibilityElement(children: .contain)
            .accessibilityLabel("Choices")
    }

    private func menu(height: CGFloat) -> some View {
        let n = Theme.NewAgent.layout
        let id = model.openChip ?? ""
        let matches = model.matches
        let selected = model.selected(id)
        return VStack(alignment: .leading, spacing: 0) {
            if model.searchable(id) {
                HStack(spacing: Theme.space("s")) {
                    LucideIcon(.search, size: Theme.Fonts.body.number("size")).foregroundStyle(Theme.Text.secondary.color)
                    PlainField(
                        text: $model.chipFilter,
                        placeholder: ["branch": "Filter branches", "repo": "Filter repositories", "model": "Filter or type a model"][id] ?? "Filter",
                        takesFocus: true)
                }
                .font(Theme.Fonts.body.font)
                .padding(.horizontal, Theme.space("l"))
                .frame(height: n.number("chip_h") + Theme.space("s"))
                Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair"))
            }
            ScrollViewReader { proxy in
                ScrollView(.vertical) {
                    VStack(alignment: .leading, spacing: Theme.space("hair")) {
                        ForEach(Array(matches.enumerated()), id: \.element.id) { i, item in
                            row(item, index: i, selected: item.value == selected).id(item.id)
                        }
                        if matches.isEmpty {
                            Text("Nothing matches").font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color)
                                .padding(Theme.space("l"))
                        }
                    }
                    .padding(Theme.space("s"))
                }
                .onAppear { if matches.indices.contains(model.cursor) { proxy.scrollTo(matches[model.cursor].id, anchor: .center) } }
                .onChange(of: model.cursor) { _, c in
                    let m = model.matches
                    if m.indices.contains(c) { proxy.scrollTo(m[c].id) }
                }
            }
        }
        .frame(maxHeight: height)
        .fixedSize(horizontal: false, vertical: true)
    }

    private func row(_ item: NewAgentModel.Choice, index: Int, selected: Bool) -> some View {
        let on = index == model.cursor
        return Button { model.choose(model.openChip ?? "", item.value) } label: {
            HStack(spacing: Theme.space("m")) {
                VStack(alignment: .leading, spacing: Theme.space("hair")) {
                    Text(item.title).font(Theme.Fonts.body.font).foregroundStyle(Theme.Text.primary.color).lineLimit(1)
                    if let d = item.detail {
                        Text(d).font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color).lineLimit(1)
                            .truncationMode(.middle)
                    }
                }
                Spacer(minLength: Theme.space("m"))
                if selected {
                    // Thicker, as the web's small checks are (strokeWidth 3).
                    LucideIcon(.check, size: Theme.icon.number("pin"), stroke: 3)
                        .foregroundStyle(Theme.accent.color)
                }
            }
            .padding(.horizontal, Theme.space("l"))
            .padding(.vertical, Theme.space("s"))
            .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt).fill(on ? Theme.Row.hover.color : .clear))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .onHover { if $0 { model.cursor = index } }
        .accessibilityLabel(item.detail.map { "\(item.title), \($0)" } ?? item.title)
        .accessibilityAddTraits(selected ? .isSelected : [])
    }
}

/// A plain one-line field drawn in the theme's colours. SwiftUI's
/// `TextField` takes the system's placeholder colour whatever its prompt
/// asks for, which on the app's dark panels is dark grey on near-black, so
/// every field with a placeholder is this one. The New Agent menu's filter
/// takes the cursor when the menu opens, so typing filters rather than
/// writing into the prompt beneath; the agents column's waits for a click
/// or a typed key (`made` hands it over for that).
struct PlainField: NSViewRepresentable {
    @Binding var text: String
    let placeholder: String
    var label = "Filter"
    var font: NSFont = Theme.Fonts.body.nsFont
    var secure = false
    var takesFocus = false
    var onSubmit: (() -> Void)?
    var made: (NSTextField) -> Void = { _ in }

    func makeCoordinator() -> Coordinator { Coordinator(text: $text, submit: onSubmit) }

    func makeNSView(context: Context) -> NSTextField {
        let f: NSTextField
        if secure {
            f = NSSecureTextField()
        } else {
            let k = KeyTakingField()
            k.landed = made
            f = k
        }
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
        if takesFocus { DispatchQueue.main.async { f.window?.makeFirstResponder(f) } }
        return f
    }

    /// A click in the field gives its panel the keyboard: a column the
    /// pointer opened has none until then.
    final class KeyTakingField: NSTextField {
        /// Handed the field once it is in a window, where it can take keys.
        var landed: (NSTextField) -> Void = { _ in }

        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            if window != nil { landed(self) }
        }

        override func mouseDown(with event: NSEvent) {
            if let w = window, !w.isKeyWindow { w.makeKey() }
            super.mouseDown(with: event)
        }
    }

    func updateNSView(_ f: NSTextField, context: Context) {
        if f.stringValue != text { f.stringValue = text }
        context.coordinator.submit = onSubmit
    }

    @MainActor
    final class Coordinator: NSObject, NSTextFieldDelegate {
        var text: Binding<String>
        var submit: (() -> Void)?
        init(text: Binding<String>, submit: (() -> Void)?) {
            self.text = text
            self.submit = submit
        }
        func controlTextDidChange(_ n: Notification) {
            if let f = n.object as? NSTextField { text.wrappedValue = f.stringValue }
        }
        func control(_ control: NSControl, textView: NSTextView, doCommandBy selector: Selector) -> Bool {
            guard selector == #selector(NSResponder.insertNewline(_:)), let submit else { return false }
            submit()
            return true
        }
    }
}

/// A small on/off switch with its label.
struct Switch: View {
    let on: Bool
    let label: String
    /// Draw the label beside the switch (a settings row shows its own).
    var showsLabel = true
    var anchor = "new.plan"
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
                if showsLabel { Text(label).font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.primary.color) }
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(label)
        .accessibilityValue(on ? "on" : "off")
        .parityAnchor(anchor)
    }
}

/// Create, or a terminal's Run: the primary button (⌘⏎, said in its label).
struct CreateButton: View {
    var terminal = false
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
                Text(busy ? (terminal ? "Starting…" : "Creating…") : (terminal ? "Run" : "Create agent")).font(Theme.Fonts.label.font)
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
        .accessibilityLabel(
            terminal ? (busy ? "Starting the terminal" : "Run it in a new terminal (⌘⏎)") : (busy ? "Creating the agent" : "Create the agent (⌘⏎)"))
    }
}

/// The prompt: a multi-line text view that takes the cursor when the form
/// opens (the Command Line Tools have no focus API SwiftUI can use).
/// Files and images pasted or dropped on it are uploaded to the chosen
/// machine (AttachingTextView), never pasted as a path on this Mac.
struct PromptEditor: NSViewRepresentable {
    @Binding var text: String
    @Binding var focused: Bool
    /// No menu is open: the cursor comes back here when one closes.
    var reclaim = true
    weak var attach: AttachingModel?

    func makeCoordinator() -> Coordinator { Coordinator(text: $text, focused: $focused) }

    func makeNSView(context: Context) -> NSScrollView {
        let scroll = NSScrollView()
        scroll.drawsBackground = false
        scroll.hasVerticalScroller = true
        scroll.autohidesScrollers = true
        // NSTextView.scrollableTextView() with the attaching text view in it.
        let tv = AttachingTextView()
        tv.attachTarget = attach
        tv.isVerticallyResizable = true
        tv.isHorizontallyResizable = false
        tv.autoresizingMask = [.width]
        tv.maxSize = NSSize(width: CGFloat.greatestFiniteMagnitude, height: CGFloat.greatestFiniteMagnitude)
        tv.textContainer?.widthTracksTextView = true
        tv.textContainer?.containerSize = NSSize(width: scroll.contentSize.width, height: CGFloat.greatestFiniteMagnitude)
        scroll.documentView = tv
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
        guard let tv = scroll.documentView as? NSTextView else { return }
        (tv as? AttachingTextView)?.attachTarget = attach
        if tv.string != text { tv.string = text }
        if reclaim {
            DispatchQueue.main.async {
                if let w = tv.window, w.firstResponder !== tv { w.makeFirstResponder(tv) }
            }
        }
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

/// The form's panel, with the keyboard: where it was last dragged to, else
/// centred high on the screen.
@MainActor
final class NewAgentController {
    private(set) var panel: RailPanel
    let model: NewAgentModel
    static let originKey = "newAgentOrigin"
    private var dragFrom: (pointer: NSPoint, origin: NSPoint)?

    init(model: NewAgentModel) {
        self.model = model
        let size = Theme.NewAgent.layout.size2("size")
        let host = NSHostingView(rootView: NewAgentView(model: model))
        host.frame = NSRect(origin: .zero, size: size)
        panel = RailPanel(content: host, size: size)
        panel.hasShadow = true
        panel.level = .floating
        panel.onKey = { [weak self] e in self?.handle(e) ?? false }
        model.onDrag = { [weak self] ended in self?.drag(ended) }
    }

    func show() {
        let s = panel.frame.size
        let saved = UserDefaults.standard.string(forKey: Self.originKey).map(NSPointFromString)
        if let o = saved, NSScreen.screens.contains(where: { $0.visibleFrame.contains(NSRect(origin: o, size: s)) }) {
            panel.setFrameOrigin(o)
        } else {
            let v = Screens.underPointer
            panel.setFrameOrigin(NSPoint(x: (v.midX - s.width / 2).rounded(), y: (v.midY - s.height / 2 + v.height / 8).rounded()))
        }
        panel.orderIn(takeKey: true) { panel = $0 }
        Task { await model.load() }
    }

    /// Gone, keyboard and all: a created agent's window takes the keyboard
    /// next, so none is left with a form nobody sees.
    func close() {
        if panel.isKeyWindow { panel.resignKey() }
        panel.orderOut(nil)
    }

    private func drag(_ ended: Bool) {
        if ended {
            dragFrom = nil
            UserDefaults.standard.set(NSStringFromPoint(panel.frame.origin), forKey: Self.originKey)
            return
        }
        let p = NSEvent.mouseLocation
        if dragFrom == nil { dragFrom = (p, panel.frame.origin) }
        guard let from = dragFrom else { return }
        panel.setFrameOrigin(NSPoint(x: from.origin.x + p.x - from.pointer.x, y: from.origin.y + p.y - from.pointer.y))
    }

    /// esc closes an open menu, else cancels; ↑/↓ and ⏎ work an open menu;
    /// ⌘⏎ creates; the rest is the form's.
    private func handle(_ e: NSEvent) -> Bool {
        if Keys.esc.matches(e) {
            if model.openChip != nil { model.openChip = nil } else { model.onClose() }
            return true
        }
        if model.openChip != nil, !e.modifierFlags.contains(.command) {
            switch e.keyCode {
            case KeyCode.up: model.moveCursor(-1)
            case KeyCode.down: model.moveCursor(1)
            case KeyCode.returnKey, KeyCode.keypadEnter: model.chooseCursor()
            default: return false
            }
            return true
        }
        if e.modifierFlags.contains(.command), e.keyCode == KeyCode.returnKey {
            model.create()
            return true
        }
        return false
    }
}
