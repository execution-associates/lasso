import AppKit
import SwiftUI

// Every colour, metric, font and duration the app draws with lives here, and
// nowhere else. Each carries its provenance, and the values are spelled the way
// mac/Spec/tokens.json spells them: the parity harness compares the two
// exactly (Theme.all against the spec) before it measures any pixel.
//
// "ref a2/a3/a5" are the reference screenshots (Retina, 2 px per point),
// measured by pixel scans; "ref b2" is the collapsed pill at 1 px per point.
// Neutrals are taken as sampled. Saturated hues stay the brief's: the
// references carry the display's own colour profile, which moves saturated
// colours and leaves greys alone.

public enum Provenance: Sendable, Equatable {
    /// Taken from a reference; the note names it.
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
    /// A token the brief lacks.
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

    /// A colour inside an object token ({..., fill: "#…"}).
    public func color(_ key: String) -> Color { RGBA(parsing: value[key]?.string ?? "")?.color ?? .clear }
    public func number(_ key: String) -> CGFloat { CGFloat(value[key]?.double ?? .nan) }
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
        public static let fillWidth = Token("rail.group.fill_width", 40, .measured("ref a2: 80 px"))
        public static let rimWidth = Token("rail.group.rim_width", 1, .measured("brief A.1; ref a2"))
        public static let outer = Token("rail.group.outer", 42, .inferred("fill 40 + 2 x rim 1"), proposed: true)
        public static let radius = Token("rail.group.radius", "width/2", .measured("brief A.1"))
        public static let gap = Token("rail.group.gap", 8, .measured("ref a2: 15-16 px between groups"))
        public static let backing = Token("rail.backing", "none", .measured("brief A.1"))
        public static let fillTop = Token(
            "rail.capsule.fill_top", "#2E3032", .measured("ref a2: agents capsule just under its top rim"))
        public static let fillBottom = Token(
            "rail.capsule.fill_bottom", "#0C0E10", .measured("ref a2: agents capsule above its bottom rim"))
        public static let rimTop = Token("rail.capsule.rim_top", "#8F8F8F", .measured("ref a2: tools capsule top edge"))
        public static let rimBottom = Token(
            "rail.capsule.rim_bottom", "#1F2021", .measured("ref a2: agents capsule bottom edge"))
        public static let padTop = Token("rail.capsule.pad_top", 8.5, .measured("brief A.1; ref a2: 82 pt for 4 slots"))
        public static let padBottom = Token("rail.capsule.pad_bottom", 8.5, .measured("ref a2: 82 pt for 4 slots"))
        public static let glyphSize = Token("rail.glyph.size", 11, .measured("ref a2: 22 px"))
        public static let glyphPitch = Token("rail.glyph.pitch", 18, .measured("ref a2: 36 px"))
        public static let ringStroke = Token("rail.glyph.ring_stroke", 2, .measured("brief A.1"))
        public static let maxSlots = Token(
            "rail.agents.max_slots", 5, .inferred("332 pt rail matches the brief's ~330 pt travel"), proposed: true)
        public static let overflowFont = Token(
            "rail.overflow.font", ["size": 9, "weight": "semibold", "color": "#949290"],
            .inferred("+N inside an 11 pt cell"), proposed: true)
        public static let toolsCell = Token("rail.tools.cell", 40, .measured("ref a2: three 80 px cells"))
        public static let icon = Token(
            "rail.icon", ["size": 17, "weight": "regular", "color": "#FFFFFF"],
            .measured("ref a2: white glyphs, about 34 px tall"))
        public static let ellipsisOuter = Token("rail.ellipsis.outer", 42, .measured("ref a2"))
        public static let edge = Token("rail.edge", "trailing", .measured("ref a2"))
        public static let edgeInset = Token("rail.edge_inset", 8, .measured("ref a2: 17 px from the screen edge"))
        public static let panelMargin = Token(
            "rail.panel.margin", 10, .inferred("room for the badge and the focus ring"), proposed: true)
        public static let badgeSize = Token("rail.badge.size", 10, .measured("ref a2: 20 px dot"))
        public static let badgeRing = Token(
            "rail.badge.ring", ["width": 2, "color": "#0A0B0C"], .measured("ref a2: dark ring around the dot"))
        public static let badgeCenter = Token(
            "rail.badge.center", [37, 3], .measured("ref a2: on the inbox's top-right rim"))
        public static let peek = Token(
            "rail.peek", ["visible": 12, "height": 60, "dot": 6, "dot_top": 16],
            .measured("ref b2 (1 px per point, ±2): the pill left showing at the screen edge"))
        public static let hover = Token(
            "rail.hover", ["open_ms": 80, "close_ms": 450], .unmeasured("hover reveal timing"), proposed: true)
    }

    public enum State {
        public static let done = Token("state.done", "#57C459", .measured("brief A.1 at-rest, A.5"))
        public static let working = Token("state.working", "#3877F4", .measured("brief A.1, A.5"))
        public static let blocked = Token("state.blocked", "#AB362F", .measured("brief A.1 needs-you, A.5"))
        public static let idle = Token("state.idle", "#98989D", .measured("ref a2: idle dot (a grey)"))
    }

    /// The surface every panel beside the rail shares: card, list, chat, setup.
    public enum Panel {
        public static let surface = Token("panel.surface", "#040306", .measured("ref a2: card body"))
        public static let border = Token(
            "panel.border", "rgba(255,255,255,0.05)", .inferred("ref a2: the edge reads #0D0C0F on #040306"))
        public static let radius = Token("panel.radius", 14, .measured("brief A.2; ref a2"))
        public static let inset = Token("panel.inset", 16, .measured("ref a2: 33 px right inset"))
        public static let inner = Token(
            "panel.inner", ["fill": "#1B191C", "radius": 12, "pad": 15], .measured("ref a2: the conversation box"))
        public static let divider = Token("panel.divider", "#161518", .measured("ref a2"))
        public static let shadow = Token(
            "panel.shadow", ["radius": 24, "opacity": 0.45], .unmeasured("soft drop shadow"), proposed: true)
    }

    public enum Control {
        /// Round icon buttons: close, paging chevrons.
        public static let round = Token(
            "control.round", ["size": 28, "fill": "#1D1C1E", "disabled": "#0C0B0E"],
            .measured("ref a2: 57 px circles"))
        /// The composer row: camera, field, send.
        public static let composer = Token(
            "control.composer", ["height": 32, "fill": "#1D1C1E", "gap": 8, "send_disabled": "#0B090C"],
            .measured("ref a2: 66 px row"))
        public static let pill = Token(
            "control.pill", ["height": 22, "pad_h": 12, "fill": "#1B1A1D"], .measured("ref a2: Kill agent, Retry"))
        public static let pillPrimary = Token(
            "control.pill.primary", ["fill": "#FFFFFF", "text": "#000000"], .measured("ref a2: Discard"))
    }

    public enum Card {
        public static let size = Token("card.size", [516, 587], .measured("ref a2: 1032 x 1174 px"))
        public static let titleX = Token("card.title_x", 53, .measured("ref a2: title at 107 px"))
        public static let contentX = Token("card.content_x", 51, .measured("ref a2: box at 102 px"))
        public static let unreadSegment = Token(
            "card.unread_segment", ["w": 6, "h": 18, "color": "#3877F4"], .measured("ref a2: 13 x 38 px pill"))
        public static let unreadDot = Token(
            "card.unread_dot", ["size": 6, "color": "#9B9A9B"], .measured("ref a2: grey dots"))
        public static let unreadGap = Token("card.unread_gap", 7, .measured("ref a2: 14 px"))
        public static let keycapFill = Token("card.keycap.fill", "#333130", .measured("brief A.2"))
        public static let keycapFont = Token(
            "card.keycap.font", ["size": 10, "weight": "semibold"], .measured("brief A.2"))
        public static let keycapMinSize = Token("card.keycap.min_size", [18, 18], .unmeasured("keycap"), proposed: true)
        public static let keycapRadius = Token("card.keycap.radius", 4, .unmeasured("keycap"), proposed: true)
        public static let keycapPadH = Token("card.keycap.pad_h", 4, .unmeasured("keycap"), proposed: true)
        public static let toolRow = Token(
            "card.tool_row", ["height": 24, "radius": 6, "fill": "rgba(255,255,255,0.05)"],
            .unmeasured("one-line tool card"), proposed: true)
        public static let machineChip = Token(
            "card.machine_chip", ["radius": 4, "fill": "#1D1C1E", "font_size": 11, "pad_h": 6, "pad_v": 2],
            .unmeasured("remote machine chip"), proposed: true)
        public static let placement = Token(
            "card.placement", ["side": "leading-of-rail", "gap": 8, "align": "top-of-rail"],
            .measured("ref a2: 8 pt from the rail"))
    }

    public enum Text {
        public static let primary = Token("text.primary", "#F1EEED", .measured("brief A.2; ref a2 #EDEDED"))
        public static let secondary = Token("text.secondary", "#949290", .measured("brief A.2; ref a2 #969697"))
        public static let placeholder = Token("text.placeholder", "#7B7B7C", .measured("ref a2: composer placeholder"))
    }

    public static let accent = Token("accent", "#3877F4", .measured("brief A.2"))
    public static let dangerText = Token("danger.text", "#EB6E62", .measured("brief A.2; ref a2 Kill agent"))

    public enum Row {
        public static let hover = Token("row.hover", "rgba(255,255,255,0.06)", .unmeasured("brief A.2 provisional"))
        public static let selected = Token(
            "row.selected", "rgba(255,255,255,0.08)", .measured("ref a3: the selected row, #141316 on #040306"))
        public static let radius = Token("row.radius", 8, .measured("brief A.2"))
    }

    public enum Fonts {
        public static let title = Token("font.title", ["size": 16, "weight": "semibold"], .measured("ref a2: 30 px line"))
        public static let body = Token("font.body", ["size": 14, "weight": "regular"], .measured("ref a2: 34 px leading"))
        public static let chat = Token("font.chat", ["size": 15, "weight": "regular"], .measured("ref a5: 37 px leading"))
        public static let name = Token("font.name", ["size": 15, "weight": "regular"], .measured("ref a3: row names"))
        public static let label = Token("font.label", ["size": 14, "weight": "semibold"], .measured("ref a2: Next steps"))
        public static let header = Token("font.header", ["size": 13, "weight": "semibold"], .measured("ref a3: group headers"))
        public static let small = Token("font.small", ["size": 12, "weight": "regular"], .measured("ref a5: Talking to …"))
        public static let terminal = Token(
            "font.terminal", ["size": 12, "weight": "regular", "design": "monospaced"], .measured("brief A.2"))
    }

    public enum List {
        public static let size = Token("list.size", [398, 560], .measured("ref a3: 797 px wide; height proposed"))
        public static let row = Token(
            "list.row", ["height": 39, "pad_h": 22, "dot": 5, "avatar": 18, "name_x": 67],
            .measured("ref a3: 78 px pitch"))
    }

    public enum Chat {
        public static let bubble = Token(
            "chat.bubble", ["fill": "#1D1C1E", "radius": 18, "pad_h": 19, "pad_v": 9], .measured("ref a5: your message"))
    }

    public static let focusRing = Token(
        "focus.ring", ["width": 2, "color": "#3877F4", "outset": 2], .unmeasured("keyboard focus"), proposed: true)

    public enum Recording {
        public static let ring = Token("recording.ring", "#EB6E62", .unmeasured("recording, PLAN Q4"), proposed: true)
        public static let meterFill = Token(
            "recording.meter.fill", "rgba(241,238,237,0.60)", .unmeasured("level bars"), proposed: true)
        public static let meterHz = Token("recording.meter.hz", 30, .unmeasured("meter refresh"), proposed: true)
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
        public static let vadFloorWindow = Token("voice.vad.floor_window", 300, .unmeasured("VAD"), proposed: true)
        public static let vadThreshold = Token("voice.vad.threshold_over_floor", 12, .unmeasured("VAD"), proposed: true)
        public static let vadEndSilence = Token("voice.vad.end_silence", 900, .unmeasured("VAD"), proposed: true)
        public static let vadMinSpeech = Token("voice.vad.min_speech", 400, .unmeasured("VAD"), proposed: true)
        public static let maxTurn = Token(
            "voice.max_turn", 120, .inferred("matches /api/transcribe's limit"), proposed: true)
        public static let pttSplit = Token("voice.ptt.tap_hold_split", 250, .unmeasured("push-to-talk"), proposed: true)
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
            "live.reconnect.backoff", ["min": 1, "max": 30, "jitter": 0.2], .unmeasured("reconnects"), proposed: true)
    }

    public static let summonKey = Token(
        "keys.summon", ["key": "l", "modifiers": ["option", "command"]], .unmeasured("global summon"), proposed: true)
    /// Keycaps stay hidden (the references show none) until this is held.
    public static let revealKeys = Token(
        "keys.reveal", "command", .unmeasured("hold to show keycaps"), proposed: true)

    /// Every token, for the parity harness's static check. A token missing
    /// here, or present here and not in tokens.json, fails that check.
    public static let all: [Token] = [
        Rail.fillWidth, Rail.rimWidth, Rail.outer, Rail.radius, Rail.gap, Rail.backing, Rail.fillTop, Rail.fillBottom,
        Rail.rimTop, Rail.rimBottom, Rail.padTop, Rail.padBottom, Rail.glyphSize, Rail.glyphPitch, Rail.ringStroke,
        Rail.maxSlots, Rail.overflowFont, Rail.toolsCell, Rail.icon, Rail.ellipsisOuter, Rail.edge, Rail.edgeInset,
        Rail.panelMargin, Rail.badgeSize, Rail.badgeRing, Rail.badgeCenter, Rail.peek, Rail.hover,
        State.done, State.working, State.blocked, State.idle,
        Panel.surface, Panel.border, Panel.radius, Panel.inset, Panel.inner, Panel.divider, Panel.shadow,
        Control.round, Control.composer, Control.pill, Control.pillPrimary,
        Card.size, Card.titleX, Card.contentX, Card.unreadSegment, Card.unreadDot, Card.unreadGap, Card.keycapFill,
        Card.keycapFont, Card.keycapMinSize, Card.keycapRadius, Card.keycapPadH, Card.toolRow, Card.machineChip,
        Card.placement,
        Text.primary, Text.secondary, Text.placeholder, accent, dangerText,
        Row.hover, Row.selected, Row.radius,
        Fonts.title, Fonts.body, Fonts.chat, Fonts.name, Fonts.label, Fonts.header, Fonts.small, Fonts.terminal,
        List.size, List.row, Chat.bubble, focusRing,
        Recording.ring, Recording.meterFill, Recording.meterHz,
        Motion.railExpand, Motion.railCollapse, Motion.cardOpen, Motion.cardClose, Motion.toastIn, Motion.cardPaging,
        Voice.sampleRate, Voice.vadFloorWindow, Voice.vadThreshold, Voice.vadEndSilence, Voice.vadMinSpeech,
        Voice.maxTurn, Voice.pttSplit,
        Live.pollAllPanes, Live.pollBots, Live.pollChat, Live.sseWatchdog, Live.backoff,
        summonKey, revealKeys,
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

    public var nsFont: NSFont {
        let size = CGFloat(value["size"]?.double ?? 12)
        return value["design"]?.string == "monospaced"
            ? .monospacedSystemFont(ofSize: size, weight: nsWeight) : .systemFont(ofSize: size, weight: nsWeight)
    }

    public var weight: Font.Weight {
        switch value["weight"]?.string {
        case "medium": .medium
        case "semibold": .semibold
        case "bold": .bold
        default: .regular
        }
    }

    var nsWeight: NSFont.Weight {
        switch value["weight"]?.string {
        case "medium": .medium
        case "semibold": .semibold
        case "bold": .bold
        default: .regular
        }
    }

    /// The colour inside a font token ({..., color}).
    public var textColor: Color { RGBA(parsing: value["color"]?.string ?? "")?.color ?? Theme.Text.primary.color }

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
