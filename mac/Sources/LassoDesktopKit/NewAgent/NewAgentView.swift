import AppKit
import SwiftUI

/// The New Agent form, in the panels' language: where (a repository and its
/// base branch, or a scratch folder) on which machine, which agent and model,
/// and the prompt, whose first line names it. ⌘⏎ creates, esc cancels.
public struct NewAgentView: View {
    @ObservedObject var model: NewAgentModel

    public init(model: NewAgentModel) { self.model = model }

    private var pad: CGFloat { Theme.Panel.inset.pt }

    public var body: some View {
        VStack(alignment: .leading, spacing: Theme.space("xl")) {
            HStack {
                Text(model.kind == .git ? "New agent" : "New scratch agent")
                    .font(Theme.Fonts.title.font)
                    .foregroundStyle(Theme.Text.primary.color)
                Spacer()
                RoundButton(symbol: "xmark", label: "Cancel (esc)") { model.onClose() }
                    .keyHint(Keys.esc)
            }

            HStack(spacing: Theme.space("s")) {
                ForEach(NewAgentModel.Kind.allCases, id: \.self) { k in
                    PillButton(title: k.label, primary: model.kind == k, label: k.label) {
                        model.kind = k
                        Task { await model.load() }
                    }
                }
                Spacer()
            }
            .parityAnchor("new.kind")

            field("Prompt") {
                PromptEditor(text: $model.prompt, placeholder: "What should it do? The first line names it.")
                    .frame(height: Theme.space("5xl") * 5)
                    .padding(Theme.space("m"))
                    .background(RoundedRectangle(cornerRadius: Theme.Panel.inner.number("radius")).fill(Theme.Panel.inner.color("fill")))
                    .parityAnchor("new.prompt")
            }

            if model.kind == .git {
                HStack(spacing: Theme.space("l")) {
                    field("Repository") {
                        SearchPicker(
                            title: model.repos.first { $0.path == model.repo }?.name ?? "Choose…", label: "Repository",
                            items: model.repos.map { ($0.path, $0.name) }, picker: model.repoPicker
                        ) { model.repo = $0 }
                    }
                    field("Base branch") {
                        SearchPicker(
                            title: model.baseBranch.isEmpty ? "Choose…" : model.baseBranch, label: "Base branch",
                            items: model.branches.map { ($0, $0) }, picker: model.branchPicker
                        ) { model.baseBranch = $0 }
                    }
                }
            }

            HStack(spacing: Theme.space("l")) {
                field("Agent") {
                    menu(model.harness?.label ?? "Choose…", label: "Agent") {
                        ForEach(model.harnesses) { h in
                            Button(h.label) {
                                model.agent = h.id
                                model.model = ""
                                model.effort = ""
                            }
                        }
                    }
                }
                field("Model") {
                    menu(model.model.isEmpty ? "Default" : model.model, label: "Model") {
                        Button("Default") { model.model = "" }
                        ForEach(model.harness?.model_suggestions ?? [], id: \.self) { m in Button(m) { model.model = m } }
                    }
                }
                if let levels = model.harness?.effort_levels, !levels.isEmpty {
                    field("Thinking") {
                        menu(model.effort.isEmpty ? "Default" : model.effort, label: "Thinking effort") {
                            Button("Default") { model.effort = "" }
                            ForEach(levels, id: \.self) { e in Button(e) { model.effort = e } }
                        }
                    }
                }
                if model.hosts.count > 1 {
                    field("Machine") {
                        menu(model.host, label: "Machine") {
                            ForEach(model.hosts, id: \.self) { h in Button(h) { model.host = h } }
                        }
                    }
                }
            }

            HStack(spacing: Theme.space("m")) {
                if model.harness?.supports_plan_mode == true {
                    PillButton(title: model.planMode ? "Plan first: on" : "Plan first: off", primary: model.planMode, label: "Plan first") {
                        model.planMode.toggle()
                    }
                }
                Spacer()
                if let e = model.error {
                    Text(e).font(Theme.Fonts.small.font).foregroundStyle(Theme.dangerText.color).lineLimit(2)
                }
                PillButton(title: model.busy ? "Creating…" : "Create", primary: model.canCreate, label: "Create the agent (⌘⏎)") {
                    model.create()
                }
                .parityAnchor("new.create")
            }
            Spacer(minLength: 0)
        }
        .padding(pad)
        .panelChrome(Theme.Card.size.size, anchor: "new")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("New agent")
    }

    private func field<C: View>(_ title: String, @ViewBuilder _ content: () -> C) -> some View {
        VStack(alignment: .leading, spacing: Theme.space("xs")) {
            Text(title).font(Theme.Fonts.header.font).foregroundStyle(Theme.Text.secondary.color)
            content()
        }
    }

    private func menu<C: View>(_ title: String, label: String, @ViewBuilder _ items: () -> C) -> some View {
        Menu { items() } label: {
            Text(title).font(Theme.Fonts.body.font).foregroundStyle(Theme.Text.primary.color).lineLimit(1)
        }
        .menuStyle(.borderlessButton)
        .fixedSize()
        .padding(.horizontal, Theme.Control.pill.number("pad_h"))
        .frame(height: Theme.Control.pill.number("height"))
        .background(Capsule().fill(Theme.Control.pill.color("fill")))
        .accessibilityLabel(label)
    }
}

/// A pick from a long list (183 repositories, 502 branches on titan): the
/// current value as a pill; a click opens a filter field and the matches.
@MainActor
public final class PickerState: ObservableObject {
    @Published public var open = false
    @Published public var filter = ""
    public init() {}
}

struct SearchPicker: View {
    let title: String
    let label: String
    /// (value, what to show), in the order to list them.
    let items: [(String, String)]
    @ObservedObject var picker: PickerState
    let choose: (String) -> Void

    var body: some View {
        Button { picker.open.toggle() } label: {
            HStack(spacing: Theme.space("xs")) {
                Text(title).lineLimit(1)
                Image(systemName: "chevron.down").font(.system(size: Theme.icon.number("pin")))
            }
            .font(Theme.Fonts.body.font)
            .foregroundStyle(Theme.Text.primary.color)
            .padding(.horizontal, Theme.Control.pill.number("pad_h"))
            .frame(height: Theme.Control.pill.number("height"))
            .background(Capsule().fill(Theme.Control.pill.color("fill")))
        }
        .buttonStyle(.plain)
        .accessibilityLabel("\(label): \(title)")
        .popover(isPresented: $picker.open, arrowEdge: .bottom) {
            let q = picker.filter.trimmingCharacters(in: .whitespaces).lowercased()
            let matches = items.filter { q.isEmpty || $0.1.lowercased().contains(q) }
            VStack(alignment: .leading, spacing: Theme.space("s")) {
                TextField("Filter", text: $picker.filter)
                    .textFieldStyle(.roundedBorder)
                    .accessibilityLabel("Filter \(label)")
                ScrollView(.vertical) {
                    VStack(alignment: .leading, spacing: 0) {
                        ForEach(Array(matches.prefix(60)), id: \.0) { value, shown in
                            Button {
                                choose(value)
                                picker.open = false
                                picker.filter = ""
                            } label: {
                                Text(shown).lineLimit(1).frame(maxWidth: .infinity, alignment: .leading)
                                    .padding(.vertical, Theme.space("xs")).contentShape(Rectangle())
                            }
                            .buttonStyle(.plain)
                        }
                        if matches.isEmpty { Text("No match").foregroundStyle(.secondary) }
                    }
                }
            }
            .font(Theme.Fonts.body.font)
            .padding(Theme.space("m"))
            .frame(width: Theme.Agent.picker.size2("size").width, height: Theme.Agent.picker.size2("size").height)
        }
    }
}

/// The prompt: a multi-line text view that takes the cursor when the form
/// opens (the Command Line Tools have no focus API SwiftUI can use).
struct PromptEditor: NSViewRepresentable {
    @Binding var text: String
    let placeholder: String

    func makeCoordinator() -> Coordinator { Coordinator(text: $text) }

    func makeNSView(context: Context) -> NSScrollView {
        let scroll = NSTextView.scrollableTextView()
        scroll.drawsBackground = false
        scroll.hasVerticalScroller = true
        let tv = scroll.documentView as! NSTextView
        tv.drawsBackground = false
        tv.isRichText = false
        tv.allowsUndo = true
        tv.font = Theme.Fonts.body.nsFont
        tv.textColor = Theme.Text.primary.nsColor
        tv.insertionPointColor = Theme.Text.primary.nsColor
        tv.delegate = context.coordinator
        tv.setAccessibilityLabel("Prompt")
        tv.setAccessibilityPlaceholderValue(placeholder)
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
        init(text: Binding<String>) { self.text = text }

        func textDidChange(_ n: Notification) {
            if let tv = n.object as? NSTextView { text.wrappedValue = tv.string }
        }
    }
}

/// The form's panel: centred on the screen, with the keyboard.
@MainActor
final class NewAgentController {
    let panel: RailPanel
    let model: NewAgentModel

    init(model: NewAgentModel) {
        self.model = model
        let size = Theme.Card.size.size
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

    /// esc cancels, ⌘⏎ creates; everything else is the form's.
    private func handle(_ e: NSEvent) -> Bool {
        if Keys.esc.matches(e) {
            model.onClose()
            return true
        }
        if e.modifierFlags.contains(.command), e.keyCode == KeyCode.returnKey {
            model.create()
            return true
        }
        return false
    }
}
