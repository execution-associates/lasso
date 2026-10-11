import SwiftUI

// The rail: an inbox circle that is always on the screen edge and, expanded,
// the agents capsule, the tools capsule and the ellipsis below it. Each group
// floats on its own (no backing). Layout numbers all come from Theme.Rail.

enum RailMetrics {
    static var outer: CGFloat { Theme.Rail.outer.pt }
    static var rim: CGFloat { Theme.Rail.rimWidth.pt }
    static var gap: CGFloat { Theme.Rail.gap.pt }
    static var glyph: CGFloat { Theme.Rail.glyphSize.pt }
    static var pitch: CGFloat { Theme.Rail.glyphPitch.pt }
    static var cell: CGFloat { Theme.Rail.toolsCell.pt }

    /// Each agent slot is a pitch-tall row with the glyph centred in it, so
    /// the space above the first row is pad_top less half the row's slack.
    static var slotInsetTop: CGFloat { Theme.Rail.padTop.pt - (pitch - glyph) / 2 }
    static var slotInsetBottom: CGFloat { Theme.Rail.padBottom.pt - (pitch - glyph) / 2 }

    static func agentsHeight(_ slots: Int) -> CGFloat {
        2 * rim + slotInsetTop + CGFloat(slots) * pitch + slotInsetBottom
    }

    static var toolsHeight: CGFloat { 2 * rim + 3 * cell }

    /// Height of everything below the inbox: the travel of expand/collapse.
    static func revealHeight(_ slots: Int) -> CGFloat {
        let agents = slots > 0 ? gap + agentsHeight(slots) : 0
        return agents + gap + toolsHeight + gap + Theme.Rail.ellipsisOuter.pt
    }

    static func expandedHeight(_ slots: Int) -> CGFloat { outer + revealHeight(slots) }
}

/// A rail group's shape: fully rounded, capsule fill with a 1 pt rim inside
/// the outer edge.
struct GroupBackground: View {
    var body: some View {
        ZStack {
            Capsule(style: .circular).fill(Theme.Rail.capsuleFill.color)
            Capsule(style: .circular).strokeBorder(Theme.Rail.capsuleRim.color, lineWidth: RailMetrics.rim)
        }
    }
}

struct RailIcon: View {
    let symbol: String

    var body: some View {
        Image(systemName: symbol)
            .font(.system(size: Theme.Rail.icon.number("size"), weight: Theme.Rail.icon.weight))
            .foregroundStyle(Theme.Rail.icon.textColor)
    }
}

/// The keyboard focus ring (Theme.focusRing), drawn around a group or slot.
struct FocusRing: ViewModifier {
    let on: Bool

    func body(content: Content) -> some View {
        content.overlay {
            if on {
                Capsule(style: .circular)
                    .stroke(
                        RGBA(parsing: Theme.focusRing.value["color"]?.string ?? "")?.color ?? .accentColor,
                        lineWidth: Theme.focusRing.number("width")
                    )
                    .padding(-Theme.focusRing.number("outset") - Theme.focusRing.number("width") / 2)
            }
        }
    }
}

/// The rail's reveal, driven by an animatable height so the parity harness
/// can record exactly what SwiftUI interpolates (MotionProbe).
struct RevealHeight: ViewModifier, @preconcurrency Animatable {
    var height: CGFloat
    let probe: MotionProbe?

    var animatableData: CGFloat {
        get { height }
        set {
            height = newValue
            probe?.record(Double(newValue))
        }
    }

    func body(content: Content) -> some View {
        content.frame(height: max(0, height), alignment: .top).clipped()
    }
}

public struct RailView: View {
    @ObservedObject var model: RailModel
    @ObservedObject var fleet: FleetModel
    @Environment(\.revealProbe) private var probe

    public init(model: RailModel) {
        self.model = model
        fleet = model.fleet
    }

    public var body: some View {
        let slots = model.slots
        VStack(spacing: 0) {
            inbox
            VStack(spacing: RailMetrics.gap) {
                if !slots.isEmpty { agents(slots) }
                tools
                ellipsis
            }
            .padding(.top, RailMetrics.gap)
            .frame(height: RailMetrics.revealHeight(slots.count), alignment: .top)
            .modifier(
                RevealHeight(height: model.expanded ? RailMetrics.revealHeight(slots.count) : 0, probe: probe))
            .accessibilityHidden(!model.expanded)
        }
        .frame(width: RailMetrics.outer, alignment: .top)
    }

    private func focused(_ item: RailModel.Item) -> Bool { model.focusVisible && model.focus == item }

    private var inbox: some View {
        Button { model.activate(.inbox) } label: {
            ZStack {
                GroupBackground()
                RailIcon(symbol: "tray")
            }
            .frame(width: RailMetrics.outer, height: RailMetrics.outer)
            .contentShape(Circle())
        }
        .buttonStyle(.plain)
        .parityAnchor("rail.inbox")
        .overlay(alignment: .topLeading) { badge }
        .modifier(FocusRing(on: focused(.inbox)))
        .accessibilityLabel(model.expanded ? "Show the fleet" : "Show lasso controls")
        .accessibilityValue(fleet.badgeCount > 0 ? "\(fleet.badgeCount) waiting on you" : "")
        .help(model.expanded ? "Show the fleet" : "Show lasso controls")
    }

    @ViewBuilder private var badge: some View {
        let n = fleet.badgeCount
        if n > 0 {
            let size = Theme.Rail.badgeSize.pt
            Text("\(n)")
                .font(.system(size: Theme.Rail.badgeFont.number("size"), weight: Theme.Rail.badgeFont.weight))
                .foregroundStyle(Theme.Rail.badgeFont.textColor)
                .frame(width: size, height: size)
                .background(Circle().fill(Theme.Rail.badgeFill.color))
                .parityAnchor("rail.badge")
                // Centred a quarter of its size in from the inbox's top-leading
                // corner. position, not offset: offset would leave the anchor's
                // reported frame behind.
                .position(x: size / 4, y: size / 4)
                .accessibilityHidden(true)
        }
    }

    private func agents(_ slots: [RailSlot]) -> some View {
        VStack(spacing: 0) {
            ForEach(Array(slots.enumerated()), id: \.element.id) { i, slot in
                Button { model.activate(.slot(slot.id)) } label: {
                    AgentGlyph(slot: slot)
                        .parityAnchor("rail.glyph[\(i)]")
                        .frame(width: RailMetrics.outer - 2 * RailMetrics.rim, height: RailMetrics.pitch)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .modifier(FocusRing(on: focused(.slot(slot.id))))
                .accessibilityLabel(slot.accessibilityLabel)
            }
        }
        .padding(.top, RailMetrics.rim + RailMetrics.slotInsetTop)
        .padding(.bottom, RailMetrics.rim + RailMetrics.slotInsetBottom)
        .frame(width: RailMetrics.outer, height: RailMetrics.agentsHeight(slots.count))
        .background(GroupBackground())
        .parityAnchor("rail.agents.capsule")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Agents")
    }

    private var tools: some View {
        VStack(spacing: 0) {
            tool(.chat, symbol: "bubble.left", label: "Chat with the assistant (coming in a later build)")
            tool(.record, symbol: "mic", label: "Record (coming in a later build)")
            tool(.attach, symbol: "camera.viewfinder", label: "Attach a screenshot (coming in a later build)")
        }
        .padding(RailMetrics.rim)
        .frame(width: RailMetrics.outer, height: RailMetrics.toolsHeight)
        .background(GroupBackground())
        .parityAnchor("rail.tools.capsule")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Tools")
    }

    private func tool(_ item: RailModel.Item, symbol: String, label: String) -> some View {
        Button { model.activate(item) } label: {
            RailIcon(symbol: symbol)
                .frame(width: RailMetrics.cell, height: RailMetrics.cell)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .modifier(FocusRing(on: focused(item)))
        .accessibilityLabel(label)
    }

    private var ellipsis: some View {
        Button { model.activate(.ellipsis) } label: {
            ZStack {
                GroupBackground()
                RailIcon(symbol: "ellipsis")
            }
            .frame(width: Theme.Rail.ellipsisOuter.pt, height: Theme.Rail.ellipsisOuter.pt)
            .contentShape(Circle())
        }
        .buttonStyle(.plain)
        .parityAnchor("rail.ellipsis")
        .modifier(FocusRing(on: focused(.ellipsis)))
        .accessibilityLabel("Hide lasso controls")
        .help("Hide lasso controls")
    }
}

/// One slot of the agents capsule, in the A.1 vocabulary.
struct AgentGlyph: View {
    let slot: RailSlot

    var body: some View {
        let d = RailMetrics.glyph
        switch slot {
        case .agent(let p):
            Group {
                if p.state == .working {
                    Circle().strokeBorder(Theme.State.working.color, lineWidth: Theme.Rail.ringStroke.pt)
                } else {
                    Circle().fill(StateStyle.color(p.state))
                }
            }
            .frame(width: d, height: d)
        case .overflow(let n):
            Text("+\(n)")
                .font(
                    .system(size: Theme.Rail.overflowFont.number("size"), weight: Theme.Rail.overflowFont.weight))
                .foregroundStyle(Theme.Rail.overflowFont.textColor)
                .frame(height: d)
        }
    }
}

public enum StateStyle {
    public static func token(_ s: AgentState) -> Token {
        switch s {
        case .blocked: Theme.State.blocked
        case .done: Theme.State.done
        case .working: Theme.State.working
        case .idle: Theme.State.idle
        }
    }

    public static func color(_ s: AgentState) -> Color { token(s).color }
}

extension RailSlot {
    var accessibilityLabel: String {
        switch self {
        case .agent(let p): "\(p.displayName), \(p.state.label), on \(p.host_label)"
        case .overflow(let n): "\(n) more agents"
        }
    }
}
