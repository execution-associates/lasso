import SwiftUI

// The rail. At rest only a sliver of a pill shows at the screen edge (the
// peek); hovering it reveals the rail: the inbox (what waits on you), the
// agents capsule (the fleet), the plus (a new agent), the terminal (lasso's
// herdr) and the ellipsis (settings), each floating on its own (no backing).
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
    /// The Bots button shows while lasso has bots.
    static func revealHeight(_ slots: Int, bots: Bool = false) -> CGFloat {
        let agents = slots > 0 ? gap + agentsHeight(slots) : 0
        return agents + CGFloat(bots ? 4 : 3) * (gap + Theme.Rail.ellipsisOuter.pt)
    }

    /// The panel's height holds the tallest rail: every slot and the Bots button.
    static func expandedHeight(_ slots: Int, bots: Bool = true) -> CGFloat { outer + revealHeight(slots, bots: bots) }

    /// Each revealed control's frame in the panel, top-left origin: what the
    /// pointer is over (hover opens it) without asking the view.
    static func itemFrames(_ slots: Int, bots: Bool = false) -> [(RailModel.Item, CGRect)] {
        var out: [(RailModel.Item, CGRect)] = [(.inbox, CGRect(x: margin, y: margin, width: outer, height: outer))]
        var y = margin + outer + gap
        if slots > 0 {
            out.append((.agents, CGRect(x: margin, y: y, width: outer, height: agentsHeight(slots))))
            y += agentsHeight(slots) + gap
        }
        let e = Theme.Rail.ellipsisOuter.pt
        if bots {
            out.append((.bots, CGRect(x: margin + (outer - e) / 2, y: y, width: e, height: e)))
            y += e + gap
        }
        out.append((.newAgent, CGRect(x: margin + (outer - e) / 2, y: y, width: e, height: e)))
        y += e + gap
        out.append((.terminal, CGRect(x: margin + (outer - e) / 2, y: y, width: e, height: e)))
        y += e + gap
        out.append((.settings, CGRect(x: margin + (outer - e) / 2, y: y, width: e, height: e)))
        return out
    }
}

/// A rail group's shape: fully rounded, a fill that is lit from above and a
/// 1 pt rim that catches the light on top and fades below (ref a2).
struct GroupBackground: View {
    /// The pointer is on it: a white overlay, stepped, not animated (rail.hover.fill).
    var hovered = false

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
            shape.fill(Theme.Rail.hoverFill.color).opacity(hovered ? 1 : 0).animation(nil, value: hovered)
        }
    }
}

/// A rail control while pressed: the whole control dims (rail.pressed).
struct RailPress: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label.opacity(configuration.isPressed ? Theme.Rail.pressed.number("opacity") : 1)
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
                    if model.hasBots { bots }
                    plus
                    terminal
                    ellipsis
                }
                .padding(.top, RailMetrics.gap)
                .frame(height: RailMetrics.revealHeight(slots.count, bots: model.hasBots), alignment: .top)
                .modifier(
                    RevealHeight(
                        height: model.expanded ? RailMetrics.revealHeight(slots.count, bots: model.hasBots) : 0, probe: probe))
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
        // Drag the peek or the rail up and down the edge. A click stays a
        // click: the drag claims the pointer only once it has moved.
        .highPriorityGesture(
            DragGesture(minimumDistance: Theme.Rail.drag.number("min_distance"), coordinateSpace: .global)
                .onChanged { _ in model.onDrag(.changed) }
                .onEnded { _ in model.onDrag(.ended) })
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
                    .fill(StateStyle.color(fleet.offline ? .idle : fleet.attention ?? .idle))
                    .frame(width: p.number("dot"), height: p.number("dot"))
                    .parityAnchor("rail.peek.dot")
            }
            .frame(width: p.number("width"), height: p.number("height"))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .parityAnchor("rail.peek")
        .accessibilityLabel("Show lasso controls")
        .accessibilityHint("Drag up or down to move it along the edge")
        .accessibilityValue(
            fleet.offline ? "not connected to lasso" : fleet.attentionCount > 0 ? "\(fleet.attentionCount) waiting on you" : "")
        .help("Show lasso controls")
    }

    private var inbox: some View {
        Button { model.activate(.inbox) } label: {
            ZStack {
                GroupBackground(hovered: model.hovered == .inbox)
                RailIcon(symbol: "tray")
            }
            .frame(width: RailMetrics.outer, height: RailMetrics.outer)
            .contentShape(Circle())
        }
        .buttonStyle(RailPress())
        .parityAnchor("rail.inbox")
        .overlay(alignment: .topLeading) { badge }
        .modifier(FocusRing(on: focused(.inbox)))
        .accessibilityLabel(fleet.offline ? "Connect to lasso" : "Inbox")
        .accessibilityValue(
            fleet.offline ? "no lasso answers" : inboxCount > 0 ? "\(inboxCount) in the inbox" : "nothing in the inbox")
        .help(fleet.offline ? "No lasso answers: connect to one" : "Inbox: what waits on you")
    }

    private var inboxCount: Int { fleet.inbox.count + fleet.notices.count }

    /// How many messages the inbox holds: red while one waits on the human,
    /// green when only finished turns and notices are left; none when empty.
    /// While no lasso answers, a grey "!" instead: the inbox opens the
    /// first-run panel then.
    @ViewBuilder private var badge: some View {
        let count = inboxCount
        if fleet.offline {
            badgeShape("!", StateStyle.color(.idle))
        } else if count > 0 {
            let state: AgentState = fleet.inbox.contains { $0.kind == .waiting } ? .blocked : .done
            badgeShape(count > 99 ? "99+" : "\(count)", StateStyle.color(state))
        }
    }

    private func badgeShape(_ text: String, _ fill: Color) -> some View {
        let b = Theme.Rail.badgeSize
        let ring = Theme.Rail.badgeRing
        let c = Theme.Rail.badgeCenter.size
        // The ring's ink, not white: white reads at 1.5:1 on the green and
        // 2.5:1 on the red; the ink at 11:1 and 7:1.
        return Text(text)
            .font(.system(size: b.number("font"), weight: .bold).monospacedDigit())
            .foregroundStyle(ring.color("color"))
            .padding(.horizontal, b.number("pad_h"))
            .frame(minWidth: b.number("height"), minHeight: b.number("height"), maxHeight: b.number("height"))
            .background(Capsule().fill(fill))
            .parityAnchor("rail.badge")
            .padding(ring.number("width"))
            .background(Capsule().fill(ring.color("color")))
            // position, not offset: offset would leave the anchor's
            // reported frame behind.
            .position(x: c.width, y: c.height)
            .accessibilityHidden(true)
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
            .background(GroupBackground(hovered: model.hovered == .agents))
            .contentShape(Capsule())
        }
        .buttonStyle(RailPress())
        .parityAnchor("rail.agents.capsule")
        .modifier(FocusRing(on: focused(.agents)))
        .accessibilityLabel("Agents")
        .accessibilityValue("\(fleet.agents.count) running")
        .help("Agents")
    }

    /// lasso's bots: its dot is the most urgent one's state.
    private var bots: some View {
        let dot = BotStanding.railDot(fleet.bots)
        let e = Theme.Rail.ellipsisOuter.pt
        let a = Theme.avatar
        return Button { model.activate(.bots) } label: {
            ZStack {
                GroupBackground(hovered: model.hovered == .bots)
                RailIcon(symbol: "face.smiling")
            }
            .frame(width: e, height: e)
            .contentShape(Circle())
            .overlay(alignment: .bottomTrailing) {
                if let dot {
                    Circle()
                        .fill(StateStyle.color(dot))
                        .overlay(Circle().strokeBorder(Theme.Rail.badgeRing.color("color"), lineWidth: a.number("ring")))
                        .frame(width: e * a.number("dot"), height: e * a.number("dot"))
                        .parityAnchor("rail.bots.dot")
                }
            }
        }
        .buttonStyle(RailPress())
        .parityAnchor("rail.bots")
        .modifier(FocusRing(on: focused(.bots)))
        .accessibilityLabel("Bots")
        .accessibilityValue(dot == .blocked ? "one is asking or failed" : dot == .working ? "one is working" : "\(fleet.bots.count) bots")
        .help("Bots")
    }

    private var plus: some View {
        Button { model.activate(.newAgent) } label: {
            ZStack {
                GroupBackground(hovered: model.hovered == .newAgent)
                RailIcon(symbol: "plus")
            }
            .frame(width: Theme.Rail.ellipsisOuter.pt, height: Theme.Rail.ellipsisOuter.pt)
            .contentShape(Circle())
        }
        .buttonStyle(RailPress())
        .parityAnchor("rail.new")
        .modifier(FocusRing(on: focused(.newAgent)))
        .accessibilityLabel("New…")
        .help("New agent or terminal")
    }

    /// lasso's herdr in a terminal window. Like the plus it opens on a click
    /// only: the terminal takes the keyboard.
    private var terminal: some View {
        Button { model.activate(.terminal) } label: {
            ZStack {
                GroupBackground(hovered: model.hovered == .terminal)
                RailIcon(symbol: "terminal")
            }
            .frame(width: Theme.Rail.ellipsisOuter.pt, height: Theme.Rail.ellipsisOuter.pt)
            .contentShape(Circle())
        }
        .buttonStyle(RailPress())
        .parityAnchor("rail.terminal")
        .modifier(FocusRing(on: focused(.terminal)))
        .accessibilityLabel("Open herdr")
        .accessibilityHint("lasso's herdr in a terminal window")
        .help("Open herdr")
    }

    private var ellipsis: some View {
        Button { model.activate(.settings) } label: {
            ZStack {
                GroupBackground(hovered: model.hovered == .settings)
                RailIcon(symbol: "ellipsis")
            }
            .frame(width: Theme.Rail.ellipsisOuter.pt, height: Theme.Rail.ellipsisOuter.pt)
            .contentShape(Circle())
        }
        .buttonStyle(RailPress())
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
