import Combine
import SwiftUI

/// What the rail shows and where keyboard focus is. Pure state: the panel
/// turns key events into calls here, and the actions are injected so the
/// parity harness can render the rail with no side effects.
@MainActor
public final class RailModel: ObservableObject {
    public enum Item: Hashable, Sendable {
        case inbox, agents, bots, terminal, browser, newAgent, settings
    }

    @Published public var expanded = false
    @Published public var focus: Item?
    /// Keyboard focus is drawn only after a key moved it, as macOS does,
    /// and only until the pointer takes over, the rail hides or it loses
    /// the keyboard: a ring left on would follow the pointer, since resting
    /// on a control moves the focus there.
    @Published public var focusVisible = false
    /// The control under the pointer: one value, since the pointer is over
    /// one thing. Resting on it opens it (`onHoverOpen`).
    @Published public var hovered: Item?
    /// An agent opened a page for the human: the Browser button pulses.
    @Published public var browserPulse = false
    /// The pulse's phase (0..<1) for the parity harness, which draws one
    /// still frame; nil animates it.
    public var browserPulsePhase: Double?

    public let fleet: FleetModel
    /// Set by the parity harness only: wraps the rail's animation so the
    /// harness can read SwiftUI's own animation clock (ProbedAnimation).
    public var animationProbe: MotionProbe?
    /// The inbox: what waits on the human.
    public var onInbox: () -> Void = {}
    /// The agents capsule: the fleet.
    public var onAgents: () -> Void = {}
    /// The Bots button: lasso's bots.
    public var onBots: () -> Void = {}
    /// The plus: the New Agent form.
    public var onNewAgent: () -> Void = {}
    /// The terminal: lasso's herdr in a window of its own.
    public var onTerminal: () -> Void = {}
    /// The globe: lasso's browsers in a window of their own.
    public var onBrowser: () -> Void = {}
    /// The ellipsis: settings.
    public var onSettings: () -> Void = {}
    /// The pointer rested on a control: open what it opens (never toggle it
    /// shut, as a click on an open one does).
    public var onHoverOpen: (Item) -> Void = { _ in }
    /// A drag on the peek or the rail (the controller moves the panel).
    public var onDrag: (DragPhase) -> Void = { _ in }

    public enum DragPhase { case changed, ended }

    public init(fleet: FleetModel) { self.fleet = fleet }

    public var slots: [RailSlot] { fleet.railSlots(maxSlots: Theme.Rail.maxSlots.int) }

    /// The Bots button shows while lasso has bots.
    public var hasBots: Bool { !fleet.bots.isEmpty }

    public var items: [Item] {
        guard expanded else { return [.inbox] }
        var out: [Item] = [.inbox]
        if !slots.isEmpty { out.append(.agents) }
        if hasBots { out.append(.bots) }
        return out + [.terminal, .browser, .newAgent, .settings]
    }

    public func setExpanded(_ on: Bool, animated: Bool = true) {
        guard on != expanded else { return }
        let token = on ? Theme.Motion.railExpand : Theme.Motion.railCollapse
        if animated {
            var animation = token.animation
            if let probe = animationProbe {
                animation = Animation(
                    ProbedAnimation(
                        base: animation, probe: probe, travel: Double(RailMetrics.revealHeight(slots.count, bots: hasBots))))
            }
            withAnimation(animation) { expanded = on }
        } else {
            var t = Transaction()
            t.disablesAnimations = true
            withTransaction(t) { expanded = on }
        }
        if !items.contains(where: { $0 == focus }) { focus = .inbox }
        if !on { focusVisible = false }
    }

    public func activate(_ item: Item) {
        focus = item
        guard expanded else {
            setExpanded(true)
            return
        }
        switch item {
        case .inbox: onInbox()
        case .agents: onAgents()
        case .bots: onBots()
        case .newAgent: onNewAgent()
        case .terminal: onTerminal()
        case .browser: onBrowser()
        case .settings: onSettings()
        }
    }

    public func moveFocus(_ delta: Int) {
        let list = items
        focusVisible = true
        guard let cur = focus, let i = list.firstIndex(of: cur) else {
            focus = list.first
            return
        }
        focus = list[max(0, min(list.count - 1, i + delta))]
    }
}
