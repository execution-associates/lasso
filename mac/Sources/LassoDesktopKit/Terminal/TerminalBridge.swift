import AppKit
import Foundation
import WebKit

// How lasso's own terminal page runs in the app (PLAN 22): ttyd's document,
// served by lasso under /terminal/<slug>/, the one the web app frames, run as
// a lasso page (Page/LassoPage.swift) under the `lasso-terminal:` scheme. It
// is confined to the one terminal the window shows: its document, the
// `token` beside it and its `ws`, and nothing else on lasso.

/// The addresses the page and lasso use for one host's terminal.
public enum HerdrTerminal {
    /// The scheme the page is served under.
    public static let scheme = "lasso-terminal"
    /// The host part of every page URL; it names nothing.
    static let pageHost = "lasso"

    /// Where lasso serves `slug`'s terminal document (ttyd's -b base).
    static func documentPath(_ slug: String) -> String { "/terminal/\(slug)/" }

    /// The page's own address: ttyd builds its token and websocket URLs from
    /// it, and reads its options (lasso's palette) from its query.
    static func pageURL(slug: String, query: String) -> URL {
        var c = URLComponents()
        c.scheme = scheme
        c.host = pageHost
        c.percentEncodedPath = documentPath(slug)
        if !query.isEmpty { c.percentEncodedQuery = query }
        return c.url!
    }

    /// The path and query on lasso a page request asks for, or nil when it
    /// is anything but this terminal's own files.
    static func lassoPath(_ url: URL, slug: String) -> String? {
        guard url.scheme == scheme, url.host == pageHost,
              let c = URLComponents(url: url, resolvingAgainstBaseURL: false)
        else { return nil }
        let path = c.percentEncodedPath
        let base = documentPath(slug)
        // The document itself, or one file beside it (ttyd's `token`):
        // nothing further down, nothing escaped, nothing relative.
        let rest = path.hasPrefix(base) ? path.dropFirst(base.count) : "/"
        guard !rest.contains("/"), !rest.contains("%"), rest != "..", rest != "." else { return nil }
        return path + (c.percentEncodedQuery.map { "?" + $0 } ?? "")
    }

    /// The websocket on lasso for the page's (ttyd's `<base>/ws`), or nil
    /// for any other path.
    static func socketURL(base: URL, path: String, query: String?, slug: String) -> URL? {
        guard path == documentPath(slug) + "ws" else { return nil }
        return LassoPageBridge.socketURL(base: base, path: path, query: query)
    }
}

/// Whether this window may resize herdr's terminal (lasso's term-claim): every
/// client of one herdr shares its size, and herdr reflows every pane on each
/// change, so only the client a human last acted in sizes it.
struct TermClaim {
    let me: String
    /// Who holds the size: "" is nobody, which any client may take.
    var owner = ""
    var lastClaim: Date?

    var may: Bool { owner.isEmpty || owner == me }

    /// A human acted here: whether to tell lasso. At once when the size is
    /// not ours; a renewal at most every `renew` seconds.
    mutating func due(now: Date, renew: Double) -> Bool {
        if owner == me, let l = lastClaim, now.timeIntervalSince(l) < renew { return false }
        lastClaim = now
        return true
    }
}

/// The terminal's page: its requests and its socket confined to `slug`'s
/// terminal, a resize held while another client has the size, the window's
/// text size, and herdr's copies.
@MainActor
final class TerminalBridge: LassoPageBridge {
    /// The terminal the page may reach; nil reaches nothing.
    var slug: String?
    /// herdr copied (OSC 52).
    var onCopy: (String) -> Void = { _ in }
    /// lasso did not serve the terminal's document: why, in its words.
    var onDocumentFailed: (String) -> Void = { _ in }

    /// What herdr may copy in one go.
    static let copyLimit = 1 << 20

    private var resizeAllowed = true
    private var fontSize = 0

    init(client: LassoClient) { super.init(client: client, global: "__lassoTerm") }

    override func lassoPath(_ url: URL, method: String) -> String? {
        guard method == "GET", let slug else { return nil }
        return HerdrTerminal.lassoPath(url, slug: slug)
    }

    override func socketURL(path: String, query: String?) -> URL? {
        guard let slug else { return nil }
        return HerdrTerminal.socketURL(base: client.baseURL, path: path, query: query, slug: slug)
    }

    /// lasso refused the document: say why over the page rather than show
    /// lasso's error text as one.
    override func refused(path: String, code: Int, body: Data) -> Bool {
        guard let slug else { return false }
        let doc = HerdrTerminal.documentPath(slug)
        guard path == doc || path.hasPrefix(doc + "?") else { return false }
        let why = String(decoding: body.prefix(300), as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
        onDocumentFailed("\(LassoError.http(code, why))")
        return true
    }

    override func handle(_ op: String, _ body: [String: Any]) {
        guard op == "copy", let t = body["text"] as? String, t.utf8.count <= Self.copyLimit else { return }
        onCopy(t)
    }

    /// Whether resizes may go to lasso; held ones go when it turns true.
    func setResizeAllowed(_ may: Bool) {
        resizeAllowed = may
        pushGate()
    }

    /// Tell the page (a new one starts allowed).
    func pushGate() { js("gate(\(resizeAllowed))") }

    /// The terminal's text size (TerminalZoom): the page sets xterm's
    /// fontSize and refits, now and after every (re)connect.
    func setFontSize(_ size: Int) {
        fontSize = size
        pushZoom()
    }

    /// Tell the page (a new one has it in its query already).
    func pushZoom() {
        guard fontSize > 0 else { return }
        js("zoom(\(fontSize))")
    }
}

extension TerminalBridge {
    /// Runs in the page before ttyd's own script: the lasso page's native
    /// socket and links, plus a held resize while another client has the
    /// size, the window's text size, right-clicks for herdr rather than
    /// WebKit's menu, herdr's copies and Shift/Ctrl+Enter as herdr reads them
    /// (ported from the web app's lib/terminal.ts).
    nonisolated static let script = LassoPage.script(
        handler: "lassoTerm", global: "__lassoTerm",
        prelude: #"""
      // ttyd's resize is "1" and {"columns","rows"}. herdr reflows every pane
      // for every client on each one, so it waits while another client holds
      // the size, and one taken before layout settles is never sent.
      const RESIZE = 0x31;
      const gate = { may: true, held: new Map() };
      // The window's text size (⌘= ⌘- ⌘0), as xterm's fontSize: the option
      // the web app's terminal text sets. ttyd's own fit (window.term.fit)
      // turns it into columns and rows, and its resize goes through the gate
      // above. Again on the next frame, in case the remeasure trails the
      // write (lib/theme.ts's nudge). ttyd re-applies lasso's options, and
      // the page's query over them, on every connect (its SET_PREFERENCES,
      // "2"), so the size is kept in the query and set again after each.
      const SET_PREFERENCES = 0x32;
      let zoom = 0;
      const applyZoom = () => {
        const term = window.term;
        if (!zoom || !term || !term.options || term.options.fontSize === zoom) return;
        term.options.fontSize = zoom;
        const fit = () => {
          try {
            if (typeof term.fit === "function") term.fit();
            else dispatchEvent(new Event("resize"));
          } catch (e) {}
        };
        fit();
        requestAnimationFrame(fit);
      };
      const laidOut = (u) => {
        try {
          const j = JSON.parse(dec.decode(u.subarray(1)));
          return j.columns >= 20 && j.rows >= 4;
        } catch (e) {
          return true;
        }
      };
"""#,
        sendGuard: #"""
          if (!b.text && b.u.length && b.u[0] === RESIZE) {
            if (!laidOut(b.u)) return;
            if (!gate.may) {
              gate.held.set(this._id, b.u.slice());
              return;
            }
          }
"""#,
        onMessage: "      if (u.length && u[0] === SET_PREFERENCES) applyZoom();",
        onClosed: "      gate.held.delete(id);",
        api: #"""
        gate(may) {
          gate.may = may;
          if (!may) return;
          for (const [id, u] of gate.held) {
            const s = sockets.get(id);
            if (s && s.readyState === 1) post({ op: "send", id: id, data: b64(u), text: false });
          }
          gate.held.clear();
        },
        zoom(size) {
          zoom = size;
          try {
            const u = new URL(location.href);
            if (u.searchParams.get("fontSize") !== String(size)) {
              u.searchParams.set("fontSize", String(size));
              history.replaceState(history.state, "", u.href);
            }
          } catch (e) {}
          applyZoom();
        },
"""#,
        rest: #"""
      // A key or a pointer here is a human using this window: what claims
      // the terminal's size (the web app's lib/term-claim.ts).
      const act = () => post({ op: "act" });
      addEventListener("keydown", act, { capture: true, passive: true });
      addEventListener("pointerdown", act, { capture: true, passive: true });
      // herdr reads right-clicks itself: the press and release reach xterm
      // (and herdr, through its mouse reporting), and WebKit's own menu
      // (Reload, Inspect Element) never opens. A Control-click is a
      // right-click, as everywhere on the Mac: its press and release go to
      // xterm as the right button's.
      addEventListener("contextmenu", (e) => e.preventDefault(), { capture: true });
      let ctrlClick = false;
      const asRight = (e, type, buttons) => {
        e.preventDefault();
        e.stopImmediatePropagation();
        const t = e.target;
        if (!t || typeof t.dispatchEvent !== "function") return;
        t.dispatchEvent(
          new MouseEvent(type, {
            button: 2,
            buttons: buttons,
            bubbles: true,
            cancelable: true,
            composed: true,
            clientX: e.clientX,
            clientY: e.clientY,
            screenX: e.screenX,
            screenY: e.screenY,
            detail: e.detail,
            shiftKey: e.shiftKey,
            altKey: e.altKey,
            view: window,
          })
        );
      };
      addEventListener(
        "mousedown",
        (e) => {
          if (!e.isTrusted || e.button !== 0 || !e.ctrlKey || e.metaKey) return;
          ctrlClick = true;
          asRight(e, "mousedown", 2);
        },
        { capture: true }
      );
      addEventListener(
        "mouseup",
        (e) => {
          if (!ctrlClick || !e.isTrusted || e.button !== 0) return;
          ctrlClick = false;
          asRight(e, "mouseup", 0);
        },
        { capture: true }
      );
      const wire = (tries) => {
        const term = window.term;
        if (!term || !term.parser || typeof term.attachCustomKeyEventHandler !== "function") {
          if (tries < 40) setTimeout(() => wire(tries + 1), 150);
          return;
        }
        // herdr copies with OSC 52, which ttyd's xterm.js drops.
        term.parser.registerOscHandler(52, (data) => {
          const semi = data.indexOf(";");
          const b = semi === -1 ? data : data.slice(semi + 1);
          if (b === "" || b === "?") return true;
          try {
            post({ op: "copy", text: dec.decode(unb64(b)) });
          } catch (e) {}
          return true;
        });
        // xterm.js sends a bare CR for Shift+Enter and Ctrl+Enter; herdr
        // reads a Kitty CSI-u key and passes the chord on.
        term.attachCustomKeyEventHandler((e) => {
          const enter = e.key === "Enter" || e.code === "Enter" || e.code === "NumpadEnter" || e.keyCode === 13;
          if (e.type !== "keydown" || !enter || e.metaKey || !(e.shiftKey || e.ctrlKey)) return true;
          e.preventDefault();
          const seq = "\x1b[13;" + (1 + (e.shiftKey ? 1 : 0) + (e.altKey ? 2 : 0) + (e.ctrlKey ? 4 : 0)) + "u";
          if (typeof term.input === "function") term.input(seq);
          else if (term._core && term._core.coreService) term._core.coreService.triggerDataEvent(seq, true);
          return false;
        });
      };
      wire(0);
"""#)
}
