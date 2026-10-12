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
            "rail.agents.max_slots", 5, .inferred("the brief's ~330 pt rail had room for five"), proposed: true)
        public static let overflowFont = Token(
            "rail.overflow.font", ["family": "Geist", "size": 9, "weight": "semibold", "color": "#B7ADBB"], .inferred("+N inside an 11 pt cell, EXA muted text"), proposed: true)
        public static let icon = Token(
            "rail.icon", ["size": 17, "weight": "regular", "color": "#F4ECF2"], .measured("ref a2 size; EXA --foreground"))
        public static let ellipsisOuter = Token("rail.ellipsis.outer", 42, .measured("ref a2"))
        public static let edge = Token("rail.edge", "trailing", .measured("ref a2"))
        public static let edgeInset = Token("rail.edge_inset", 8, .measured("ref a2: 17 px from the screen edge"))
        public static let panelMargin = Token(
            "rail.panel.margin", 10, .inferred("room for the badge and the focus ring"), proposed: true)
        public static let badgeSize = Token(
            "rail.badge.size", ["height": 16, "pad_h": 4, "font": 10],
            .inferred("a count, not a dot (the user asked for the number of messages): plan 2.2's 16 pt badge with a 10 pt semibold count"),
            proposed: true)
        public static let badgeRing = Token(
            "rail.badge.ring", ["width": 2, "color": "#12071F"], .measured("ref a2 ring; EXA ink-deep"))
        public static let badgeCenter = Token(
            "rail.badge.center", [37, 3], .measured("ref a2: on the inbox's top-right rim"))
        /// The tab left at the screen edge at rest: flush with the edge, its
        /// ends flaring into it (a concave `flare` arc, then a convex `corner`).
        public static let peek = Token(
            "rail.peek", ["width": 13, "height": 80, "flare": 5, "corner": 8, "dot": 8],
            .measured("user's reference pill, 2026-10-11 (1.8 px per point, ±1)"))
        public static let peekFill = Token(
            "rail.peek.fill", "#090909", .measured("user's reference pill, 2026-10-11; ref b2"))
        public static let drag = Token(
            "rail.drag", ["min_distance": 3, "nudge": 24],
            .inferred("past a click's jitter before a drag claims the pointer; ⌥↑/⌥↓ moves one control's height"),
            proposed: true)
        public static let hover = Token(
            "rail.hover", ["open_ms": 80, "close_ms": 370, "item_open_ms": 117, "panel_close_ms": 317],
            .measured(
                "reference recording: item_open_ms (resting on a control opens it) frames 655-662; panel_close_ms (a hover-opened panel closes after the pointer leaves) 707-726; close_ms (the rail hides) 959-981. open_ms is ours: the reveal starts with no measurable delay (149-151), and 80 ms keeps a passing pointer from revealing it"))
        public static let hoverFill = Token(
            "rail.hover.fill", "rgba(255,255,255,0.20)",
            .measured("reference recording, frames 655 and 734: a control's fill 43,37,29 -> 85,78,75, a 20% white overlay, stepped in one frame"))
        public static let pressed = Token(
            "rail.pressed", ["opacity": 0.71],
            .measured("reference recording, frames 219-225, 355-361, 452-456: the whole control at 0.71 while pressed"))
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

    /// The surface every panel beside the rail shares: card, lists, chat, settings.
    public enum Settings {
        /// The Execution Associates lockup in Settings' footer, sized by height.
        public static let footer = Token(
            "settings.footer", ["logo_height": 18, "pad_v": 14],
            .inferred("isb sizes the lockup by height (h-7, 28 px, in its header); a footer takes it smaller"),
            proposed: true)
    }

    /// The New Agent composer.
    public enum NewAgent {
        public static let layout = Token(
            "new_agent.layout",
            ["size": [560, 412], "pad": 20, "gap": 16, "radius": 14, "prompt_h": 168, "prompt_inset": 14,
             "chip_h": 28, "chevron": 8, "switch_h": 24, "switch_w": 30, "switch_track": 18, "button_h": 32,
             "hover": 0.35, "lit": 0.55, "tint": 0.1, "disabled": 0.4,
             "menu_gap": 6, "menu_radius": 12, "menu_shadow": 18, "menu_shadow_y": 8, "menu_shadow_a": 0.45, "menu_min": 160],
            .inferred("a composer: the prompt is the page, settings are chips beneath it, one primary action"),
            proposed: true)
    }

    /// The first-run panel (Onboarding/).
    public enum Onboarding {
        public static let layout = Token(
            "onboarding.layout",
            ["size": [460, 560], "pad": 28, "icon": 64, "badge": 22, "badge_glyph": 11, "glow": 0.3, "glow_radius": 56,
             "title_gap": 14, "section_gap": 22, "field_h": 40, "button_h": 34, "radius": 12, "row_icon": 30,
             "row_icon_radius": 8, "key_h": 22, "lit": 0.55, "tint": 0.12, "footer_pad": 16],
            .inferred("the first-run panel: the app's mark over a soft glow, one heading, then the step (an address with examples, or tips) in Settings' grouped cards, one primary action"),
            proposed: true)
    }

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
            "control.composer",
            ["height": 32, "fill": "#2D1D3C", "gap": 8, "send_disabled": "#210E33", "field_pad_l": 14, "field_pad_r": 12, "mic_w": 20], .measured("ref a2 size; EXA --secondary, --card"))
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
            "card.tool_row", ["height": 24, "radius": 6, "fill": "rgba(253,246,250,0.05)", "dot": 6, "pad_h": 8], .unmeasured("one-line tool card, EXA white 5%"), proposed: true)
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
        public static let drag = Token(
            "rail.drag", ["min_distance": 3, "nudge": 24],
            .inferred("past a click's jitter before a drag claims the pointer; ⌥↑/⌥↓ moves one control's height"),
            proposed: true)
        public static let hover = Token(
            "row.hover", "rgba(253,246,250,0.06)", .unmeasured("EXA white 6%"))
        public static let selected = Token(
            "row.selected", "rgba(253,246,250,0.10)", .inferred("ref a3 contrast, EXA white"))
        public static let radius = Token("row.radius", 8, .measured("brief A.2"))
    }

    public enum Fonts {
        public static let display = Token(
            "font.display", ["family": "Archivo", "size": 22, "weight": "semibold", "width": 112],
            .inferred("font.title's face (Archivo at isb .font-display font-stretch 112%), larger, for the first-run panel's one heading"),
            proposed: true)
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
            "list.row", ["height": 39, "pad_h": 22, "dot": 5, "avatar": 18, "name_x": 67, "dot_col": 38, "action_w": 30],
            .measured("ref a3: 78 px pitch"))
    }

    /// An agent's own window (a click on its row) and its hover preview,
    /// measured from the user's screenshots (2026-10-11, 2 px per point).
    public enum Agent {
        public static let window = Token(
            "agent.window",
            ["size": [488, 632], "min": [360, 320], "header_h": 60, "radius": 18, "avatar": 26, "button": 28, "icon": 15],
            .measured("user screenshot of the detached window: 978x1268 px; header to 120 px; buttons 31 pt apart"))
        public static let collapsed = Token(
            "agent.window.collapsed", ["size": [416, 60]],
            .measured("user screenshot of the minimized window: the header alone, 832x120 px"))
        /// The New Agent form's filterable picker (repositories, branches).
        public static let picker = Token(
            "agent.picker", ["size": [300, 340]], .inferred("a filter field and about a dozen rows"), proposed: true)
        public static let media = Token(
            "agent.media", ["image_max_h": 320, "video_h": 240, "radius": 12],
            .inferred("an image or video the width of the conversation, never taller than about half the window"),
            proposed: true)
        public static let preview = Token(
            "agent.preview", ["size": [508, 580], "gap": 9],
            .measured("user screenshot of the hover preview: 1015x1158 px, 18 px left of the agents list"))
    }

    public enum Chat {
        public static let bubble = Token(
            "chat.bubble", ["fill": "#2D1D3C", "radius": 18, "pad_h": 19, "pad_v": 9, "table_cell": 220, "dot": 6, "dot_lag": 0.9, "pending": 0.55], .measured("ref a5 shape; EXA --secondary"))
    }


    /// The spacing scale every gap and inset is drawn from.
    public static let spaceScale = Token(
        "space",
        ["hair": 1, "2xs": 2, "xs": 4, "s": 6, "m": 8, "l": 10, "xl": 12, "2xl": 14, "3xl": 16, "4xl": 18, "5xl": 22],
        .unmeasured("spacing scale; steps the references use (ref a2/a3/a5)"), proposed: true)

    public static func space(_ step: String) -> CGFloat { spaceScale.number(step) }

    public enum Layout {
        /// The card's vertical rhythm and its paging column (ref a2, 2 px per point).
        public static let card = Token(
            "layout.card",
            ["top": 12.5, "identity_gap": 9, "box_gap": 16, "composer_gap": 22, "divider_gap": 12, "footer_gap": 14,
             "bottom": 14, "column_x": 10, "column_gap": 5, "unread_top": 22, "avatar": 16],
            .measured("ref a2: title 25 px from the top, box at 175 px, composer at 924 px, divider at 1015 px"))
        public static let chat = Token(
            "layout.chat",
            ["top": 10, "text_x": 22, "message_gap": 14, "bubble_inset": 60, "close_bar": [16, 2.5], "close_hit": [28, 24]],
            .measured("ref a5: text at 52 px, bubbles 28 px apart; bubble inset proposed"))
    }

    /// An agent avatar's proportions, as fractions of its size.
    public static let avatar = Token(
        "avatar", ["glyph": 0.62, "radius": 0.28, "dot": 0.42, "dot_offset": 0.12, "ring": 1.5],
        .unmeasured("symbol avatar with a state dot on its corner"), proposed: true)

    public static let icon = Token(
        "icon", ["round_ratio": 0.42, "control": 14, "step": 11, "pin": 10, "menubar": 16, "menubar_text": 13],
        .measured("ref a2: control glyphs about 28 px; the rest proposed"))

    public static let focusRing = Token(
            "focus.ring", ["width": 2, "color": "#FEBE74", "outset": 2], .measured("EXA: isb web/src/index.css :root.ea --ring (peach)"), proposed: true)

    public enum Motion {
        public static let railExpand = Token(
            "motion.rail_expand", ["kind": "spring", "response": 0.2, "damping": 0.9], .measured("brief A.4"))
        public static let railCollapse = Token(
            "motion.rail_collapse", ["kind": "spring", "response": 0.2, "damping": 0.9],
            .measured("brief A.4; its ~133 ms disagrees, PLAN 2.3-2"))
        /// A panel beside the rail fades in while sliding `slide` pt away from it.
        public static let cardOpen = Token(
            "motion.card_open", ["kind": "easeOut", "duration": 0.117, "slide": 4.5],
            .measured("reference recording, frames 662-669: opacity .16 .44 .66 .80 .87 .94 .97 .98, ease-out, 4.5 pt away from the rail"))
        public static let cardClose = Token(
            "motion.card_close", ["kind": "easeOut", "duration": 0.067],
            .measured("reference recording, frames 726-730: 67 ms, 80% in the first frame"))
        public static let toastIn = Token(
            "motion.toast_in", ["kind": "timingCurve", "c": [0, 0, 0.58, 1], "duration": 0.183],
            .inferred("brief A.4: ~183 ms, decelerating, no overshoot"))
        public static let cardPaging = Token(
            "motion.card_paging", ["kind": "easeInOut", "duration": 0.2], .unmeasured("brief A.4 provisional"))
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
        public static let arrivalSound = Token(
            "live.arrival_sound", ["gap_ms": 2000, "max_bytes": 1_048_576],
            .inferred("arrivals this close make one sound, not a burst; an upload is capped where lasso caps it (src/desktopsound.go)"),
            proposed: true)
    }

    public static let summonKey = Token(
        "keys.summon", ["key": "l", "modifiers": ["option", "command"]], .unmeasured("global summon"), proposed: true)
    /// The inbox shortcut's double tap: two taps of the modifier alone, each
    /// shorter than tap_ms, the second within gap_ms of the first.
    public static let inboxDoubleTap = Token(
        "keys.inbox_double_tap", ["tap_ms": 300, "gap_ms": 400],
        .inferred("a deliberate double tap, not a held modifier or a chord"), proposed: true)
    /// Keycaps stay hidden (the references show none) until this is held.
    public static let revealKeys = Token(
        "keys.reveal", "command", .unmeasured("hold to show keycaps"), proposed: true)

    /// Every token, for the parity harness's static check. A token missing
    /// here, or present here and not in tokens.json, fails that check.
    public static let all: [Token] = [
        Rail.fillWidth, Rail.rimWidth, Rail.outer, Rail.radius, Rail.gap, Rail.backing, Rail.fillTop, Rail.fillBottom,
        Rail.rimTop, Rail.rimBottom, Rail.padTop, Rail.padBottom, Rail.glyphSize, Rail.glyphPitch, Rail.ringStroke,
        Rail.maxSlots, Rail.overflowFont, Rail.icon, Rail.ellipsisOuter, Rail.edge, Rail.edgeInset,
        Rail.panelMargin, Rail.badgeSize, Rail.badgeRing, Rail.badgeCenter, Rail.peek, Rail.peekFill, Rail.drag, Rail.hover, Rail.hoverFill, Rail.pressed,
        State.done, State.working, State.blocked, State.idle,
        Settings.footer, Agent.window, Agent.collapsed, Agent.preview, Agent.media, Agent.picker, NewAgent.layout,
        Onboarding.layout,
        Panel.surface, Panel.border, Panel.radius, Panel.inset, Panel.inner, Panel.divider, Panel.shadow,
        Control.round, Control.composer, Control.pill, Control.pillPrimary,
        Card.size, Card.titleX, Card.contentX, Card.unreadSegment, Card.unreadDot, Card.unreadGap, Card.keycapFill,
        Card.keycapFont, Card.keycapMinSize, Card.keycapRadius, Card.keycapPadH, Card.toolRow, Card.machineChip,
        Card.placement,
        Text.primary, Text.secondary, Text.placeholder, accent, dangerText,
        Row.hover, Row.selected, Row.radius,
        Fonts.display, Fonts.title, Fonts.body, Fonts.chat, Fonts.name, Fonts.label, Fonts.header, Fonts.small, Fonts.terminal,
        List.size, List.row, Chat.bubble, focusRing,
        spaceScale, Layout.card, Layout.chat,
        avatar, icon,
        Motion.railExpand, Motion.railCollapse, Motion.cardOpen, Motion.cardClose, Motion.toastIn, Motion.cardPaging,
        Live.pollAllPanes, Live.pollBots, Live.pollChat, Live.sseWatchdog, Live.backoff, Live.arrivalSound,
        summonKey, inboxDoubleTap, revealKeys,
    ]
}

// MARK: - Typed views of token values

extension Token {
    /// A font token: {family?, size, weight, width?, design?}. A family is
    /// one of the bundled faces (BundledFonts), set through its variable
    /// axes: weight on wght, width (percent) on wdth. Without one, or if the
    /// face is not registered, the system font.
    public var font: Font { Font(nsFont as CTFont) }

    public var nsFont: NSFont { nsFont(weight: nil) }

    /// The same face at another weight on its wght axis (markdown's bold:
    /// a variable face ignores SwiftUI's bold trait).
    public func font(weight: Double) -> Font { Font(nsFont(weight: weight) as CTFont) }

    func nsFont(weight: Double?) -> NSFont {
        let size = CGFloat(value["size"]?.double ?? 12)
        if let family = value["family"]?.string {
            var axes: [NSNumber: NSNumber] = [Self.wghtAxis: NSNumber(value: weight ?? weightNumber)]
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

extension Token {
    /// A two-number array inside an object token ({key: [w, h]}).
    public func size2(_ key: String) -> CGSize {
        let a = value[key]?.array ?? []
        return CGSize(width: a.first?.double ?? .nan, height: a.dropFirst().first?.double ?? .nan)
    }
}
