import AppKit
import SwiftUI

// Every colour, metric, font and duration the app draws with lives here, and
// nowhere else. Each carries its provenance, and the values are spelled the way
// mac/Spec/tokens.json spells them: the parity harness compares the two
// exactly (Theme.all against the spec) before it measures any pixel.
//
// Shapes and sizes: "ref a2/a3/a5" are the reference screenshots (Retina,
// 2 px per point), measured by pixel scans; "ref b2" is the collapsed pill at
// 1 px per point. Colours: the Execution Associates palette ("EXA"), as isb's
// web app applies it (isb web/src/index.css :root.ea, oklch converted to
// sRGB): ink surfaces, white text, glow pink as the brand colour, peach for
// focus, and status colours chosen to stay clear of the pink.

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
            "rail.capsule.fill_top", "#352545", .inferred("EXA ink lifted to oklch(0.30 0.06 306), lit from above"))
        public static let fillBottom = Token(
            "rail.capsule.fill_bottom", "#12071F", .measured("EXA: isb web/src/index.css :root.ea --sidebar (ink-deep)"))
        public static let rimTop = Token(
            "rail.capsule.rim_top", "#947D99", .inferred("EXA glow-tinted light, oklch(0.62 0.05 320)"))
        public static let rimBottom = Token(
            "rail.capsule.rim_bottom", "#2A1D37", .inferred("EXA ink, oklch(0.26 0.05 306)"))
        public static let padTop = Token("rail.capsule.pad_top", 8.5, .measured("brief A.1; ref a2: 82 pt for 4 slots"))
        public static let padBottom = Token("rail.capsule.pad_bottom", 8.5, .measured("ref a2: 82 pt for 4 slots"))
        public static let glyphSize = Token("rail.glyph.size", 11, .measured("ref a2: 22 px"))
        public static let glyphPitch = Token("rail.glyph.pitch", 18, .measured("ref a2: 36 px"))
        public static let ringStroke = Token("rail.glyph.ring_stroke", 2, .measured("brief A.1"))
        public static let maxSlots = Token(
            "rail.agents.max_slots", 5, .inferred("332 pt rail matches the brief's ~330 pt travel"), proposed: true)
        public static let overflowFont = Token(
            "rail.overflow.font", ["family": "Geist", "size": 9, "weight": "semibold", "color": "#B7ADBB"], .inferred("+N inside an 11 pt cell, EXA muted text"), proposed: true)
        public static let toolsCell = Token("rail.tools.cell", 40, .measured("ref a2: three 80 px cells"))
        public static let icon = Token(
            "rail.icon", ["size": 17, "weight": "regular", "color": "#F4ECF2"], .measured("ref a2 size; EXA --foreground"))
        public static let ellipsisOuter = Token("rail.ellipsis.outer", 42, .measured("ref a2"))
        public static let edge = Token("rail.edge", "trailing", .measured("ref a2"))
        public static let edgeInset = Token("rail.edge_inset", 8, .measured("ref a2: 17 px from the screen edge"))
        public static let panelMargin = Token(
            "rail.panel.margin", 10, .inferred("room for the badge and the focus ring"), proposed: true)
        public static let badgeSize = Token("rail.badge.size", 10, .measured("ref a2: 20 px dot"))
        public static let badgeRing = Token(
            "rail.badge.ring", ["width": 2, "color": "#12071F"], .measured("ref a2 ring; EXA ink-deep"))
        public static let badgeCenter = Token(
            "rail.badge.center", [37, 3], .measured("ref a2: on the inbox's top-right rim"))
        public static let peek = Token(
            "rail.peek", ["visible": 12, "height": 60, "dot": 6, "dot_top": 16],
            .measured("ref b2 (1 px per point, ±2): the pill left showing at the screen edge"))
        public static let hover = Token(
            "rail.hover", ["open_ms": 80, "close_ms": 450], .unmeasured("hover reveal timing"), proposed: true)
    }

    public enum State {
        public static let done = Token(
            "state.done", "#5ED99E", .measured("EXA: isb web/src/index.css :root.ea --success"))
        public static let working = Token(
            "state.working", "#80B3FD", .measured("EXA: isb web/src/index.css :root.ea --info"))
        public static let blocked = Token(
            "state.blocked", "#FA686A", .measured("EXA: isb web/src/index.css :root.ea --destructive"))
        public static let idle = Token(
            "state.idle", "#796C82", .inferred("EXA muted text at 60% on ink"))
    }

    /// The surface every panel beside the rail shares: card, list, chat, setup.
    public enum Panel {
        public static let surface = Token(
            "panel.surface", "#1B0A2C", .measured("EXA: isb web/src/index.css :root.ea --background (ink)"))
        public static let border = Token(
            "panel.border", "rgba(253,246,250,0.14)", .measured("EXA: isb web/src/index.css :root.ea --border (white 14%)"))
        public static let radius = Token("panel.radius", 14, .measured("brief A.2; ref a2"))
        public static let inset = Token("panel.inset", 16, .measured("ref a2: 33 px right inset"))
        public static let inner = Token(
            "panel.inner", ["fill": "#291838", "radius": 12, "pad": 15], .measured("ref a2 shape; EXA --muted"))
        public static let divider = Token(
            "panel.divider", "rgba(253,246,250,0.09)", .measured("EXA: isb web/src/index.css :root.ea --sidebar-border (white 9%)"))
        public static let shadow = Token(
            "panel.shadow", ["radius": 24, "opacity": 0.45], .unmeasured("soft drop shadow"), proposed: true)
    }

    public enum Control {
        /// Round icon buttons: close, paging chevrons.
        public static let round = Token(
            "control.round", ["size": 28, "fill": "#2D1D3C", "disabled": "#210E33"], .measured("ref a2 size; EXA --secondary, --card"))
        /// The composer row: camera, field, send.
        public static let composer = Token(
            "control.composer", ["height": 32, "fill": "#2D1D3C", "gap": 8, "send_disabled": "#210E33"], .measured("ref a2 size; EXA --secondary, --card"))
        public static let pill = Token(
            "control.pill", ["height": 22, "pad_h": 12, "fill": "#2D1D3C"], .measured("ref a2 size; EXA --secondary"))
        public static let pillPrimary = Token(
            "control.pill.primary", ["fill": "#FDF6FA", "text": "#1B0A2C"], .measured("EXA: isb web/src/index.css :root.ea --primary (the site's solid white button)"))
    }

    public enum Card {
        public static let size = Token("card.size", [516, 587], .measured("ref a2: 1032 x 1174 px"))
        public static let titleX = Token("card.title_x", 53, .measured("ref a2: title at 107 px"))
        public static let contentX = Token("card.content_x", 51, .measured("ref a2: box at 102 px"))
        public static let unreadSegment = Token(
            "card.unread_segment", ["w": 6, "h": 18, "color": "#F099BF"], .measured("ref a2 size; EXA --brand (glow)"))
        public static let unreadDot = Token(
            "card.unread_dot", ["size": 6, "color": "#B7ADBB"], .measured("ref a2 size; EXA --muted-foreground"))
        public static let unreadGap = Token("card.unread_gap", 7, .measured("ref a2: 14 px"))
        public static let keycapFill = Token(
            "card.keycap.fill", "#3D2D4E", .inferred("EXA ink, oklch(0.33 0.06 306), above the controls"))
        public static let keycapFont = Token(
            "card.keycap.font", ["family": "Geist", "size": 10, "weight": "semibold"], .measured("brief A.2"))
        public static let keycapMinSize = Token("card.keycap.min_size", [18, 18], .unmeasured("keycap"), proposed: true)
        public static let keycapRadius = Token("card.keycap.radius", 4, .unmeasured("keycap"), proposed: true)
        public static let keycapPadH = Token("card.keycap.pad_h", 4, .unmeasured("keycap"), proposed: true)
        public static let toolRow = Token(
            "card.tool_row", ["height": 24, "radius": 6, "fill": "rgba(253,246,250,0.05)"], .unmeasured("one-line tool card, EXA white 5%"), proposed: true)
        public static let machineChip = Token(
            "card.machine_chip", ["radius": 4, "fill": "#2D1D3C", "font_size": 11, "pad_h": 6, "pad_v": 2], .unmeasured("remote machine chip, EXA --secondary"), proposed: true)
        public static let placement = Token(
            "card.placement", ["side": "leading-of-rail", "gap": 8, "align": "top-of-rail"],
            .measured("ref a2: 8 pt from the rail"))
    }

    public enum Text {
        public static let primary = Token(
            "text.primary", "#F4ECF2", .measured("EXA: isb web/src/index.css :root.ea --foreground"))
        public static let secondary = Token(
            "text.secondary", "#B7ADBB", .measured("EXA: isb web/src/index.css :root.ea --muted-foreground"))
        public static let placeholder = Token(
            "text.placeholder", "#887C90", .inferred("EXA muted text at 70% on ink"))
    }

    public static let accent = Token(
            "accent", "#F099BF", .measured("EXA: isb web/src/index.css :root.ea --brand (glow pink)"))
    public static let dangerText = Token(
            "danger.text", "#FA686A", .measured("EXA: isb web/src/index.css :root.ea --destructive"))

    public enum Row {
        public static let hover = Token(
            "row.hover", "rgba(253,246,250,0.06)", .unmeasured("EXA white 6%"))
        public static let selected = Token(
            "row.selected", "rgba(253,246,250,0.10)", .inferred("ref a3 contrast, EXA white"))
        public static let radius = Token("row.radius", 8, .measured("brief A.2"))
    }

    public enum Fonts {
        public static let title = Token("font.title", ["family": "Archivo", "size": 16, "weight": "semibold", "width": 112], .measured("ref a2 size; EXA display face, Archivo at isb .font-display font-stretch 112%"))
        public static let body = Token("font.body", ["family": "Geist", "size": 14, "weight": "regular"], .measured("ref a2: 34 px leading"))
        public static let chat = Token("font.chat", ["family": "Geist", "size": 15, "weight": "regular"], .measured("ref a5: 37 px leading"))
        public static let name = Token("font.name", ["family": "Geist", "size": 15, "weight": "regular"], .measured("ref a3: row names"))
        public static let label = Token("font.label", ["family": "Geist", "size": 14, "weight": "semibold"], .measured("ref a2: Next steps"))
        public static let header = Token("font.header", ["family": "Geist", "size": 13, "weight": "semibold"], .measured("ref a3: group headers"))
        public static let small = Token("font.small", ["family": "Geist", "size": 12, "weight": "regular"], .measured("ref a5: Talking to …"))
        public static let terminal = Token(
            "font.terminal", ["family": "Geist Mono", "size": 12, "weight": "regular", "design": "monospaced"],
            .measured("brief A.2 size; EXA mono face, isb --app-font-mono"))
    }

    public enum List {
        public static let size = Token("list.size", [398, 560], .measured("ref a3: 797 px wide; height proposed"))
        public static let row = Token(
            "list.row", ["height": 39, "pad_h": 22, "dot": 5, "avatar": 18, "name_x": 67],
            .measured("ref a3: 78 px pitch"))
    }

    public enum Chat {
        public static let bubble = Token(
            "chat.bubble", ["fill": "#2D1D3C", "radius": 18, "pad_h": 19, "pad_v": 9], .measured("ref a5 shape; EXA --secondary"))
    }

    public static let focusRing = Token(
            "focus.ring", ["width": 2, "color": "#FEBE74", "outset": 2], .measured("EXA: isb web/src/index.css :root.ea --ring (peach)"), proposed: true)

    public enum Recording {
        public static let ring = Token(
            "recording.ring", "#DD519B", .measured("EXA brand brief: magenta (neon is light)"), proposed: true)
        public static let meterFill = Token(
            "recording.meter.fill", "rgba(244,236,242,0.60)", .unmeasured("level bars, EXA --foreground"), proposed: true)
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
    /// A font token: {family?, size, weight, width?, design?}. A family is
    /// one of the bundled faces (BundledFonts), set through its variable
    /// axes: weight on wght, width (percent) on wdth. Without one, or if the
    /// face is not registered, the system font.
    public var font: Font { Font(nsFont as CTFont) }

    public var nsFont: NSFont {
        let size = CGFloat(value["size"]?.double ?? 12)
        if let family = value["family"]?.string {
            var axes: [NSNumber: NSNumber] = [Self.wghtAxis: NSNumber(value: weightNumber)]
            if let w = value["width"]?.double { axes[Self.wdthAxis] = NSNumber(value: w) }
            let d = NSFontDescriptor(fontAttributes: [.family: family, .variation: axes])
            if let f = NSFont(descriptor: d, size: size), f.familyName == family { return f }
        }
        return value["design"]?.string == "monospaced"
            ? .monospacedSystemFont(ofSize: size, weight: nsWeight) : .systemFont(ofSize: size, weight: nsWeight)
    }

    /// OpenType axis tags as CoreText wants them: 'wght' and 'wdth'.
    static let wghtAxis = NSNumber(value: 0x7767_6874)
    static let wdthAxis = NSNumber(value: 0x7764_7468)

    var weightNumber: Double {
        switch value["weight"]?.string {
        case "medium": 500
        case "semibold": 600
        case "bold": 700
        default: 400
        }
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
