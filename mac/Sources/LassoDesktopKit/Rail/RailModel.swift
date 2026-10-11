import Combine
import SwiftUI

/// What the rail shows and where keyboard focus is. Pure state: the panel
/// turns key events into calls here, and the actions are injected so the
/// parity harness can render the rail with no side effects.
@MainActor
public final class RailModel: ObservableObject {
    public enum Item: Hashable, Sendable {
        case inbox, agents, settings
    }

    @Published public var expanded = false
    @Published public var focus: Item?
    /// Keyboard focus is drawn only after a key moved it, as macOS does.
    @Published public var focusVisible = false

    public let fleet: FleetModel
    /// Set by the parity harness only: wraps the rail's animation so the
    /// harness can read SwiftUI's own animation clock (ProbedAnimation).
    public var animationProbe: MotionProbe?
    /// The inbox: what waits on the human.
    public var onInbox: () -> Void = {}
    /// The agents capsule: the fleet.
    public var onAgents: () -> Void = {}
    /// The ellipsis: settings.
    public var onSettings: () -> Void = {}

    public init(fleet: FleetModel) { self.fleet = fleet }

    public var slots: [RailSlot] { fleet.railSlots(maxSlots: Theme.Rail.maxSlots.int) }

    public var items: [Item] {
        guard expanded else { return [.inbox] }
        return slots.isEmpty ? [.inbox, .settings] : [.inbox, .agents, .settings]
    }

    public func setExpanded(_ on: Bool, animated: Bool = true) {
        guard on != expanded else { return }
        let token = on ? Theme.Motion.railExpand : Theme.Motion.railCollapse
        if animated {
            var animation = token.animation
            if let probe = animationProbe {
                animation = Animation(
                    ProbedAnimation(
                        base: animation, probe: probe, travel: Double(RailMetrics.revealHeight(slots.count))))
            }
            withAnimation(animation) { expanded = on }
        } else {
            var t = Transaction()
            t.disablesAnimations = true
            withTransaction(t) { expanded = on }
        }
        if !items.contains(where: { $0 == focus }) { focus = .inbox }
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
