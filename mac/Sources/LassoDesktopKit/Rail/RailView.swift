import SwiftUI

// The rail. At rest only a sliver of a pill shows at the screen edge (the
// peek); hovering it reveals the rail: the inbox (what waits on you), the
// agents capsule (the fleet) and the ellipsis (settings), each floating on
// its own (no backing).
// Layout numbers all come from Theme.Rail.

enum RailMetrics {
    static var outer: CGFloat { Theme.Rail.outer.pt }
    static var rim: CGFloat { Theme.Rail.rimWidth.pt }
    static var gap: CGFloat { Theme.Rail.gap.pt }
    static var glyph: CGFloat { Theme.Rail.glyphSize.pt }
    static var pitch: CGFloat { Theme.Rail.glyphPitch.pt }
    static var margin: CGFloat { Theme.Rail.panelMargin.pt }

    /// The panel spans from the rail's leading margin to the screen edge.
    static var panelWidth: CGFloat { margin + outer + Theme.Rail.edgeInset.pt }

    /// The peek's top: centred on where the inbox will appear, inside the panel.
    static var peekTop: CGFloat { max(0, margin + outer / 2 - Theme.Rail.peek.number("height") / 2) }

    /// Each agent slot is a pitch-tall row with the glyph centred in it, so
    /// the space above the first row is pad_top less half the row's slack.
    static var slotInsetTop: CGFloat { Theme.Rail.padTop.pt - (pitch - glyph) / 2 }
    static var slotInsetBottom: CGFloat { Theme.Rail.padBottom.pt - (pitch - glyph) / 2 }

    static func agentsHeight(_ slots: Int) -> CGFloat {
        2 * rim + slotInsetTop + CGFloat(slots) * pitch + slotInsetBottom
    }

    /// Height of everything below the inbox: the travel of expand/collapse.
    static func revealHeight(_ slots: Int) -> CGFloat {
        let agents = slots > 0 ? gap + agentsHeight(slots) : 0
        return agents + gap + Theme.Rail.ellipsisOuter.pt
    }

    static func expandedHeight(_ slots: Int) -> CGFloat { outer + revealHeight(slots) }
}

/// A rail group's shape: fully rounded, a fill that is lit from above and a
/// 1 pt rim that catches the light on top and fades below (ref a2).
struct GroupBackground: View {
    var body: some View {
        let shape = Capsule(style: .circular)
        ZStack {
            shape.fill(
                LinearGradient(
                    colors: [Theme.Rail.fillTop.color, Theme.Rail.fillBottom.color], startPoint: .top,
                    endPoint: .bottom))
            shape.strokeBorder(
                LinearGradient(
                    colors: [Theme.Rail.rimTop.color, Theme.Rail.rimBottom.color], startPoint: .top,
                    endPoint: .bottom),
                lineWidth: RailMetrics.rim)
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
                    .stroke(Theme.focusRing.color("color"), lineWidth: Theme.focusRing.number("width"))
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

/// The peek's outline in a rect whose trailing side is the screen edge: each
/// end leaves the edge in a concave `flare` arc, then turns down the tab's
/// outer side in a convex `corner` arc.
struct PeekShape: Shape {
    var flare: CGFloat
    var corner: CGFloat

    func path(in r: CGRect) -> Path {
        var p = Path()
        let (x0, x1, y0, y1) = (r.minX, r.maxX, r.minY, r.maxY)
        p.move(to: CGPoint(x: x1, y: y0))
        p.addArc(tangent1End: CGPoint(x: x1, y: y0 + flare), tangent2End: CGPoint(x: x0, y: y0 + flare), radius: flare)
        p.addArc(tangent1End: CGPoint(x: x0, y: y0 + flare), tangent2End: CGPoint(x: x0, y: y1), radius: corner)
        p.addArc(tangent1End: CGPoint(x: x0, y: y1 - flare), tangent2End: CGPoint(x: x1, y: y1 - flare), radius: corner)
        p.addArc(tangent1End: CGPoint(x: x1, y: y1 - flare), tangent2End: CGPoint(x: x1, y: y1), radius: flare)
        p.closeSubpath()
        return p
    }
}

/// The most urgent state among what waits on the human, for the badge dot
/// and the peek's dot.
extension FleetModel {
    public var attention: AgentState? {
        let states = agents.map(\.state) + bots.compactMap { $0.state == "blocked" ? AgentState.blocked : nil }
        return states.min(by: { $0.urgency < $1.urgency }).flatMap { $0 == .blocked || $0 == .done ? $0 : nil }
    }

    public var attentionCount: Int {
        agents.filter { ($0.state == .blocked || $0.state == .done) && bot(for: $0) == nil }.count
            + bots.filter { $0.state == "blocked" }.count
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
        ZStack(alignment: .topLeading) {
            peek
                .offset(x: RailMetrics.panelWidth - Theme.Rail.peek.number("width"), y: RailMetrics.peekTop)
                .opacity(model.expanded ? 0 : 1)
            VStack(spacing: 0) {
                inbox
                VStack(spacing: RailMetrics.gap) {
                    if !slots.isEmpty { agents(slots) }
                    ellipsis
                }
                .padding(.top, RailMetrics.gap)
                .frame(height: RailMetrics.revealHeight(slots.count), alignment: .top)
                .modifier(
                    RevealHeight(height: model.expanded ? RailMetrics.revealHeight(slots.count) : 0, probe: probe))
                .accessibilityHidden(!model.expanded)
            }
            .frame(width: RailMetrics.outer, alignment: .top)
            .padding(.top, RailMetrics.margin)
            // Slides in from the screen edge as it is revealed.
            .offset(x: model.expanded ? RailMetrics.margin : RailMetrics.panelWidth + RailMetrics.margin)
            .accessibilityHidden(!model.expanded)
        }
        .frame(
            width: RailMetrics.panelWidth,
            height: RailMetrics.expandedHeight(Theme.Rail.maxSlots.int) + 2 * RailMetrics.margin, alignment: .topLeading
        )
        .clipped()
    }

    private func focused(_ item: RailModel.Item) -> Bool { model.focusVisible && model.focus == item }

    /// The tab at rest: a flat pill growing out of the screen edge, with the
    /// most urgent state as a dot.
    private var peek: some View {
        let p = Theme.Rail.peek
        return Button { model.activate(.inbox) } label: {
            ZStack {
                PeekShape(flare: p.number("flare"), corner: p.number("corner"))
                    .fill(Theme.Rail.peekFill.color)
                Circle()
                    .fill(StateStyle.color(fleet.attention ?? .idle))
                    .frame(width: p.number("dot"), height: p.number("dot"))
                    .parityAnchor("rail.peek.dot")
            }
            .frame(width: p.number("width"), height: p.number("height"))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .parityAnchor("rail.peek")
        .accessibilityLabel("Show lasso controls")
        .accessibilityValue(fleet.attentionCount > 0 ? "\(fleet.attentionCount) waiting on you" : "")
        .help("Show lasso controls")
    }

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
        .accessibilityLabel("Inbox")
        .accessibilityValue(fleet.attentionCount > 0 ? "\(fleet.attentionCount) waiting on you" : "nothing waiting")
        .help("Inbox: what waits on you")
    }

    @ViewBuilder private var badge: some View {
        if let state = fleet.attention {
            let size = Theme.Rail.badgeSize.pt
            let ring = Theme.Rail.badgeRing
            let c = Theme.Rail.badgeCenter.size
            Circle()
                .fill(StateStyle.color(state))
                .frame(width: size, height: size)
                .parityAnchor("rail.badge")
                .padding(ring.number("width"))
                .background(Circle().fill(ring.color("color")))
                // position, not offset: offset would leave the anchor's
                // reported frame behind.
                .position(x: c.width, y: c.height)
                .accessibilityHidden(true)
        }
    }

    /// The agents capsule is one button: the fleet. Its glyphs are the
    /// agents at a glance, most urgent first.
    private func agents(_ slots: [RailSlot]) -> some View {
        Button { model.activate(.agents) } label: {
            VStack(spacing: 0) {
                ForEach(Array(slots.enumerated()), id: \.element.id) { i, slot in
                    AgentGlyph(slot: slot)
                        .parityAnchor("rail.glyph[\(i)]")
                        .frame(width: RailMetrics.outer - 2 * RailMetrics.rim, height: RailMetrics.pitch)
                }
            }
            .padding(.top, RailMetrics.rim + RailMetrics.slotInsetTop)
            .padding(.bottom, RailMetrics.rim + RailMetrics.slotInsetBottom)
            .frame(width: RailMetrics.outer, height: RailMetrics.agentsHeight(slots.count))
            .background(GroupBackground())
            .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .parityAnchor("rail.agents.capsule")
        .modifier(FocusRing(on: focused(.agents)))
        .accessibilityLabel("Agents")
        .accessibilityValue("\(fleet.agents.count) running")
        .help("Agents")
    }

    private var ellipsis: some View {
        Button { model.activate(.settings) } label: {
            ZStack {
                GroupBackground()
                RailIcon(symbol: "ellipsis")
            }
            .frame(width: Theme.Rail.ellipsisOuter.pt, height: Theme.Rail.ellipsisOuter.pt)
            .contentShape(Circle())
        }
        .buttonStyle(.plain)
        .parityAnchor("rail.ellipsis")
        .modifier(FocusRing(on: focused(.settings)))
        .accessibilityLabel("Settings")
        .help("Settings")
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
                .font(Theme.Rail.overflowFont.font)
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
