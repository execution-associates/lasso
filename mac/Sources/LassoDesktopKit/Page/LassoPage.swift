import AppKit
import Foundation
import WebKit

// How one of lasso's own pages runs in the app: the herdr window's terminal
// (PLAN 22), the Browser window (PLAN 24) and a file window's editor (PLAN 26). A WKWebView cannot put a bearer
// token on a websocket, and Cloudflare Access would want its own cookie, so
// the page never touches the network:
//
//   - its files and its fetches come through a URL scheme the app answers,
//     each one a request through the app's LassoClient, credentials and all;
//   - its WebSocket is a stand-in (LassoPage.script) that hands every frame
//     to a native URLSessionWebSocketTask, which carries the same credentials
//     on its upgrade (and no Origin, which lasso's guards take as a client
//     that is not a web page).
//
// Each page is confined to what it needs: a subclass says which requests and
// which websockets it may make (lassoPath, socketURL), and every other one is
// refused, so the page cannot reach /api/file with this Mac's sign-in.

/// A WKURLSchemeTask carried into the task that answers it.
private struct SchemeTask: @unchecked Sendable {
    let task: any WKURLSchemeTask
}

/// A page's two ways out: its requests through the scheme, its websockets
/// through native tasks. One per window.
@MainActor
class LassoPageBridge: NSObject {
    let client: LassoClient
    weak var webView: WKWebView?
    /// The page's object the app calls into (`window.<global>`).
    let global: String
    /// A human acted in the page (a key, a pointer).
    var onAct: () -> Void = {}
    /// A link the page opened.
    var onLink: (URL) -> Void = { _ in }

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

    init(client: LassoClient, global: String) {
        self.client = client
        self.global = global
        super.init()
        events.bridge = self
    }

    // MARK: what the page may reach (a subclass's)

    /// The path and query on lasso a page request asks for, or nil when the
    /// page may not have it.
    func lassoPath(_ url: URL, method: String) -> String? { nil }

    /// The websocket on lasso for a path the page opened, or nil.
    func socketURL(path: String, query: String?) -> URL? { nil }

    /// lasso answered a request with `code`: true when the bridge took care
    /// of it (the request then fails in the page).
    func refused(path: String, code: Int, body: Data) -> Bool { false }

    /// A message the page sent that is not one of the bridge's own.
    func handle(_ op: String, _ body: [String: Any]) {}

    /// The largest request body the page may send; a larger one is refused
    /// whole, never cut short (a cut file write would be a cut file).
    var maxBody: Int { 1 << 20 }

    /// A request `lassoPath` allowed, once its body is read: false refuses
    /// it (a write the page may make only about its own file).
    func admit(path: String, method: String, body: Data?) -> Bool { true }

    /// lasso's response headers the page sees, besides its Content-Type:
    /// an editor's mtime for its next save (/api/file's X-Lasso-Mtime), and
    /// whether lasso sent its "too large to preview" line instead of the file
    /// (X-Lasso-Too-Large: the editor then must not save it over the file).
    nonisolated static let passedHeaders = ["X-Lasso-Mtime", "X-Lasso-Too-Large", "Last-Modified"]

    // MARK: calling the page

    func js(_ call: String) {
        webView?.evaluateJavaScript("window.\(global) && window.\(global)." + call, completionHandler: nil)
    }

    /// `base` with the page's path on it, ws or wss as `base` is http or https.
    nonisolated static func socketURL(base: URL, path: String, query: String?) -> URL? {
        guard var c = URLComponents(url: base, resolvingAgainstBaseURL: false) else { return nil }
        c.scheme = base.scheme == "https" ? "wss" : "ws"
        let prefix = c.percentEncodedPath.hasSuffix("/") ? String(c.percentEncodedPath.dropLast()) : c.percentEncodedPath
        c.percentEncodedPath = prefix + path
        c.percentEncodedQuery = (query?.isEmpty ?? true) ? nil : query
        return c.url
    }

    // MARK: sockets

    /// Close every socket: the page went away (a new page, a reload, the
    /// window closed), and with it whatever each one ran on lasso.
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

    func open(_ id: Int, path: String, query: String?, protocols: [String]) {
        let key = Key(generation: generation, id: id)
        guard let url = socketURL(path: path, query: query) else {
            js("closed(\(id), 1008, 'not this page', true)")
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
        // Frames of up to 16 MB: a browser's screencast frame is a large JPEG.
        task.maximumMessageSize = 16 << 20
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
        js("closed(\(id), \(code), '', false)")
    }

    /// The upgrade went through: the page may send, and frames flow back.
    func opened(_ key: Key, protocol p: String?) {
        guard let s = sockets[key], !s.opened else { return }
        s.opened = true
        let proto = (p ?? "").filter { $0.isLetter || $0.isNumber || $0 == "-" || $0 == "." }
        js("opened(\(key.id), '\(proto)')")
        receive(key, s.task)
    }

    private func receive(_ key: Key, _ task: URLSessionWebSocketTask) {
        Task { [weak self] in
            while true {
                guard let message = try? await task.receive() else { return }
                guard let self, self.sockets[key] != nil else { return }
                switch message {
                case .data(let d): self.js("message(\(key.id), '\(d.base64EncodedString())', false)")
                case .string(let s): self.js("message(\(key.id), '\(Data(s.utf8).base64EncodedString())', true)")
                @unknown default: break
                }
            }
        }
    }

    /// lasso closed it.
    func closed(_ key: Key, code: Int) {
        guard sockets.removeValue(forKey: key) != nil else { return }
        js("closed(\(key.id), \(code), '', false)")
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
        js("closed(\(key.id), 1006, '', \(failed))")
    }
}

// MARK: the page's requests

extension LassoPageBridge: WKURLSchemeHandler {
    func webView(_ webView: WKWebView, start urlSchemeTask: any WKURLSchemeTask) {
        let id = ObjectIdentifier(urlSchemeTask)
        let req = urlSchemeTask.request
        let method = (req.httpMethod ?? "GET").uppercased()
        guard let url = req.url, let path = lassoPath(url, method: method) else {
            urlSchemeTask.didFailWithError(URLError(.noPermissionsToReadFile))
            return
        }
        var body: Data?
        if method != "GET", method != "HEAD" {
            if let b = req.httpBody {
                body = b.count <= maxBody ? b : nil
            } else if let stream = req.httpBodyStream {
                body = Self.drain(stream, limit: maxBody)
            } else {
                body = Data()
            }
            guard body != nil else {
                urlSchemeTask.didFailWithError(URLError(.dataLengthExceedsMaximum))
                return
            }
        }
        guard admit(path: path, method: method, body: body) else {
            urlSchemeTask.didFailWithError(URLError(.noPermissionsToReadFile))
            return
        }
        let assetKey = method == "GET" && LassoPageAssets.isAsset(path) ? client.baseURL.absoluteString + path : nil
        if let assetKey, let hit = LassoPageAssets.shared.get(assetKey) {
            let out = HTTPURLResponse(
                url: url, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": hit.type, "Cache-Control": "no-store"])!
            urlSchemeTask.didReceive(out)
            urlSchemeTask.didReceive(hit.data)
            urlSchemeTask.didFinish()
            return
        }
        var r = client.request(raw: path)
        r.httpMethod = method
        r.setValue("*/*", forHTTPHeaderField: "Accept")
        if let body {
            r.httpBody = body
            if let type = req.value(forHTTPHeaderField: "Content-Type") { r.setValue(type, forHTTPHeaderField: "Content-Type") }
        }
        let box = SchemeTask(task: urlSchemeTask)
        Task {
            defer { self.stopped.remove(id) }
            do {
                let (data, http) = try await client.response(r)
                guard !self.stopped.contains(id) else { return }
                let code = http?.statusCode ?? 0
                if !(200..<300).contains(code), self.refused(path: path, code: code, body: data) {
                    box.task.didFailWithError(URLError(.badServerResponse))
                    return
                }
                let type = http?.value(forHTTPHeaderField: "Content-Type") ?? "application/octet-stream"
                var headers = ["Content-Type": type, "Cache-Control": "no-store"]
                for h in Self.passedHeaders {
                    if let v = http?.value(forHTTPHeaderField: h) { headers[h] = v }
                }
                if let assetKey, code == 200 { LassoPageAssets.shared.put(assetKey, data: data, type: type) }
                let out = HTTPURLResponse(
                    url: url, statusCode: code == 0 ? 502 : code, httpVersion: "HTTP/1.1", headerFields: headers)!
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

    /// A request body WebKit handed over as a stream; nil past `limit`.
    nonisolated static func drain(_ s: InputStream, limit: Int) -> Data? {
        var out = Data()
        var buf = [UInt8](repeating: 0, count: 16 << 10)
        s.open()
        defer { s.close() }
        while s.hasBytesAvailable {
            let n = s.read(&buf, maxLength: buf.count)
            if n <= 0 { break }
            out.append(buf, count: n)
            if out.count > limit { return nil }
        }
        return out
    }
}

/// A build's `/assets/` files: what every lasso page loads its scripts and
/// styles from. Their names carry their content's hash, so one never changes
/// under its name, and the app keeps them in memory (per lasso, up to
/// `capacity` bytes, the oldest going first): a file window opened for each
/// file an agent shows loads them once, not once a window.
@MainActor
final class LassoPageAssets {
    static let shared = LassoPageAssets()
    let capacity: Int
    private var files: [String: (data: Data, type: String)] = [:]
    private var order: [String] = []
    private var bytes = 0

    init(capacity: Int = 32 << 20) { self.capacity = capacity }

    /// The page may load `/assets/<name>`: one level, no escapes, nothing hidden.
    nonisolated static func isAsset(_ path: String) -> Bool {
        guard path.hasPrefix("/assets/") else { return false }
        let name = path.dropFirst("/assets/".count)
        return !name.isEmpty && !name.contains("/") && !name.contains("%") && !name.contains("?") && !name.hasPrefix(".")
    }

    func get(_ key: String) -> (data: Data, type: String)? { files[key] }

    func put(_ key: String, data: Data, type: String) {
        guard files[key] == nil, data.count <= capacity / 4 else { return }
        files[key] = (data, type)
        order.append(key)
        bytes += data.count
        while bytes > capacity, let old = order.first {
            order.removeFirst()
            bytes -= files.removeValue(forKey: old)?.data.count ?? 0
        }
    }
}

// MARK: the page's messages

extension LassoPageBridge: WKScriptMessageHandler {
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
        default:
            handle(op, body)
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
    weak var bridge: LassoPageBridge?

    private func key(_ task: URLSessionTask) -> LassoPageBridge.Key? {
        let parts = (task.taskDescription ?? "").split(separator: ":").compactMap { Int($0) }
        return parts.count == 2 ? LassoPageBridge.Key(generation: parts[0], id: parts[1]) : nil
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

/// A page's web view. WebKit's own menu (Reload, Inspect Element) never
/// opens: the page cancels its event, and this empties the menu for a click
/// the page did not see (one before it loaded). A click on the page acts
/// whether or not its window has the keyboard yet: WebKit takes a first
/// click only to make the window key, so the Browser window's "+" (or any
/// of its page's controls) clicked while another app was in front did
/// nothing. ⌘W is its window's (AgentWindowPanel).
class LassoPageWebView: WKWebView {
    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }

    override func willOpenMenu(_ menu: NSMenu, with event: NSEvent) {
        menu.removeAllItems()
        super.willOpenMenu(menu, with: event)
    }
}

/// The script every lasso page runs before its own: a WebSocket that is a
/// native socket (through the bridge), and window.open handed to the app.
/// A page adds its own part: `prelude` runs first, `sendGuard` in a socket's
/// send with the frame in `b` (a `return` holds it back), `onMessage` after a
/// frame reaches the page (as `u`), `onClosed` when a socket closes (`id`),
/// `api` adds members to the page's object, and `rest` runs last.
enum LassoPage {
    static func script(
        handler: String, global: String, prelude: String = "", sendGuard: String = "", onMessage: String = "",
        onClosed: String = "", api: String = "", rest: String = ""
    ) -> String {
        #"""
        (() => {
          if (window.\#(global)) return;
          const post = (m) => window.webkit.messageHandlers.\#(handler).postMessage(m);
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
        \#(prelude)
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
        \#(sendGuard)
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
          window.\#(global) = {
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
        \#(onMessage)
            },
            closed(id, code, reason, failed) {
              const s = sockets.get(id);
              if (!s) return;
              sockets.delete(id);
        \#(onClosed)
              s.readyState = 3;
              if (failed) s._fire("error");
              s._fire("close", { code: code, reason: reason || "", wasClean: !failed });
            },
        \#(api)
          };
          // Links (a terminal's, a browser page's "open in a new tab") open
          // in the Mac's browser: a blank window whose location is set later,
          // or a URL at once.
          window.open = (url) => {
            if (url) {
              post({ op: "link", url: String(url) });
              return null;
            }
            const loc = {};
            Object.defineProperty(loc, "href", { set: (v) => post({ op: "link", url: String(v) }) });
            return { opener: null, location: loc, close() {} };
          };
        \#(rest)
        })();
        """#
    }
}
