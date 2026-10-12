import AppKit
import Foundation
import WebKit

// How lasso's own terminal page runs in the app (PLAN 22). The page is ttyd's
// document, served by lasso under /terminal/<slug>/, the one the web app
// frames. A WKWebView cannot put a bearer token on a websocket, and Cloudflare
// Access would want its own cookie, so the page never touches the network:
//
//   - its document and its `token` fetch come through a URL scheme the app
//     answers (`lasso-terminal:`), each one a request through the app's
//     LassoClient, credentials and all;
//   - its WebSocket is a stand-in (TerminalBridge.script) that hands every
//     frame to a native URLSessionWebSocketTask, which carries the same
//     credentials on its upgrade.
//
// Both are confined to the one terminal the window shows: a request for any
// other path on lasso is refused, so the page cannot reach /api/file with
// this Mac's sign-in.

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
        guard path == documentPath(slug) + "ws", var c = URLComponents(url: base, resolvingAgainstBaseURL: false) else { return nil }
        c.scheme = base.scheme == "https" ? "wss" : "ws"
        let prefix = c.percentEncodedPath.hasSuffix("/") ? String(c.percentEncodedPath.dropLast()) : c.percentEncodedPath
        c.percentEncodedPath = prefix + path
        c.percentEncodedQuery = (query?.isEmpty ?? true) ? nil : query
        return c.url
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

/// A WKURLSchemeTask carried into the task that answers it.
private struct SchemeTask: @unchecked Sendable {
    let task: any WKURLSchemeTask
}

/// The page's two ways out: its files through the scheme, its websockets
/// through native tasks. One per window; `slug` names the terminal it serves.
@MainActor
final class TerminalBridge: NSObject {
    let client: LassoClient
    /// The terminal the page may reach; nil reaches nothing.
    var slug: String?
    weak var webView: WKWebView?
    /// A human acted in the page (a key, a pointer).
    var onAct: () -> Void = {}
    /// A link the terminal opened.
    var onLink: (URL) -> Void = { _ in }
    /// herdr copied (OSC 52).
    var onCopy: (String) -> Void = { _ in }
    /// lasso did not serve the terminal's document: why, in its words.
    var onDocumentFailed: (String) -> Void = { _ in }

    /// One socket the page opened. `generation` changes on every new page,
    /// whose ids start again from 1.
    struct Key: Hashable {
        let generation: Int
        let id: Int
    }

    final class Socket {
        let task: URLSessionWebSocketTask
        let request: URLRequest
        var opened = false
        var retried: Bool

        init(task: URLSessionWebSocketTask, request: URLRequest, retried: Bool) {
            self.task = task
            self.request = request
            self.retried = retried
        }
    }

    private var generation = 0
    private var sockets: [Key: Socket] = [:]
    private var stopped: Set<ObjectIdentifier> = []
    private var resizeAllowed = true
    private let events = SocketEvents()
    private var sessionStore: URLSession?
    /// The websockets' session, made when one is first needed.
    private var session: URLSession {
        if let s = sessionStore { return s }
        let cfg = URLSessionConfiguration.ephemeral
        cfg.httpCookieStorage = nil
        cfg.urlCache = nil
        let s = URLSession(configuration: cfg, delegate: events, delegateQueue: nil)
        sessionStore = s
        return s
    }

    /// What herdr may copy in one go.
    static let copyLimit = 1 << 20

    init(client: LassoClient) {
        self.client = client
        super.init()
        events.bridge = self
    }

    /// Close every socket: the page went away (a new page, a reload, the
    /// window closed), and with it the herdr client each one runs.
    func closeAll() {
        for s in sockets.values { s.task.cancel(with: .goingAway, reason: nil) }
        sockets.removeAll()
        generation += 1
    }

    /// The window closed: every socket, and the session (which holds its
    /// delegate until it is invalidated).
    func shutdown() {
        closeAll()
        sessionStore?.invalidateAndCancel()
        sessionStore = nil
    }

    /// Whether resizes may go to lasso; held ones go when it turns true.
    func setResizeAllowed(_ may: Bool) {
        resizeAllowed = may
        pushGate()
    }

    /// Tell the page (a new one starts allowed).
    func pushGate() { js("__lassoTerm.gate(\(resizeAllowed))") }

    private var fontSize = 0

    /// The terminal's text size (TerminalZoom): the page sets xterm's
    /// fontSize and refits, now and after every (re)connect.
    func setFontSize(_ size: Int) {
        fontSize = size
        pushZoom()
    }

    /// Tell the page (a new one has it in its query already).
    func pushZoom() {
        guard fontSize > 0 else { return }
        js("__lassoTerm.zoom(\(fontSize))")
    }

    private func js(_ call: String) {
        webView?.evaluateJavaScript("window.__lassoTerm && " + call, completionHandler: nil)
    }

    // MARK: sockets

    func open(_ id: Int, path: String, query: String?, protocols: [String]) {
        let key = Key(generation: generation, id: id)
        guard let slug, let url = HerdrTerminal.socketURL(base: client.baseURL, path: path, query: query, slug: slug) else {
            js("__lassoTerm.closed(\(id), 1008, 'not this terminal', true)")
            return
        }
        Task {
            await client.auth?.freshen()
            var r = URLRequest(url: url)
            client.authorize(&r)
            // A websocket answers no Accept; the rest (X-Requested-With, the
            // credentials) are what /api requests carry.
            r.setValue(nil, forHTTPHeaderField: "Accept")
            if !protocols.isEmpty { r.setValue(protocols.joined(separator: ", "), forHTTPHeaderField: "Sec-WebSocket-Protocol") }
            guard key.generation == generation else { return }
            start(key, r, retried: false)
        }
    }

    private func start(_ key: Key, _ r: URLRequest, retried: Bool) {
        let task = session.webSocketTask(with: r)
        task.taskDescription = "\(key.generation):\(key.id)"
        sockets[key] = Socket(task: task, request: r, retried: retried)
        task.resume()
    }

    func send(_ id: Int, _ data: Data, text: Bool) {
        guard let s = sockets[Key(generation: generation, id: id)], s.opened else { return }
        s.task.send(text ? .string(String(decoding: data, as: UTF8.self)) : .data(data)) { _ in }
    }

    func close(_ id: Int, code: Int, reason: String) {
        let key = Key(generation: generation, id: id)
        guard let s = sockets.removeValue(forKey: key) else { return }
        s.task.cancel(with: URLSessionWebSocketTask.CloseCode(rawValue: code) ?? .normalClosure, reason: Data(reason.utf8))
        js("__lassoTerm.closed(\(id), \(code), '', false)")
    }

    /// The upgrade went through: the page may send, and frames flow back.
    func opened(_ key: Key, protocol p: String?) {
        guard let s = sockets[key], !s.opened else { return }
        s.opened = true
        let proto = (p ?? "").filter { $0.isLetter || $0.isNumber || $0 == "-" || $0 == "." }
        js("__lassoTerm.opened(\(key.id), '\(proto)')")
        receive(key, s.task)
    }

    private func receive(_ key: Key, _ task: URLSessionWebSocketTask) {
        Task { [weak self] in
            while true {
                guard let message = try? await task.receive() else { return }
                guard let self, self.sockets[key] != nil else { return }
                switch message {
                case .data(let d): self.js("__lassoTerm.message(\(key.id), '\(d.base64EncodedString())', false)")
                case .string(let s): self.js("__lassoTerm.message(\(key.id), '\(Data(s.utf8).base64EncodedString())', true)")
                @unknown default: break
                }
            }
        }
    }

    /// lasso closed it.
    func closed(_ key: Key, code: Int) {
        guard sockets.removeValue(forKey: key) != nil else { return }
        js("__lassoTerm.closed(\(key.id), \(code), '', false)")
    }

    /// The task ended without a close frame: a refused upgrade or a dropped
    /// connection. A token lasso refused is refreshed once and tried again.
    func failed(_ key: Key, status: Int?) {
        guard let s = sockets[key] else { return }
        if !s.opened, !s.retried, status == 401, let auth = client.auth {
            Task {
                guard await auth.refreshAfterRefusal(), self.sockets[key] === s else { return self.closed(key, failed: true) }
                var r = s.request
                auth.apply(&r)
                self.start(key, r, retried: true)
            }
            return
        }
        closed(key, failed: true)
    }

    private func closed(_ key: Key, failed: Bool) {
        guard sockets.removeValue(forKey: key) != nil else { return }
        js("__lassoTerm.closed(\(key.id), 1006, '', \(failed))")
    }
}

// MARK: the page's files

extension TerminalBridge: WKURLSchemeHandler {
    func webView(_ webView: WKWebView, start urlSchemeTask: any WKURLSchemeTask) {
        let id = ObjectIdentifier(urlSchemeTask)
        guard let url = urlSchemeTask.request.url, let slug, let path = HerdrTerminal.lassoPath(url, slug: slug) else {
            urlSchemeTask.didFailWithError(URLError(.noPermissionsToReadFile))
            return
        }
        var r = client.request(raw: path)
        r.setValue("*/*", forHTTPHeaderField: "Accept")
        let box = SchemeTask(task: urlSchemeTask)
        Task {
            defer { self.stopped.remove(id) }
            do {
                let (data, http) = try await client.response(r)
                guard !self.stopped.contains(id) else { return }
                let code = http?.statusCode ?? 0
                if !(200..<300).contains(code), path == HerdrTerminal.documentPath(slug) || path.hasPrefix(HerdrTerminal.documentPath(slug) + "?") {
                    // Say why over the page rather than show lasso's error text as one.
                    let why = String(decoding: data.prefix(300), as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
                    self.onDocumentFailed("\(LassoError.http(code, why))")
                    box.task.didFailWithError(URLError(.badServerResponse))
                    return
                }
                let type = http?.value(forHTTPHeaderField: "Content-Type") ?? "application/octet-stream"
                let out = HTTPURLResponse(
                    url: url, statusCode: http?.statusCode ?? 502, httpVersion: "HTTP/1.1",
                    headerFields: ["Content-Type": type, "Cache-Control": "no-store"])!
                box.task.didReceive(out)
                box.task.didReceive(data)
                box.task.didFinish()
            } catch {
                guard !self.stopped.contains(id) else { return }
                box.task.didFailWithError(error)
            }
        }
    }

    func webView(_ webView: WKWebView, stop urlSchemeTask: any WKURLSchemeTask) {
        stopped.insert(ObjectIdentifier(urlSchemeTask))
    }
}

// MARK: the page's messages

extension TerminalBridge: WKScriptMessageHandler {
    func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        guard message.frameInfo.isMainFrame, let body = message.body as? [String: Any], let op = body["op"] as? String else { return }
        let id = (body["id"] as? NSNumber)?.intValue ?? 0
        switch op {
        case "open":
            open(id, path: body["path"] as? String ?? "", query: body["query"] as? String, protocols: body["protocols"] as? [String] ?? [])
        case "send":
            guard let b = body["data"] as? String, let d = Data(base64Encoded: b) else { return }
            send(id, d, text: (body["text"] as? NSNumber)?.boolValue ?? false)
        case "close":
            close(id, code: (body["code"] as? NSNumber)?.intValue ?? 1000, reason: body["reason"] as? String ?? "")
        case "act":
            onAct()
        case "link":
            guard let s = body["url"] as? String, let u = URL(string: s), ["http", "https", "mailto"].contains(u.scheme?.lowercased() ?? "")
            else { return }
            onLink(u)
        case "copy":
            guard let t = body["text"] as? String, t.utf8.count <= Self.copyLimit else { return }
            onCopy(t)
        default: break
        }
    }
}

/// The user content controller keeps its handlers: this keeps the bridge
/// out of that cycle.
final class WeakMessageHandler: NSObject, WKScriptMessageHandler {
    weak var target: (any WKScriptMessageHandler)?

    init(_ target: any WKScriptMessageHandler) { self.target = target }

    func userContentController(_ c: WKUserContentController, didReceive message: WKScriptMessage) {
        target?.userContentController(c, didReceive: message)
    }
}

/// The websocket session's delegate: each event hops to the bridge.
private final class SocketEvents: NSObject, URLSessionWebSocketDelegate, @unchecked Sendable {
    weak var bridge: TerminalBridge?

    private func key(_ task: URLSessionTask) -> TerminalBridge.Key? {
        let parts = (task.taskDescription ?? "").split(separator: ":").compactMap { Int($0) }
        return parts.count == 2 ? TerminalBridge.Key(generation: parts[0], id: parts[1]) : nil
    }

    func urlSession(_ session: URLSession, webSocketTask: URLSessionWebSocketTask, didOpenWithProtocol p: String?) {
        guard let k = key(webSocketTask) else { return }
        let b = bridge
        Task { @MainActor in b?.opened(k, protocol: p) }
    }

    func urlSession(
        _ session: URLSession, webSocketTask: URLSessionWebSocketTask, didCloseWith closeCode: URLSessionWebSocketTask.CloseCode,
        reason: Data?
    ) {
        guard let k = key(webSocketTask) else { return }
        let b = bridge
        Task { @MainActor in b?.closed(k, code: closeCode.rawValue) }
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: (any Error)?) {
        guard let k = key(task) else { return }
        let status = (task.response as? HTTPURLResponse)?.statusCode
        let b = bridge
        Task { @MainActor in b?.failed(k, status: status) }
    }
}

extension TerminalBridge {
    /// Runs in the page before ttyd's own script: a WebSocket that is a
    /// native socket, links and herdr's copies handed to the app, a held
    /// resize while another client has the size, the window's text size,
    /// and Shift/Ctrl+Enter as herdr reads them (ported from the web app's
    /// lib/terminal.ts).
    nonisolated static let script = #"""
    (() => {
      if (window.__lassoTerm) return;
      const post = (m) => window.webkit.messageHandlers.lassoTerm.postMessage(m);
      const enc = new TextEncoder();
      const dec = new TextDecoder();
      const toBytes = (d) => {
        if (typeof d === "string") return { u: enc.encode(d), text: true };
        if (d instanceof ArrayBuffer) return { u: new Uint8Array(d), text: false };
        if (ArrayBuffer.isView(d)) return { u: new Uint8Array(d.buffer, d.byteOffset, d.byteLength), text: false };
        return null;
      };
      const b64 = (u) => {
        let s = "";
        for (let i = 0; i < u.length; i += 0x8000) s += String.fromCharCode.apply(null, u.subarray(i, i + 0x8000));
        return btoa(s);
      };
      const unb64 = (s) => {
        const bin = atob(s);
        const u = new Uint8Array(bin.length);
        for (let i = 0; i < bin.length; i++) u[i] = bin.charCodeAt(i);
        return u;
      };
      const sockets = new Map();
      let next = 1;
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
      class LassoSocket extends EventTarget {
        constructor(url, protocols) {
          super();
          this.url = String(url);
          this.readyState = 0;
          this.protocol = "";
          this.extensions = "";
          this.bufferedAmount = 0;
          this.binaryType = "blob";
          this.onopen = this.onmessage = this.onerror = this.onclose = null;
          this._id = next++;
          sockets.set(this._id, this);
          const u = new URL(this.url);
          const list = protocols == null ? [] : [].concat(protocols).map(String);
          post({ op: "open", id: this._id, path: u.pathname, query: u.search.replace(/^\?/, ""), protocols: list });
        }
        send(d) {
          if (this.readyState === 0) throw new DOMException("still connecting", "InvalidStateError");
          if (this.readyState !== 1) return;
          const b = toBytes(d);
          if (!b) return;
          if (!b.text && b.u.length && b.u[0] === RESIZE) {
            if (!laidOut(b.u)) return;
            if (!gate.may) {
              gate.held.set(this._id, b.u.slice());
              return;
            }
          }
          post({ op: "send", id: this._id, data: b64(b.u), text: b.text });
        }
        close(code, reason) {
          if (this.readyState >= 2) return;
          this.readyState = 2;
          post({ op: "close", id: this._id, code: code || 1000, reason: reason || "" });
        }
        _fire(type, init) {
          const e =
            type === "message" ? new MessageEvent("message", init) : type === "close" ? new CloseEvent("close", init) : new Event(type);
          const h = this["on" + type];
          if (typeof h === "function") h.call(this, e);
          this.dispatchEvent(e);
        }
      }
      for (const [k, v] of Object.entries({ CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3 })) {
        Object.defineProperty(LassoSocket, k, { value: v });
        Object.defineProperty(LassoSocket.prototype, k, { value: v });
      }
      window.WebSocket = LassoSocket;
      window.__lassoTerm = {
        opened(id, protocol) {
          const s = sockets.get(id);
          if (!s || s.readyState !== 0) return;
          s.readyState = 1;
          s.protocol = protocol || "";
          s._fire("open");
        },
        message(id, data, text) {
          const s = sockets.get(id);
          if (!s || s.readyState !== 1) return;
          const u = unb64(data);
          const payload = text ? dec.decode(u) : s.binaryType === "arraybuffer" ? u.buffer : new Blob([u]);
          s._fire("message", { data: payload });
          if (u.length && u[0] === SET_PREFERENCES) applyZoom();
        },
        closed(id, code, reason, failed) {
          const s = sockets.get(id);
          if (!s) return;
          sockets.delete(id);
          gate.held.delete(id);
          s.readyState = 3;
          if (failed) s._fire("error");
          s._fire("close", { code: code, reason: reason || "", wasClean: !failed });
        },
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
      };
      // A key or a pointer here is a human using this window: what claims
      // the terminal's size (the web app's lib/term-claim.ts).
      const act = () => post({ op: "act" });
      addEventListener("keydown", act, { capture: true, passive: true });
      addEventListener("pointerdown", act, { capture: true, passive: true });
      // xterm's links open a blank window and then set its location.
      window.open = (url) => {
        if (url) {
          post({ op: "link", url: String(url) });
          return null;
        }
        const loc = {};
        Object.defineProperty(loc, "href", { set: (v) => post({ op: "link", url: String(v) }) });
        return { opener: null, location: loc, close() {} };
      };
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
    })();
    """#
}
