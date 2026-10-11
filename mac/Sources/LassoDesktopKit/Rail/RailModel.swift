import Combine
import SwiftUI

/// What the rail shows and where keyboard focus is. Pure state: the panel
/// turns key events into calls here, and the actions are injected so the
/// parity harness can render the rail with no side effects.
@MainActor
public final class RailModel: ObservableObject {
    public enum Item: Hashable, Sendable {
        case inbox, slot(String), chat, record, attach, ellipsis
    }

    @Published public var expanded = false
    @Published public var focus: Item?
    /// Keyboard focus is drawn only after a key moved it, as macOS does.
    @Published public var focusVisible = false
    /// A voice turn is being captured or transcribed (the record tool shows it).
    @Published public var recording = false

    public let fleet: FleetModel
    /// Set by the parity harness only: wraps the rail's animation so the
    /// harness can read SwiftUI's own animation clock (ProbedAnimation).
    public var animationProbe: MotionProbe?
    public var onPickAgent: (HostPane) -> Void = { _ in }
    public var onOverflow: () -> Void = {}
    public var onInbox: () -> Void = {}
    public var onChat: () -> Void = {}
    public var onRecord: () -> Void = {}
    public var onAttach: () -> Void = {}

    public init(fleet: FleetModel) { self.fleet = fleet }

    public var slots: [RailSlot] { fleet.railSlots(maxSlots: Theme.Rail.maxSlots.int) }

    public var items: [Item] {
        guard expanded else { return [.inbox] }
        return [.inbox] + slots.map { .slot($0.id) } + [.chat, .record, .attach, .ellipsis]
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
        switch item {
        case .inbox:
            if expanded { onInbox() } else { setExpanded(true) }
        case .ellipsis:
            setExpanded(false)
            focus = .inbox
        case .slot(let id):
            switch slots.first(where: { $0.id == id }) {
            case .agent(let p): onPickAgent(p)
            case .overflow: onOverflow()
            case nil: break
            }
        case .chat: onChat()
        case .record: onRecord()
        case .attach: onAttach()
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
