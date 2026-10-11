import AppKit
import SwiftUI

/// Help, from Settings: what the rail, the inbox and the agents are, and every
/// key. The key lists are drawn from the app's own bindings (Keys, the inbox
/// shortcut, the summon key), so they cannot drift from what the keys do.
public struct HelpView: View {
    let inboxShortcut: String
    let summon: String
    var onDone: () -> Void = {}

    public init(inboxShortcut: String, summon: String, onDone: @escaping () -> Void = {}) {
        self.inboxShortcut = inboxShortcut
        self.summon = summon
        self.onDone = onDone
    }

    private var pad: CGFloat { Theme.Panel.inset.pt }

    public var body: some View {
        ScrollView(.vertical) {
            VStack(alignment: .leading, spacing: Theme.space("4xl")) {
                section("The rail") {
                    para("The tab on the right edge of your screen. Rest the pointer on it and the rail slides out with three buttons: Inbox, Agents and Settings.")
                    para("Resting on a button opens it; a click, or a shortcut, also gives it the keyboard. Drag the tab or the rail up and down the edge to where it suits you.")
                }
                section("Inbox") {
                    para("What is waiting on you from your Claude Code and omp agents: questions first, then turns that finished, newest first, then notices from lasso. It opens straight on the newest message; page through the rest.")
                    para("The badge on the rail means something is new since you looked. A card is seen once you land on it.")
                    bullet("Answer", "pick a choice, then Answer (or →). A click only selects, so a stray click never answers. Other lets you answer in your own words.")
                    bullet("Acknowledge", "you have seen it. Nothing reaches the agent; it leaves the inbox until it has something new. Shared with your other devices.")
                    bullet("Kill agent", "ends the agent. Asks first.")
                    bullet("Blocked, no choices", "the agent waits on something lasso cannot read (a permission prompt, say): Focus in herdr and answer it there.")
                }
                section("Agents") {
                    para("Every agent lasso sees, grouped by herdr workspace and named by its herdr label, else its tab.")
                    bullet("Rest on a row", "a preview of its latest turn, beside the list.")
                    bullet("Click a row", "its own window: drag the header to move it, the corner to resize it, − to shrink it to its header. It stays on every desktop and over full-screen apps.")
                    para("Files an agent shows you (Claude Code's shared files, images in its replies) appear in its window: images inline, audio and video as players. The paperclip sends any file to the agent's machine and puts its path in your message.")
                }
                section("Keys anywhere") {
                    keyRow(inboxShortcut, "Open the inbox, with the keyboard in it")
                    keyRow(summon, "Focus the rail; ↑ ↓ to move, ⏎ to open, esc to leave")
                    keyRow("⌥↑ ⌥↓", "Move the rail along the edge (while it has the keyboard)")
                    keyRow("hold ⌘", "Show each control's key")
                }
                section("Keys in a card") {
                    ForEach(Keys.all) { b in keyRow(b.legend, b.action) }
                    keyRow("1–9", "Select a choice")
                    para("A text box you are typing in keeps every key as text. Resting the pointer on an inbox card that opened by hover lends it J, K, X, E and esc only; there E and X ask for a second press, and any other key hands the keyboard back to your app.")
                }
                section("Settings") {
                    para("Which lasso this Mac talks to (production or a dev lasso), the inbox shortcut and arrival sound (or your own, kept on lasso for all your Macs), and permissions. Accessibility is needed only for a double-tap shortcut.")
                }
                HStack {
                    Spacer()
                    PillButton(title: "Done", primary: true, label: "Close Help", action: onDone)
                }
            }
            .padding(pad)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .background(PanelBackground())
        .parityAnchor("help")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Help")
    }

    private func section<C: View>(_ title: String, @ViewBuilder _ content: () -> C) -> some View {
        VStack(alignment: .leading, spacing: Theme.space("m")) {
            Text(title)
                .font(Theme.Fonts.title.font)
                .foregroundStyle(Theme.Text.primary.color)
                .accessibilityAddTraits(.isHeader)
            content()
        }
    }

    private func para(_ s: String) -> some View {
        Text(s)
            .font(Theme.Fonts.body.font)
            .foregroundStyle(Theme.Text.secondary.color)
            .fixedSize(horizontal: false, vertical: true)
    }

    private func bullet(_ name: String, _ s: String) -> some View {
        (Text(name + ": ").foregroundStyle(Theme.Text.primary.color) + Text(s).foregroundStyle(Theme.Text.secondary.color))
            .font(Theme.Fonts.body.font)
            .fixedSize(horizontal: false, vertical: true)
    }

    private func keyRow(_ key: String, _ what: String) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.space("l")) {
            Text(key)
                .font(Theme.Card.keycapFont.font)
                .foregroundStyle(Theme.Text.primary.color)
                .padding(.horizontal, Theme.Card.keycapPadH.pt)
                .frame(minWidth: Theme.Card.keycapMinSize.size.width, minHeight: Theme.Card.keycapMinSize.size.height)
                .background(RoundedRectangle(cornerRadius: Theme.Card.keycapRadius.pt).fill(Theme.Card.keycapFill.color))
            Text(what)
                .font(Theme.Fonts.body.font)
                .foregroundStyle(Theme.Text.secondary.color)
                .fixedSize(horizontal: false, vertical: true)
        }
        .accessibilityElement(children: .combine)
    }
}

extension Token {
    /// A {key, modifiers} shortcut token as macOS menus write it: "⌥⌘L".
    var shortcutLabel: String {
        let mods = value["modifiers"]?.array?.compactMap(\.string) ?? []
        var s = ""
        if mods.contains("control") { s += "⌃" }
        if mods.contains("option") { s += "⌥" }
        if mods.contains("shift") { s += "⇧" }
        if mods.contains("command") { s += "⌘" }
        let key = value["key"]?.string ?? ""
        return s + (key == "space" ? "Space" : key.uppercased())
    }
}

/// Help's window: an ordinary titled window to read and scroll, floating over
/// other apps and on every Space, so it can sit beside what it explains.
@MainActor
final class HelpController {
    private var window: NSPanel?

    func show(inboxShortcut: String) {
        let view = HelpView(inboxShortcut: inboxShortcut, summon: Theme.summonKey.shortcutLabel) { [weak self] in
            self?.window?.orderOut(nil)
        }
        if let w = window {
            w.contentView = NSHostingView(rootView: view)
            w.makeKeyAndOrderFront(nil)
            return
        }
        let size = Theme.Card.size.size
        let w = NSPanel(
            contentRect: NSRect(origin: .zero, size: size),
            styleMask: [.titled, .closable, .resizable, .nonactivatingPanel, .fullSizeContentView],
            backing: .buffered, defer: false)
        w.title = "LassoDesktop Help"
        w.titlebarAppearsTransparent = true
        w.isFloatingPanel = true
        w.level = .floating
        w.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary]
        w.hidesOnDeactivate = false
        w.isReleasedWhenClosed = false
        w.contentView = NSHostingView(rootView: view)
        w.center()
        window = w
        w.makeKeyAndOrderFront(nil)
    }
}
