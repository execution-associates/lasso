import AppKit
import SwiftUI

/// An agent in the shared window and preview (Conversation.swift): its
/// header is the herdr pane's (renamed in place from the title), its footer
/// the folder and model, and a window's footer ends in Kill.
extension CardModel: Conversation {
    public var anchor: String { "agent" }
    public var title: String { current.displayName }
    public var statusLine: String { agentStatusLine(current) }
    public var dotState: AgentState { current.state }
    public var items: [ChatItem] { chat?.items ?? [] }
    public var cwd: String? { chat?.cwd }
    public var pendingUI: ChatPayload.PendingUI? { pending }
    public var canCompose: Bool { true }
    public var waiting: String? { killing ? "Killing \(current.displayName)…" : nil }

    public var herdrLabel: String {
        if canShowInHerdr { return "Show \(current.displayName) in herdr" }
        return current.pane_id.isEmpty ? "\(current.displayName) has no pane to show" : "No herdr window for this lasso"
    }

    public func settle() { if renaming { commitRename() } }

    public static var windowKeys: [KeyBinding] { CardKeys.windowSet }
    public static var previewKeys: [KeyBinding] { CardKeys.previewSet }
    public static var framesKey: String { "agentWindowFrames" }
    public var windowKey: String { pane.key }

    public func avatarView(size: CGFloat) -> some View {
        AgentAvatar(agent: current.agent, state: current.state, size: size)
    }

    public func titleView() -> some View { AgentTitle(model: self) }

    public func aboveView() -> some View { EmptyView() }

    public func choicesView() -> some View { StepList(model: self) }

    public func footerView(window: Bool) -> some View {
        AgentFooter(pane: current, model: chat?.model, card: window ? self : nil)
    }
}
