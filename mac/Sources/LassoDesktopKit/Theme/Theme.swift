import AppKit
import SwiftUI

// Every colour, metric, font and duration the app draws with lives here, and
// nowhere else. Each carries its provenance, and the values are spelled the way
// mac/Spec/tokens.json spells them: the parity harness compares the two
// exactly (Theme.all against the spec) before it measures any pixel.

public enum Provenance: Sendable, Equatable {
    /// Taken from a reference. For the brief's values the method was not
    /// supplied, so the note says "brief A.x".
    case measured(String)
    /// Nobody measured it: provisional.
    case unmeasured(String)
    /// Derived from other tokens; the note gives the derivation.
    case inferred(String)

    public var kind: String {
        switch self {
        case .measured: "measured"
        case .unmeasured: "unmeasured"
        case .inferred: "inferred"
        }
    }
}

public struct Token: Sendable {
    public let id: String
    public let value: JSONValue
    public let provenance: Provenance
    /// A token the brief lacks (PLAN.md 2.2).
    public let proposed: Bool

    init(_ id: String, _ value: JSONValue, _ provenance: Provenance, proposed: Bool = false) {
        self.id = id
        self.value = value
        self.provenance = provenance
        self.proposed = proposed
    }

    public var pt: CGFloat { CGFloat(value.double ?? .nan) }
    public var seconds: Double { value.double ?? .nan }
    public var int: Int { Int(value.double ?? 0) }

    public var size: CGSize {
        let a = value.array ?? []
        return CGSize(width: a.first?.double ?? .nan, height: a.dropFirst().first?.double ?? .nan)
    }

    public var rgba: RGBA { RGBA(parsing: value.string ?? "") ?? RGBA(r: 1, g: 0, b: 1, a: 1) }
    public var color: Color { rgba.color }
    public var nsColor: NSColor { rgba.nsColor }

    public subscript(key: String) -> Token {
        Token("\(id).\(key)", value[key] ?? .null, provenance, proposed: proposed)
    }
}

/// An sRGB colour with alpha, parsed from "#RRGGBB" or "rgba(r,g,b,a)".
public struct RGBA: Sendable, Equatable {
    public var r, g, b, a: Double

    public init(r: Double, g: Double, b: Double, a: Double) {
        self.r = r
        self.g = g
        self.b = b
        self.a = a
    }

    public init?(parsing s: String) {
        if s.hasPrefix("#"), s.count == 7, let v = UInt32(s.dropFirst(), radix: 16) {
            self.init(
                r: Double((v >> 16) & 0xFF) / 255, g: Double((v >> 8) & 0xFF) / 255,
                b: Double(v & 0xFF) / 255, a: 1)
        } else if s.hasPrefix("rgba("), s.hasSuffix(")") {
            let parts = s.dropFirst(5).dropLast().split(separator: ",").compactMap {
                Double($0.trimmingCharacters(in: .whitespaces))
            }
            guard parts.count == 4 else { return nil }
            self.init(r: parts[0] / 255, g: parts[1] / 255, b: parts[2] / 255, a: parts[3])
        } else {
            return nil
        }
    }

    public var color: Color { Color(.sRGB, red: r, green: g, blue: b, opacity: a) }
    public var nsColor: NSColor { NSColor(srgbRed: r, green: g, blue: b, alpha: a) }
}

public enum Theme {
    public enum Rail {
        public static let fillWidth = Token("rail.group.fill_width", 40, .measured("brief A.1"))
        public static let rimWidth = Token("rail.group.rim_width", 1, .measured("brief A.1"))
        public static let outer = Token(
            "rail.group.outer", 42, .inferred("fill 40 + 2 x rim 1; PLAN 2.3-1"), proposed: true)
        public static let radius = Token("rail.group.radius", "width/2", .measured("brief A.1"))
        public static let gap = Token("rail.group.gap", 8, .measured("brief A.1"))
        public static let backing = Token("rail.backing", "none", .measured("brief A.1"))
        public static let capsuleFill = Token("rail.capsule.fill", "#1E2020", .measured("brief A.1, flat lower half"))
        public static let capsuleRim = Token("rail.capsule.rim", "#5D5F5F", .measured("brief A.1"))
        public static let padTop = Token("rail.capsule.pad_top", 8.5, .measured("brief A.1"))
        public static let padBottom = Token(
            "rail.capsule.pad_bottom", 8.5, .inferred("mirrors pad_top"), proposed: true)
        public static let glyphSize = Token("rail.glyph.size", 11, .measured("brief A.1"))
        public static let glyphPitch = Token("rail.glyph.pitch", 18, .measured("brief A.1"))
        public static let ringStroke = Token("rail.glyph.ring_stroke", 2, .measured("brief A.1"))
        public static let maxSlots = Token(
            "rail.agents.max_slots", 5, .inferred("332 pt rail matches the brief's ~330 pt travel"), proposed: true)
        public static let overflowFont = Token(
            "rail.overflow.font", ["size": 9, "weight": "semibold", "color": "#949290"],
            .inferred("+N inside an 11 pt cell"), proposed: true)
        public static let toolsCell = Token(
            "rail.tools.cell", 40, .inferred("three glyphs filling the 40 pt width"), proposed: true)
        public static let icon = Token(
            "rail.icon", ["size": 15, "weight": "regular", "color": "#949290"],
            .unmeasured("PLAN 2.2"), proposed: true)
        public static let ellipsisOuter = Token(
            "rail.ellipsis.outer", 42, .inferred("same circle as the inbox"), proposed: true)
        public static let edge = Token("rail.edge", "trailing", .unmeasured("PLAN 2.2"), proposed: true)
        public static let edgeInset = Token("rail.edge_inset", 8, .unmeasured("PLAN 2.2"), proposed: true)
        public static let panelMargin = Token(
            "rail.panel.margin", 10, .inferred("room for the badge offset and focus ring"), proposed: true)
        public static let badgeSize = Token("rail.badge.size", 16, .unmeasured("PLAN 2.2"), proposed: true)
        public static let badgeFill = Token(
            "rail.badge.fill", "#AB362F", .inferred("the needs-you state hue"), proposed: true)
        public static let badgeFont = Token(
            "rail.badge.font", ["size": 10, "weight": "semibold", "color": "#F1EEED"],
            .inferred("keycap legend weight, primary text"), proposed: true)
    }

    public enum State {
        public static let done = Token("state.done", "#57C459", .measured("brief A.1 at-rest, A.5"))
        public static let working = Token("state.working", "#3877F4", .measured("brief A.1, A.5"))
        public static let blocked = Token("state.blocked", "#AB362F", .measured("brief A.1 needs-you, A.5"))
        public static let idle = Token("state.idle", "#828186", .measured("brief A.1, A.5"))
    }

    public enum Card {
        public static let size = Token("card.size", [500, 500], .measured("brief A.2"))
        public static let radius = Token("card.radius", 14, .measured("brief A.2"))
        public static let surface = Token("card.surface", "#110F0D", .measured("brief A.2"))
        public static let footer = Token("card.footer", "#0F0F0C", .measured("brief A.2"))
        public static let keycapFill = Token("card.keycap.fill", "#333130", .measured("brief A.2"))
        public static let keycapFont = Token(
            "card.keycap.font", ["size": 10, "weight": "semibold"], .measured("brief A.2"))
        public static let keycapMinSize = Token(
            "card.keycap.min_size", [18, 18], .unmeasured("PLAN 2.2"), proposed: true)
        public static let keycapRadius = Token("card.keycap.radius", 4, .unmeasured("PLAN 2.2"), proposed: true)
        public static let keycapPadH = Token("card.keycap.pad_h", 4, .unmeasured("PLAN 2.2"), proposed: true)
        public static let unreadSegment = Token(
            "card.unread_segment", ["w": 4, "h": 16, "color": "#3877F4"], .measured("brief A.2"))
        public static let unreadGap = Token(
            "card.unread_segment.gap", 4, .unmeasured("PLAN 2.2"), proposed: true)
        public static let unreadRead = Token(
            "card.unread_segment.read", "rgba(255,255,255,0.12)", .unmeasured("PLAN 2.2"), proposed: true)
        public static let headerHeight = Token("card.header_height", 36, .unmeasured("PLAN 2.2"), proposed: true)
        public static let footerHeight = Token("card.footer_height", 44, .unmeasured("PLAN 2.2"), proposed: true)
        public static let composer = Token(
            "card.composer", ["height": 32, "fill": "rgba(255,255,255,0.06)", "radius": 8],
            .unmeasured("PLAN 2.2"), proposed: true)
        public static let toolRow = Token(
            "card.tool_row", ["height": 24, "radius": 6, "fill": "rgba(255,255,255,0.04)"],
            .unmeasured("PLAN 2.2"), proposed: true)
        public static let machineChip = Token(
            "card.machine_chip", ["radius": 4, "fill": "#333130", "font_size": 11, "pad_h": 6, "pad_v": 2],
            .unmeasured("PLAN 2.2"), proposed: true)
        public static let pad = Token("card.pad", 16, .unmeasured("PLAN 2.2"), proposed: true)
        public static let sectionGap = Token("card.section_gap", 8, .unmeasured("PLAN 2.2"), proposed: true)
        public static let unreadInset = Token(
            "card.unread_bar.inset", 6, .unmeasured("PLAN 2.2"), proposed: true)
        public static let unreadCurrent = Token(
            "card.unread_segment.current", "#F1EEED", .inferred("primary text marks the open card"), proposed: true)
        public static let placement = Token(
            "card.placement", ["side": "leading-of-rail", "gap": 8, "align": "top-of-picked-glyph"],
            .unmeasured("PLAN 2.2"), proposed: true)
    }

    public enum Text {
        public static let primary = Token("text.primary", "#F1EEED", .measured("brief A.2"))
        public static let secondary = Token("text.secondary", "#949290", .measured("brief A.2"))
    }

    public static let accent = Token("accent", "#3877F4", .measured("brief A.2"))
    public static let dangerText = Token("danger.text", "#EB6E62", .measured("brief A.2"))
    public static let dangerFill = Token("danger.fill", "#23211F", .measured("brief A.2"))

    public enum Row {
        public static let hover = Token("row.hover", "rgba(255,255,255,0.06)", .unmeasured("brief A.2 provisional"))
        public static let selected = Token(
            "row.selected", "rgba(255,255,255,0.12)", .unmeasured("brief A.2 provisional"))
        public static let radius = Token("row.radius", 8, .measured("brief A.2"))
        public static let pad = Token("row.pad", ["v": 6, "h": 8], .measured("brief A.2"))
    }

    public enum Fonts {
        public static let name = Token("font.name", ["size": 13, "weight": "medium"], .measured("brief A.2"))
        public static let secondLine = Token(
            "font.second_line", ["size": 11, "weight": "regular"], .measured("brief A.2"))
        public static let header = Token("font.header", ["size": 12, "weight": "regular"], .measured("brief A.2"))
        public static let tree = Token("font.tree", ["size": 12, "weight": "regular"], .measured("brief A.2"))
        public static let terminal = Token(
            "font.terminal", ["size": 12, "weight": "regular", "design": "monospaced"], .measured("brief A.2"))
    }

    public enum List {
        public static let size = Token("list.size", [360, 560], .unmeasured("PLAN 2.2"), proposed: true)
        public static let groupHeaderFont = Token(
            "list.group_header.font", ["size": 11, "weight": "semibold", "color": "#949290"],
            .unmeasured("PLAN 2.2"), proposed: true)
    }

    public static let focusRing = Token(
        "focus.ring", ["width": 2, "color": "#3877F4", "outset": 2], .unmeasured("PLAN 2.2"), proposed: true)

    public enum Recording {
        public static let ring = Token("recording.ring", "#EB6E62", .unmeasured("PLAN 2.2, Q4"), proposed: true)
        public static let meterFill = Token(
            "recording.meter.fill", "rgba(241,238,237,0.60)", .unmeasured("PLAN 2.2"), proposed: true)
        public static let meterHz = Token("recording.meter.hz", 30, .unmeasured("PLAN 2.2"), proposed: true)
    }

    public enum Motion {
        public static let railExpand = Token(
            "motion.rail_expand", ["kind": "spring", "response": 0.2, "damping": 0.9], .measured("brief A.4"))
        public static let railCollapse = Token(
            "motion.rail_collapse", ["kind": "spring", "response": 0.2, "damping": 0.9],
            .measured("brief A.4; its ~133 ms disagrees, PLAN 2.3-2"))
        public static let cardOpen = Token(
            "motion.card_open", ["kind": "easeOut", "duration": 0.07], .measured("brief A.4"))
        public static let cardClose = Token(
            "motion.card_close", ["kind": "easeOut", "duration": 0.07], .measured("brief A.4"))
        public static let toastIn = Token(
            "motion.toast_in", ["kind": "timingCurve", "c": [0, 0, 0.58, 1], "duration": 0.183],
            .inferred("brief A.4: ~183 ms, decelerating, no overshoot"))
        public static let cardPaging = Token(
            "motion.card_paging", ["kind": "easeInOut", "duration": 0.2], .unmeasured("brief A.4 provisional"))
    }

    public enum Voice {
        public static let sampleRate = Token("voice.sample_rate", 16000, .measured("brief C"))
        public static let vadFloorWindow = Token(
            "voice.vad.floor_window", 300, .unmeasured("PLAN 2.2"), proposed: true)
        public static let vadThreshold = Token(
            "voice.vad.threshold_over_floor", 12, .unmeasured("PLAN 2.2"), proposed: true)
        public static let vadEndSilence = Token(
            "voice.vad.end_silence", 900, .unmeasured("PLAN 2.2"), proposed: true)
        public static let vadMinSpeech = Token(
            "voice.vad.min_speech", 400, .unmeasured("PLAN 2.2"), proposed: true)
        public static let maxTurn = Token(
            "voice.max_turn", 120, .inferred("matches /api/transcribe's limit"), proposed: true)
        public static let pttSplit = Token(
            "voice.ptt.tap_hold_split", 250, .unmeasured("PLAN 2.2"), proposed: true)
    }

    public enum Live {
        public static let pollAllPanes = Token(
            "live.poll.all_panes", 5, .inferred("src/web/src/lib/agents.ts:337"), proposed: true)
        public static let pollBots = Token("live.poll.bots", 3, .inferred("src/web/src/lib/bots.ts"), proposed: true)
        public static let pollChat = Token(
            "live.poll.chat_open", 2, .inferred("src/web/src/components/ChatView.tsx"), proposed: true)
        public static let sseWatchdog = Token(
            "live.sse.watchdog", 60, .inferred("server pings every 25 s, src/main.go:2847"), proposed: true)
        public static let backoff = Token(
            "live.reconnect.backoff", ["min": 1, "max": 30, "jitter": 0.2], .unmeasured("PLAN 2.2"),
            proposed: true)
    }

    public static let summonKey = Token(
        "keys.summon", ["key": "l", "modifiers": ["option", "command"]], .unmeasured("PLAN 2.2"), proposed: true)

    /// Every token, for the parity harness's static check. A token missing
    /// here, or present here and not in tokens.json, fails that check.
    public static let all: [Token] = [
        Rail.fillWidth, Rail.rimWidth, Rail.outer, Rail.radius, Rail.gap, Rail.backing, Rail.capsuleFill,
        Rail.capsuleRim, Rail.padTop, Rail.padBottom, Rail.glyphSize, Rail.glyphPitch, Rail.ringStroke,
        Rail.maxSlots, Rail.overflowFont, Rail.toolsCell, Rail.icon, Rail.ellipsisOuter, Rail.edge,
        Rail.edgeInset, Rail.panelMargin, Rail.badgeSize, Rail.badgeFill, Rail.badgeFont,
        State.done, State.working, State.blocked, State.idle,
        Card.size, Card.radius, Card.surface, Card.footer, Card.keycapFill, Card.keycapFont, Card.keycapMinSize,
        Card.keycapRadius, Card.keycapPadH, Card.unreadSegment, Card.unreadGap, Card.unreadRead,
        Card.headerHeight, Card.footerHeight, Card.composer, Card.toolRow, Card.machineChip, Card.placement,
        Card.pad, Card.sectionGap, Card.unreadInset, Card.unreadCurrent,
        Text.primary, Text.secondary, accent, dangerText, dangerFill,
        Row.hover, Row.selected, Row.radius, Row.pad,
        Fonts.name, Fonts.secondLine, Fonts.header, Fonts.tree, Fonts.terminal,
        List.size, List.groupHeaderFont, focusRing,
        Recording.ring, Recording.meterFill, Recording.meterHz,
        Motion.railExpand, Motion.railCollapse, Motion.cardOpen, Motion.cardClose, Motion.toastIn,
        Motion.cardPaging,
        Voice.sampleRate, Voice.vadFloorWindow, Voice.vadThreshold, Voice.vadEndSilence, Voice.vadMinSpeech,
        Voice.maxTurn, Voice.pttSplit,
        Live.pollAllPanes, Live.pollBots, Live.pollChat, Live.sseWatchdog, Live.backoff,
        summonKey,
    ]
}

// MARK: - Typed views of token values

extension Token {
    /// A font token: {size, weight, design?}.
    public var font: Font {
        let size = CGFloat(value["size"]?.double ?? 12)
        let design: Font.Design = value["design"]?.string == "monospaced" ? .monospaced : .default
        return .system(size: size, weight: weight, design: design)
    }

    public var weight: Font.Weight {
        switch value["weight"]?.string {
        case "medium": .medium
        case "semibold": .semibold
        case "bold": .bold
        default: .regular
        }
    }

    /// The colour inside an object token ({..., color}).
    public var textColor: Color { RGBA(parsing: value["color"]?.string ?? "")?.color ?? Theme.Text.primary.color }

    public func number(_ key: String) -> CGFloat { CGFloat(value[key]?.double ?? .nan) }

    /// The SwiftUI animation a motion token describes.
    public var animation: Animation {
        let d = value["duration"]?.double ?? 0
        switch value["kind"]?.string {
        case "spring":
            return .spring(
                response: value["response"]?.double ?? 0.2, dampingFraction: value["damping"]?.double ?? 1)
        case "easeOut": return .easeOut(duration: d)
        case "easeIn": return .easeIn(duration: d)
        case "easeInOut": return .easeInOut(duration: d)
        case "timingCurve":
            let c = (value["c"]?.array ?? []).compactMap(\.double)
            guard c.count == 4 else { return .linear(duration: d) }
            return .timingCurve(c[0], c[1], c[2], c[3], duration: d)
        default: return .linear(duration: d)
        }
    }
}

/// Run a state change under a motion token's animation. The parity harness
/// passes a probe to read the animation on SwiftUI's own clock.
@MainActor
public func withAnimationToken(
    _ token: Token, probe: MotionProbe? = nil, travel: Double = 1, _ body: () -> Void
) {
    var a = token.animation
    if let probe { a = Animation(ProbedAnimation(base: a, probe: probe, travel: travel)) }
    withAnimation(a, body)
}
