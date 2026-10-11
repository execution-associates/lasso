import AppKit
import ApplicationServices
import SwiftUI

/// `LassoDesktop --parity <state,...|all> --out <dir> [--spec <dir>]`: render
/// each named state from its fixture, measure it against tokens.json, write
/// the screenshots and a report, and exit non-zero on any failure. Runs the
/// shipped views; fixtures make every action a no-op.
@MainActor
public final class ParityRunner {
    /// States whose surfaces exist. The rest report as pending until their
    /// milestone lands (PLAN 6).
    public static let implemented: Set<String> = [
        "rail-collapsed", "rail-expanded", "card-open", "list-panel-open", "chat-open", "recording", "onboarding",
    ]

    let spec: Spec
    let specDir: URL
    let out: URL
    let wanted: Set<String>?
    var rows: [ParityRow] = []

    public init(specDir: URL, out: URL, states: [String]?) throws {
        self.specDir = specDir
        self.out = out
        spec = try Spec.load(specDir.appendingPathComponent("tokens.json"))
        wanted = states.map(Set.init)
    }

    public func run() async -> Int32 {
        try? FileManager.default.createDirectory(at: out, withIntermediateDirectories: true)
        staticChecks()
        let states = spec.states.filter { wanted?.contains($0) ?? true }
        for s in states {
            guard Self.implemented.contains(s) else {
                add(s, "state", "-", "rendered", "not built yet", "", .pending)
                continue
            }
            do {
                try await runState(s)
            } catch {
                add(s, "state", "-", "rendered", "\(error)", "", .fail)
            }
        }
        if states.contains(where: { $0.hasPrefix("rail-") || $0 == "card-open" }) {
            await motionChecks()
        }
        return finish()
    }

    // MARK: rows

    private func add(
        _ state: String, _ check: String, _ token: String, _ spec: String, _ measured: String, _ delta: String,
        _ result: ParityRow.Result
    ) {
        // Pane keys are host NUL pane_id; a NUL is no use in a table or a
        // JSON file other tools read.
        func clean(_ s: String) -> String { s.replacingOccurrences(of: "\u{0}", with: "/") }
        rows.append(
            ParityRow(
                state: state, check: clean(check), token: clean(token), spec: clean(spec), measured: clean(measured),
                delta: clean(delta), result: result))
    }

    private static func f(_ v: Double, _ digits: Int = 2) -> String { String(format: "%.\(digits)f", v) }

    // MARK: static: Theme.swift against tokens.json

    private func staticChecks() {
        var seen = Set<String>()
        for t in Theme.all {
            seen.insert(t.id)
            guard let s = spec.tokens[t.id] else {
                add("-", "static", t.id, "(absent)", t.value.compact, "", .fail)
                continue
            }
            var problems: [String] = []
            if s.value != t.value { problems.append("value") }
            if s.provenance != t.provenance.kind { problems.append("provenance \(t.provenance.kind)") }
            if (s.proposed ?? false) != t.proposed { problems.append("proposed") }
            add(
                "-", "static", t.id, "\(s.value.compact) [\(s.provenance)]",
                "\(t.value.compact) [\(t.provenance.kind)]", problems.joined(separator: ", "),
                problems.isEmpty ? .pass : .fail)
        }
        for id in spec.tokens.keys.sorted() where !seen.contains(id) {
            add("-", "static", id, spec.tokens[id]!.value.compact, "(not in Theme.swift)", "", .fail)
        }
    }

    // MARK: states

    private func fleet(for fixture: Fixture) -> FleetModel {
        let fleet = FleetModel()
        fleet.apply(panes: fixture.server.all_panes)
        fleet.apply(bots: fixture.server.bots)
        fleet.apply(uiState: fixture.server.ui_state)
        return fleet
    }

    private func railSize() -> CGSize {
        CGSize(
            width: RailMetrics.panelWidth,
            height: RailMetrics.expandedHeight(Theme.Rail.maxSlots.int) + 2 * RailMetrics.margin)
    }

    private func runState(_ state: String) async throws {
        let fixture = try Fixture.load(state, from: specDir.appendingPathComponent("fixtures"))
        let fleet = fleet(for: fixture)
        let model = RailModel(fleet: fleet)
        model.setExpanded(fixture.ui.rail == "expanded", animated: false)
        let cap: Capture
        var card: CardModel?
        var bindings: Set<KeyBinding>?
        var recapture: (@MainActor () async throws -> Capture)?
        if fixture.ui.onboarding == true {
            let setup = AssistantSetup(fleet: fleet, client: nil)
            let hosts = fixture.server.hosts
            let usable = hosts.hosts.filter { $0.reachable && $0.running && $0.compatible }.count + 1
            let m = OnboardingModel(
                permissions: FixedPermissions(fixture.permissions), setup: setup, client: nil,
                baseURL: "http://127.0.0.1:18090", engine: .lasso, version: { fixture.server.version },
                usableHosts: { (usable, hosts.hosts.count + 1) })
            m.show(transcribe: fixture.transcribe)
            bindings = [Keys.esc, Keys.send]
            let make: @MainActor () async throws -> Capture = { try await Capturer.capture(OnboardingView(model: m), size: Theme.Card.size.size) }
            cap = try await make()
            recapture = make
        } else if fixture.ui.chat == true, let name = fixture.ui.bot,
                  let bot = fixture.server.bots.bots.first(where: { $0.name == name })
        {
            let m = ChatModel(bot: bot, actions: FixtureBotChatActions(payload: fixture.chats["bot:\(name)"]))
            m.show(chat: fixture.chats["bot:\(name)"])
            bindings = m.activeBindings
            if let r = fixture.ui.recording {
                let rec = RecordingModel(
                    permissions: FixedPermissions(fixture.permissions), engineName: r["engine"]?.string ?? "lasso"
                ) { ParakeetTranscriber() }
                rec.showForParity(
                    phase: r["phase"]?.string == "transcribing" ? .transcribing : .recording(handsFree: true),
                    level: r["level"]?.double ?? 0.5)
                model.recording = true
                let gap = Theme.Card.placement.number("gap")
                let view = HStack(alignment: .top, spacing: gap - RailMetrics.margin) {
                    ChatView(model: m, recording: rec)
                    RailView(model: model)
                }
                let size = CGSize(
                    width: Theme.Card.size.size.width + gap - RailMetrics.margin + railSize().width,
                    height: max(Theme.Card.size.size.height, railSize().height))
                let make: @MainActor () async throws -> Capture = { try await Capturer.capture(view, size: size) }
                cap = try await make()
                recapture = make
            } else {
                let make: @MainActor () async throws -> Capture = { try await Capturer.capture(ChatView(model: m), size: Theme.Card.size.size) }
                cap = try await make()
                recapture = make
            }
        } else if fixture.ui.list == true {
            let l = ListModel(fleet: fleet, actions: FixtureFleetActions(), now: { fixture.now })
            l.selected = fixture.ui.selected
            l.hovered = fixture.ui.hovered
            let make: @MainActor () async throws -> Capture = { try await Capturer.capture(ListView(model: l), size: Theme.List.size.size) }
            cap = try await make()
            recapture = make
        } else if let key = fixture.ui.card {
            let c = try cardModel(fixture, fleet: fleet, key: key)
            card = c
            let make: @MainActor () async throws -> Capture = { try await Capturer.capture(CardView(model: c), size: Theme.Card.size.size) }
            cap = try await make()
            recapture = make
        } else {
            let make: @MainActor () async throws -> Capture = { try await Capturer.capture(RailView(model: model), size: self.railSize()) }
            cap = try await make()
            recapture = make
        }

        if let png = cap.image.representation(using: .png, properties: [:]) {
            try png.write(to: out.appendingPathComponent("\(state).png"))
        }
        add(
            state, "capture", "scale", "2.00", Self.f(Double(cap.scale)), "",
            abs(cap.scale - 2) < 0.001 ? .pass : .fail)

        fixtureChecks(state, fixture, fleet: fleet, model: model)
        patchChecks(state, cap)
        geometryChecks(state, cap)
        accessibilityChecks(state, cap)
        if let card { bindings = card.activeBindings }
        if let bindings, let again = recapture {
            // Keycaps show only while ⌘ is held: render again holding it.
            KeyHints.shared.visible = true
            let held = try await again()
            KeyHints.shared.visible = false
            if let png = held.image.representation(using: .png, properties: [:]) {
                try png.write(to: out.appendingPathComponent("\(state)-keys.png"))
            }
            keycapChecks(state, held, bindings)
            held.window.orderOut(nil)
        }
        cap.window.orderOut(nil)
    }

    /// A card on the fixture's pane, with the fixture's chat already shown
    /// and the fixture's clock.
    private func cardModel(_ fx: Fixture, fleet: FleetModel, key: String) throws -> CardModel {
        guard let pane = fleet.panes.first(where: { $0.key == key }) else {
            throw ParityError.capture("fixture card \(key) is not in its panes")
        }
        let actions = FixtureCardActions(chats: fx.chats, canDismiss: fx.server.ui_state.dismissed_cards != nil)
        let now = fx.now
        let c = CardModel(pane: pane, fleet: fleet, actions: actions, now: { now })
        c.show(chat: fx.chats[key])
        return c
    }

    /// Every keycap on screen is a binding the card's key handler has live
    /// in this state, so its legend is a key that works.
    private func keycapChecks(_ state: String, _ cap: Capture, _ bindings: Set<KeyBinding>) {
        let shown = Set(cap.frames.keys.filter { $0.hasPrefix("keycap[") }.map { String($0.dropFirst(7).dropLast()) })
        let live = Set(bindings.map(\.id))
        let stray = shown.subtracting(live)
        add(
            state, "keycaps bound", "keys", "every shown keycap bound", shown.sorted().joined(separator: " "),
            stray.isEmpty ? "" : "unbound: " + stray.sorted().joined(separator: " "), !shown.isEmpty && stray.isEmpty ? .pass : .fail)
    }

    private func fixtureChecks(_ state: String, _ fx: Fixture, fleet: FleetModel, model: RailModel) {
        if let badge = fx.expect?.badge {
            add(
                state, "fixture", "badge", "\(badge)", "\(fleet.badgeCount)", "",
                badge == fleet.badgeCount ? .pass : .fail)
        }
        if let slots = fx.expect?.rail_slots {
            let got = model.slots.map(\.id)
            add(
                state, "fixture", "rail slots", slots.joined(separator: " ").replacingOccurrences(of: "\u{0}", with: "/"),
                got.joined(separator: " ").replacingOccurrences(of: "\u{0}", with: "/"), "",
                slots == got ? .pass : .fail)
        }
    }

    private func patchChecks(_ state: String, _ cap: Capture) {
        let tol = spec.tolerances.deltaE2000
        for p in spec.patches where p.state == state {
            guard let frame = cap.frames[p.anchor], let r = p.rect, r.count == 4 else {
                add(state, "colour", p.token, p.anchor, "anchor not rendered", "", .fail)
                continue
            }
            let tv = spec.tokens[p.token]?.value
            guard var want = RGBA(parsing: tv?.string ?? tv?["color"]?.string ?? tv?["fill"]?.string ?? "") else {
                add(state, "colour", p.token, "(not a colour)", "", "", .fail)
                continue
            }
            if let g = p.gradient, g.count == 2, let at = p.at,
               let top = RGBA(parsing: spec.tokens[g[0]]?.value.string ?? ""),
               let bottom = RGBA(parsing: spec.tokens[g[1]]?.value.string ?? "")
            {
                want = RGBA(
                    r: top.r + (bottom.r - top.r) * at, g: top.g + (bottom.g - top.g) * at,
                    b: top.b + (bottom.b - top.b) * at, a: 1)
            }
            if let over = p.over, let base = RGBA(parsing: spec.tokens[over]?.value.string ?? "") {
                // Source-over in sRGB, as the window server composites.
                let a = want.a
                want = RGBA(
                    r: want.r * a + base.r * (1 - a), g: want.g * a + base.g * (1 - a), b: want.b * a + base.b * (1 - a),
                    a: 1)
            }
            let rect = CGRect(x: frame.minX + r[0], y: frame.minY + r[1], width: r[2], height: r[3])
            let got = cap.median(in: rect)
            let de = ColorMath.deltaE2000(want, got)
            let opaque = got.a >= 0.98
            add(
                state, "colour @\(p.anchor)", p.token, ColorMath.hex(want), ColorMath.hex(got),
                "ΔE " + Self.f(de) + (opaque ? "" : " (not opaque)"), de <= tol && opaque ? .pass : .fail)
        }
    }

    private func geometryChecks(_ state: String, _ cap: Capture) {
        let tol = spec.tolerances.geometry_pt
        for g in spec.geometry where g.state == state {
            let names = g.anchor.split(separator: ">").map(String.init)
            let frames = names.map { cap.frames[$0] }
            guard frames.allSatisfy({ $0 != nil }) else {
                add(state, "geometry \(g.measure)", g.token, g.anchor, "anchor not rendered", "", .fail)
                continue
            }
            let fr = frames.map { $0! }
            if g.measure == "size" {
                let v = spec.tokens[g.token]?.value
                let w = v?.array?.first?.double ?? v?["w"]?.double ?? .nan
                let h = v?.array?.dropFirst().first?.double ?? v?["h"]?.double ?? .nan
                let d = max(abs(fr[0].width - w), abs(fr[0].height - h))
                add(
                    state, "geometry size @\(g.anchor)", g.token, Self.f(w) + "×" + Self.f(h),
                    Self.f(fr[0].width) + "×" + Self.f(fr[0].height), Self.f(d), d <= tol ? .pass : .fail)
                continue
            }
            let rim = Double(RailMetrics.rim)
            let measured: Double?
            switch g.measure {
            case "width": measured = fr[0].width
            case "height": measured = fr[0].height
            case "gap": measured = fr[1].minY - fr[0].maxY
            case "pitch": measured = fr[1].midY - fr[0].midY
            case "pad_top": measured = fr[1].minY - fr[0].minY - rim
            case "pad_bottom": measured = fr[0].maxY - fr[1].maxY - rim
            case "span": measured = fr[1].maxY - fr[0].minY
            case "x": measured = fr[0].minX
            case "y": measured = fr[0].minY
            case "right_inset": measured = Double(cap.image.size.width) - fr[0].maxX
            case "pixel_width": measured = pixelWidth(cap, fr[0])
            case "pixel_fill_width": measured = pixelRims(cap, fr[0], row: g.row ?? Double(fr[0].height / 2)).fill
            case "pixel_rim_width": measured = pixelRims(cap, fr[0], row: g.row ?? Double(fr[0].height / 2)).rim
            case "pixel_diameter": measured = pixelDiameter(cap, fr[0])
            default: measured = nil
            }
            let want = g.expect ?? spec.tokens[g.token]?.value.double
            guard let m = measured, let w = want else {
                add(state, "geometry \(g.measure)", g.token, g.anchor, "unknown measure or value", "", .fail)
                continue
            }
            add(
                state, "geometry \(g.measure) @\(g.anchor)", g.token, Self.f(w), Self.f(m), Self.f(m - w),
                abs(m - w) <= tol ? .pass : .fail)
        }
    }

    /// Opaque run along the frame's middle row, in points.
    private func pixelWidth(_ cap: Capture, _ f: CGRect) -> Double {
        let px = cap.row(y: f.midY, from: f.minX - 4, to: f.maxX + 4)
        return Double(px.filter { $0.a > 0.5 }.count) / Double(cap.scale)
    }

    /// A vertical gradient's colour a fraction of the way down.
    static func mix(_ a: Token, _ b: Token, _ t: Double) -> RGBA {
        let x = a.rgba, y = b.rgba
        let u = max(0, min(1, t))
        return RGBA(r: x.r + (y.r - x.r) * u, g: x.g + (y.g - x.g) * u, b: x.b + (y.b - x.b) * u, a: 1)
    }

    /// Fill and rim widths across a capsule at a row, by nearest colour (the
    /// gradients' colours at that row).
    private func pixelRims(_ cap: Capture, _ f: CGRect, row: Double) -> (fill: Double, rim: Double) {
        let t = row / Double(f.height)
        let fill = Self.mix(Theme.Rail.fillTop, Theme.Rail.fillBottom, t)
        let rim = Self.mix(Theme.Rail.rimTop, Theme.Rail.rimBottom, t)
        let px = cap.row(y: f.minY + row, from: f.minX - 2, to: f.maxX + 2).filter { $0.a > 0.5 }
        var nFill = 0, nRim = 0
        for p in px {
            if ColorMath.deltaE2000(p, fill) <= ColorMath.deltaE2000(p, rim) { nFill += 1 } else { nRim += 1 }
        }
        let s = Double(cap.scale)
        return (Double(nFill) / s, Double(nRim) / s / 2)
    }

    /// Width of whatever differs from the capsule fill across a glyph's
    /// middle row, in points.
    private func pixelDiameter(_ cap: Capture, _ f: CGRect) -> Double {
        let fill = Self.mix(Theme.Rail.fillTop, Theme.Rail.fillBottom, 0.3)
        let px = cap.row(y: f.midY, from: f.minX - 3, to: f.maxX + 3)
        let mark = px.filter { $0.a > 0.5 && ColorMath.deltaE2000($0, fill) > 10 }
        return Double(mark.count) / Double(cap.scale)
    }

    /// Every button the state exposes has a label; and the walk found some,
    /// so an empty tree cannot pass. Read through the accessibility client
    /// API on this process, as VoiceOver would: SwiftUI builds its tree only
    /// for a client, so NSView's own accessibilityChildren shows nothing.
    /// Reading one's own process needs no Accessibility permission.
    private func accessibilityChecks(_ state: String, _ cap: Capture) {
        func attr(_ e: AXUIElement, _ a: String) -> CFTypeRef? {
            var r: CFTypeRef?
            return AXUIElementCopyAttributeValue(e, a as CFString, &r) == .success ? r : nil
        }
        var buttons = 0
        var unlabelled = 0
        var labels: [String] = []
        func walk(_ e: AXUIElement, depth: Int) {
            guard depth < 40 else { return }
            // A scroll bar's arrow and page parts are AppKit's, announced
            // through the scroll bar itself: not controls of ours.
            if attr(e, kAXRoleAttribute) as? String == kAXScrollBarRole as String { return }
            if attr(e, kAXRoleAttribute) as? String == kAXButtonRole as String {
                buttons += 1
                let label = [kAXDescriptionAttribute, kAXTitleAttribute]
                    .compactMap { attr(e, $0) as? String }.first { !$0.isEmpty } ?? ""
                if label.isEmpty {
                    unlabelled += 1
                    var where_ = ""
                    if let v = attr(e, kAXPositionAttribute), CFGetTypeID(v) == AXValueGetTypeID() {
                        var p = CGPoint.zero
                        AXValueGetValue(v as! AXValue, .cgPoint, &p)
                        where_ = " at \(Int(p.x)),\(Int(p.y))"
                    }
                    let kids = (attr(e, kAXChildrenAttribute) as? [AXUIElement])?.count ?? 0
                    labels.append("UNLABELLED(\((attr(e, kAXRoleDescriptionAttribute) as? String) ?? "?")\(where_), \(kids) children)")
                } else {
                    labels.append(label)
                }
            }
            for c in (attr(e, kAXChildrenAttribute) as? [AXUIElement]) ?? [] { walk(c, depth: depth + 1) }
        }
        for w in (attr(AXUIElementCreateApplication(getpid()), kAXWindowsAttribute) as? [AXUIElement]) ?? [] {
            walk(w, depth: 0)
        }
        add(
            state, "a11y", "buttons labelled", "all, ≥1", "\(buttons - unlabelled)/\(buttons)",
            labels.joined(separator: "; "), buttons > 0 && unlabelled == 0 ? .pass : .fail)
    }

    // MARK: motion

    private func motionChecks() async {
        let tolMs = spec.tolerances.duration_ms
        let tolPx = spec.tolerances.overshoot_px
        let samples = await probeRail()
        for m in spec.motion {
            let st = "motion"
            let ends = m.transition.split(separator: "→").map { String($0.split(separator: "[")[0]) }
            guard ends.allSatisfy(Self.implemented.contains) else {
                add(st, m.transition, m.token, spec.tokens[m.token]?.value.compact ?? "", "not built yet", "", .pending)
                continue
            }
            if spec.tokens[m.token]?.value["kind"]?.string != "spring" {
                await curveCheck(m)
                continue
            }
            guard let tok = spec.tokens[m.token]?.value, tok["kind"]?.string == "spring",
                  let resp = tok["response"]?.double, let damp = tok["damping"]?.double, let travel = m.travel_pt
            else {
                add(st, m.transition, m.token, "spring", "spec row unusable", "", .fail)
                continue
            }
            let want = SpringModel(response: resp, damping: damp)
            let wantSettle = want.settleTime(travel: travel, tolerance: 0.5) * 1000
            let wantOver = want.overshootFraction * travel * 2

            // SwiftUI's own spring for the same parameters.
            let sw = Spring(response: resp, dampingRatio: damp)
            var peak = 0.0
            var settle = 0.0
            var t = 0.0
            while t < 2 {
                let v: Double = sw.value(target: travel, time: t)
                peak = max(peak, v - travel)
                if abs(v - travel) > 0.5 { settle = t }
                t += 0.0005
            }
            add(
                st, "\(m.transition) swiftui settle≤1px", m.token, Self.f(wantSettle, 1) + " ms",
                Self.f(settle * 1000, 1) + " ms", Self.f(settle * 1000 - wantSettle, 1),
                abs(settle * 1000 - wantSettle) <= tolMs ? .pass : .fail)
            add(
                st, "\(m.transition) swiftui overshoot", m.token, Self.f(wantOver) + " px", Self.f(peak * 2) + " px",
                Self.f(peak * 2 - wantOver), abs(peak * 2 - wantOver) <= tolPx ? .pass : .fail)

            // The animation the app's transaction ran, against a reference
            // SwiftUI spring built from the spec's numbers (not Theme's), both
            // on SwiftUI's own animation clock. SwiftUI steps its spring frame
            // by frame, so its curve sits up to a few points off the closed
            // form; the reference is what the spec means when SwiftUI runs it.
            let expanding = m.transition.hasSuffix("rail-expanded")
            let app = expanding ? samples.expand : samples.collapse
            let ref = expanding ? samples.refExpand : samples.refCollapse
            for (name, set) in [("app", app), ("reference", ref)] {
                let csv = "t,v\n" + set.map { "\($0.t),\($0.v)" }.joined(separator: "\n") + "\n"
                try? csv.write(
                    to: out.appendingPathComponent("probe-\(expanding ? "expand" : "collapse")-\(name).csv"),
                    atomically: true, encoding: .utf8)
            }
            guard app.count >= 3, ref.count >= 3 else {
                add(st, "\(m.transition) animation", m.token, "≥3 samples", "app \(app.count), ref \(ref.count)", "", .fail)
                continue
            }
            let appSettle = Self.settle(app, travel: travel) * 1000
            let refSettle = Self.settle(ref, travel: travel) * 1000
            let appOver = max(0, (app.map(\.v).max() ?? 0) - travel) * 2
            let refOver = max(0, (ref.map(\.v).max() ?? 0) - travel) * 2
            add(
                st, "\(m.transition) settle≤1px vs spec spring (\(app.count) samples)", m.token,
                Self.f(refSettle, 1) + " ms", Self.f(appSettle, 1) + " ms", Self.f(appSettle - refSettle, 1),
                abs(appSettle - refSettle) <= tolMs ? .pass : .fail)
            add(
                st, "\(m.transition) overshoot vs spec spring", m.token, Self.f(refOver) + " px",
                Self.f(appOver) + " px", Self.f(appOver - refOver), abs(appOver - refOver) <= tolPx ? .pass : .fail)
            add(
                st, "\(m.transition) closed-form spring", m.token, Self.f(wantSettle, 1) + " ms, " + Self.f(wantOver) + " px",
                Self.f(appSettle, 1) + " ms, " + Self.f(appOver) + " px", "SwiftUI steps its spring", .info)

            // What was drawn: the frames end exactly where the rail rests.
            let frames = expanding ? samples.framesExpand : samples.framesCollapse
            let end = expanding ? travel : 0
            let last = frames.last?.v ?? .nan
            add(
                st, "\(m.transition) drawn frames end at rest (\(frames.count) frames)", m.token, Self.f(end) + " pt",
                Self.f(last) + " pt", Self.f(last - end), frames.count > 2 && abs(last - end) <= 0.5 ? .pass : .fail)
            let claimed = expanding ? "~200 ms" : "~133 ms"
            add(
                st, "\(m.transition) brief's duration", m.token, claimed, Self.f(appSettle, 1) + " ms (as run)",
                "PLAN 2.3-2", .info)
        }
    }

    /// Show the rail on screen (animations only tick for a visible window),
    /// expand it and collapse it, recording every interpolated reveal height.
    typealias Samples = [(t: Double, v: Double)]

    struct RailProbe {
        var expand: Samples = [], collapse: Samples = [], framesExpand: Samples = [], framesCollapse: Samples = []
        var refExpand: Samples = [], refCollapse: Samples = []
    }

    /// When a sampled motion from 0 to travel last leaves the ±0.5 pt band
    /// around the target, interpolated between samples.
    static func settle(_ s: Samples, travel: Double, tolerance: Double = 0.5) -> Double {
        guard let i = s.lastIndex(where: { abs($0.v - travel) > tolerance }) else { return 0 }
        guard i + 1 < s.count else { return s[i].t }
        let e0 = abs(s[i].v - travel), e1 = abs(s[i + 1].v - travel)
        return s[i].t + (s[i + 1].t - s[i].t) * (e0 - tolerance) / max(1e-9, e0 - e1)
    }

    private func probeRail() async -> RailProbe {
        guard let fx = try? Fixture.load("rail-expanded", from: specDir.appendingPathComponent("fixtures")) else {
            return RailProbe()
        }
        let model = RailModel(fleet: fleet(for: fx))
        let probe = MotionProbe()
        let clockProbe = MotionProbe()
        model.animationProbe = clockProbe
        let size = railSize()
        let view = RailView(model: model).environment(\.revealProbe, probe)
        let host = NSHostingView(rootView: view)
        host.frame = NSRect(origin: .zero, size: size)
        let screen = NSScreen.main?.visibleFrame ?? .zero
        let window = NSWindow(
            contentRect: NSRect(x: screen.minX + 20, y: screen.minY + 20, width: size.width, height: size.height),
            styleMask: .borderless, backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.isOpaque = false
        window.backgroundColor = .clear
        window.level = .statusBar
        window.contentView = host
        window.orderFrontRegardless()
        defer { window.orderOut(nil) }
        try? await Task.sleep(nanoseconds: 400_000_000)
        probe.reset()
        model.setExpanded(true)
        try? await Task.sleep(nanoseconds: 900_000_000)
        var result = RailProbe()
        result.expand = clockProbe.snapshot()
        result.framesExpand = probe.snapshot()
        probe.reset()
        clockProbe.reset()
        model.setExpanded(false)
        try? await Task.sleep(nanoseconds: 900_000_000)
        result.collapse = clockProbe.snapshot()
        result.framesCollapse = probe.snapshot()

        // The reference: the same travel, animated by a SwiftUI spring made
        // from tokens.json's numbers, in a view of its own.
        let travel = Double(RailMetrics.revealHeight(model.slots.count))
        let tok = spec.tokens[Theme.Motion.railExpand.id]?.value
        let springAnim = Animation.spring(
            response: tok?["response"]?.double ?? .nan, dampingFraction: tok?["damping"]?.double ?? .nan)
        let refProbe = MotionProbe()
        let ref = ReferenceMotion()
        let refHost = NSHostingView(rootView: ReferenceMotionView(model: ref, travel: travel))
        refHost.frame = NSRect(x: 0, y: 0, width: 4, height: travel + 10)
        window.contentView = refHost
        try? await Task.sleep(nanoseconds: 300_000_000)
        let probed = Animation(ProbedAnimation(base: springAnim, probe: refProbe, travel: travel))
        withAnimation(probed) { ref.on = true }
        try? await Task.sleep(nanoseconds: 900_000_000)
        result.refExpand = refProbe.snapshot()
        refProbe.reset()
        withAnimation(probed) { ref.on = false }
        try? await Task.sleep(nanoseconds: 900_000_000)
        result.refCollapse = refProbe.snapshot()
        return result
    }

    /// A timing-curve motion (card open/close, paging): SwiftUI evaluates a
    /// curve exactly (no stepping), so each sample the app's animation
    /// returns must sit on the spec's curve; the duration is fitted from them.
    private func curveCheck(_ m: Spec.Motion) async {
        let st = "motion"
        guard let tok = spec.tokens[m.token]?.value, let d = tok["duration"]?.double, let curve = Self.unitCurve(tok) else {
            add(st, m.transition, m.token, "curve", "spec row unusable", "", .fail)
            return
        }
        let samples = await probeCard(m)
        let moving = samples.filter { $0.t > 0 && $0.v > 1e-6 && $0.v < 1 - 1e-6 }
        guard moving.count >= 2 else {
            add(st, "\(m.transition) app animation", m.token, "≥2 samples", "\(moving.count) samples", "", .fail)
            return
        }
        var best = (d: d, err: Double.infinity)
        var cand = 0.02
        while cand <= 0.6 {
            let err = moving.map { abs(curve.value(at: min(1, $0.t / cand)) - $0.v) }.max() ?? .infinity
            if err < best.err { best = (cand, err) }
            cand += 0.0005
        }
        let tolMs = spec.tolerances.duration_ms
        add(
            st, "\(m.transition) duration (\(moving.count) samples)", m.token, Self.f(d * 1000, 1) + " ms",
            Self.f(best.d * 1000, 1) + " ms", Self.f((best.d - d) * 1000, 1),
            abs(best.d - d) * 1000 <= tolMs ? .pass : .fail)
        add(
            st, "\(m.transition) on the spec curve", m.token, "≤ 0.01", Self.f(best.err, 4), "", best.err <= 0.01 ? .pass : .fail)
    }

    static func unitCurve(_ tok: JSONValue) -> UnitCurve? {
        switch tok["kind"]?.string {
        case "easeOut": return .easeOut
        case "easeIn": return .easeIn
        case "easeInOut": return .easeInOut
        case "linear": return .linear
        case "timingCurve":
            let c = (tok["c"]?.array ?? []).compactMap(\.double)
            guard c.count == 4 else { return nil }
            return .bezier(startControlPoint: UnitPoint(x: c[0], y: c[1]), endControlPoint: UnitPoint(x: c[2], y: c[3]))
        default: return nil
        }
    }

    /// Run one card motion in a visible window, the same calls the
    /// CardController and CardModel make, and return what the animation
    /// computed on SwiftUI's clock (progress 0…1).
    private func probeCard(_ m: Spec.Motion) async -> Samples {
        guard let fx = try? Fixture.load("card-open", from: specDir.appendingPathComponent("fixtures")),
              let key = fx.ui.card, let model = try? cardModel(fx, fleet: fleet(for: fx), key: key)
        else { return [] }
        let probe = MotionProbe()
        let presence = CardPresence()
        let holder = CardHolder()
        holder.model = model
        let size = Theme.Card.size.size
        let host = NSHostingView(rootView: CardHost(presence: presence, holder: holder))
        host.frame = NSRect(origin: .zero, size: size)
        let screen = NSScreen.main?.visibleFrame ?? .zero
        let window = NSWindow(
            contentRect: NSRect(x: screen.minX + 20, y: screen.minY + 20, width: size.width, height: size.height),
            styleMask: .borderless, backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.isOpaque = false
        window.backgroundColor = .clear
        window.level = .statusBar
        window.contentView = host
        window.orderFrontRegardless()
        defer { window.orderOut(nil) }
        let wait: UInt64 = 600_000_000
        switch m.token {
        case Theme.Motion.cardOpen.id:
            try? await Task.sleep(nanoseconds: 300_000_000)
            withAnimationToken(Theme.Motion.cardOpen, probe: probe) { presence.shown = true }
        case Theme.Motion.cardClose.id:
            presence.shown = true
            try? await Task.sleep(nanoseconds: 300_000_000)
            withAnimationToken(Theme.Motion.cardClose, probe: probe) { presence.shown = false }
        default:
            presence.shown = true
            try? await Task.sleep(nanoseconds: 300_000_000)
            model.animationProbe = probe
            model.page(1)
        }
        try? await Task.sleep(nanoseconds: wait)
        return probe.snapshot()
    }

    // MARK: report

    private func finish() -> Int32 {
        let failures = rows.filter { $0.result == .fail }.count
        let code: Int32 = failures > 0 ? 1 : 0
        let text = Self.table(rows) + "\n\(rows.filter { $0.result == .pass }.count) pass, \(failures) fail, "
            + "\(rows.filter { $0.result == .pending }.count) pending\n"
        let report: [String: Any] = [
            "exit_code": Int(code),
            "generated_at": ISO8601DateFormatter().string(from: Date()),
            "rows": rows.map {
                [
                    "state": $0.state, "check": $0.check, "token": $0.token, "spec": $0.spec,
                    "measured": $0.measured, "delta": $0.delta, "result": $0.result.rawValue,
                ]
            },
        ]
        try? text.write(to: out.appendingPathComponent("report.txt"), atomically: true, encoding: .utf8)
        try? "\(code)\n".write(to: out.appendingPathComponent("exit_code"), atomically: true, encoding: .utf8)
        if let data = try? JSONSerialization.data(withJSONObject: report, options: [.prettyPrinted, .sortedKeys]) {
            // Written last: its presence tells dev.sh the run is over.
            try? data.write(to: out.appendingPathComponent("report.json"), options: .atomic)
        }
        FileHandle.standardOutput.write(Data(text.utf8))
        return code
    }

    static func table(_ rows: [ParityRow]) -> String {
        let head = ["state", "check", "token", "spec", "measured", "Δ", "result"]
        let cells = rows.map { [$0.state, $0.check, $0.token, $0.spec, $0.measured, $0.delta, $0.result.rawValue] }
        let widths = head.indices.map { i in min(48, ([head[i]] + cells.map { $0[i] }).map(\.count).max() ?? 0) }
        func line(_ c: [String]) -> String {
            c.indices.map { i in
                let s = c[i].count > widths[i] ? String(c[i].prefix(widths[i] - 1)) + "…" : c[i]
                return s.padding(toLength: widths[i], withPad: " ", startingAt: 0)
            }.joined(separator: " | ")
        }
        return ([line(head), widths.map { String(repeating: "-", count: $0) }.joined(separator: "-+-")]
            + cells.map(line)).joined(separator: "\n")
    }
}

@MainActor
final class ReferenceMotion: ObservableObject {
    @Published var on = false
}

struct ReferenceMotionView: View {
    @ObservedObject var model: ReferenceMotion
    let travel: Double

    var body: some View {
        // The same animatable scalar the rail reveals with, so the probed
        // animation sees one value of exactly `travel`.
        Color.clear.frame(width: 1, height: travel)
            .modifier(RevealHeight(height: model.on ? travel : 0, probe: nil))
    }
}
