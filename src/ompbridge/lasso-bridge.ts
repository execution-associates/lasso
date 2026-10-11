// lasso's omp bridge (docs/design/bots.md, "omp bots"). lasso writes this file
// into every omp bot's folder as .lasso/omp-bridge.ts, and the bot's launch
// task loads it into the interactive omp TUI with `omp -e`. It serves the
// bot's socket from inside omp in the protocol lasso's Go side speaks
// (botomp.go): host_hello, omp's session events, and the RPC commands lasso
// sends. It is also the bot pane's one herdr reporter.
//
// It reaches past omp's public extension API in three places, all on the live
// session (pi.pi.AgentRegistry): the UI context dialogs are raced on, the
// command context for new and switch session, and the session's state.
// @ts-nocheck

import { AsyncLocalStorage } from "node:async_hooks";
import fs from "node:fs";
import net from "node:net";

// botBridgeProtocol in botomp.go. Bump both whenever a frame or its meaning
// changes: lasso restarts a bot whose bridge speaks another one.
const PROTOCOL = 4;

const env = process.env;

export default function lassoBridge(pi) {
  const socketPath = env.LASSO_BOT_SOCKET;
  // OMPCODE=1 marks a shell omp spawned: an omp started from inside the bot
  // is not the bot.
  if (!socketPath || env.OMPCODE === "1") return;
  // One bridge per process, whatever loads this file again.
  if (globalThis.__lassoBridge) return;
  globalThis.__lassoBridge = true;

  const clients = new Set();
  const pending = new Map(); // id -> { frame, settle(response) }
  const started = new Date().toISOString();
  let ready = false;
  let modelChanged = false;
  let thinkingChanged = false;
  let session = null;
  let unsubscribe = null;
  let lastCtx = null;
  let seq = 0;
  let shuttingDown = false;

  const nextId = () => `lb-${process.pid}-${++seq}`;
  const debug = (msg) => {
    if (env.LASSO_BRIDGE_LOG) {
      try {
        fs.appendFileSync(env.LASSO_BRIDGE_LOG, `${new Date().toISOString()} ${msg}\n`);
      } catch {}
    }
  };

  function write(sock, obj) {
    try {
      sock.write(`${JSON.stringify(obj)}\n`);
    } catch {}
  }

  function broadcast(obj) {
    let line;
    try {
      line = `${JSON.stringify(obj)}\n`;
    } catch {
      return;
    }
    for (const c of clients) {
      try {
        c.write(line);
      } catch {}
    }
  }

  function live() {
    return pi.pi.AgentRegistry.global().get(pi.pi.MAIN_AGENT_ID)?.session ?? null;
  }

  function hello() {
    return {
      type: "host_hello",
      protocol: PROTOCOL,
      version: env.LASSO_BRIDGE_VERSION ?? "",
      bot: env.LASSO_BOT_NAME ?? "",
      dir: process.cwd(),
      pid: process.pid,
      child_pid: process.pid,
      ready,
      streaming: !!session?.isStreaming,
      started_at: started,
      restarts: 0,
      // Changed since launch: what a newly connected lasso must keep.
      model_changed: modelChanged,
      thinking_changed: thinkingChanged,
      pending: [...pending.values()].map((p) => p.frame),
    };
  }

  function shutdown() {
    if (shuttingDown) return;
    shuttingDown = true;
    if (lastCtx?.shutdown) lastCtx.shutdown();
    else process.kill(process.pid, "SIGTERM");
  }

  // --- session events -------------------------------------------------------

  function attach(ctx) {
    lastCtx = ctx ?? lastCtx;
    const s = live();
    // The runner's own context is what the approval gate and tools call
    // (ctx.ui is a per-extension guard wrapped around it, and wrapping that
    // recursed). omp can rebuild it on re-initialize, so every event checks.
    patchUI(s?.extensionRunner?.getUIContext?.());
    if (!s || s === session) return;
    unsubscribe?.();
    session = s;
    unsubscribe = s.subscribe((event) => {
      if (event.type === "agent_start" || event.type === "agent_end") queueMicrotask(reportHerdr);
      // Token deltas: lasso reads finished messages from the session file.
      if (event.type === "message_update") return;
      if (event.type === "model_changed") modelChanged = true;
      if (event.type === "thinking_level_changed") thinkingChanged = true;
      broadcast(event);
    });
    if (!ready) {
      ready = true;
      broadcast({ type: "ready" });
    }
    reportHerdr();
  }

  pi.on("session_start", (_e, ctx) => {
    hookInput(ctx);
    attach(ctx);
  });
  pi.on("agent_start", (_e, ctx) => {
    hookInput(ctx);
    attach(ctx);
  });
  // A conversation changed from the terminal (/new, /resume) or by lasso:
  // lasso re-reads the session id.
  pi.on("session_switch", (_e, ctx) => {
    attach(ctx);
    broadcast({ type: "session_changed" });
    queueMicrotask(reportHerdr);
  });
  pi.on("session_shutdown", () => {
    shuttingDown = true;
    if (herdrHolds) herdrCall("pane.release_agent", {});
    unsubscribe?.();
    for (const c of clients) c.destroy();
    if (serving) {
      server.close();
      try {
        fs.unlinkSync(socketPath);
      } catch {}
    }
  });

  // --- self-restart -----------------------------------------------------------
  //
  // `mise run restart` leaves the marker. Once the turn is over and nothing is
  // waiting on the human, omp shuts down; the launch task sees the marker and
  // starts again with -c, so new env, servers and settings apply on the same
  // conversation.
  const marker = env.LASSO_BOT_RESTART;
  if (marker) {
    setInterval(() => {
      if (shuttingDown || !ready || !session || session.isStreaming || pending.size > 0) return;
      if (!fs.existsSync(marker)) return;
      debug("restart marker: shutting down for the task to relaunch");
      broadcast({ type: "host_restarting" });
      shutdown();
    }, 1000).unref?.();
  }

  // --- typed input --------------------------------------------------------------
  //
  // Text typed into the pane by something other than the human. herdr's agent
  // prompt (and anything built on it) sends a bracketed paste, then Enter
  // ~300ms later, nothing in between. Two states make that harmful:
  //
  // - A dialog is open. omp's dialog starts on its first option (Approve), so
  //   the Enter would answer it and the text would be lost.
  // - The human has a draft in the input box. The paste would be appended to
  //   it and the Enter would submit both, mangled together.
  //
  // In either state a paste is held back instead of inserted. If Enter comes
  // within PASTE_WINDOW_MS with no other key, it was a message: it is sent on
  // its own (a follow-up under a dialog, else a prompt or a steer), the Enter
  // is swallowed, and the dialog or the draft is left as it was. Any other
  // key, or the window passing, means it was the human pasting: the paste goes
  // into the input box as usual. A human who pastes into a draft and presses
  // Enter within the window gets the paste sent alone and the draft kept:
  // split, never lost or merged.
  const PASTE_START = "\x1b[200~";
  const PASTE_END = "\x1b[201~";
  const PASTE_WINDOW_MS = 1000;
  let inputHooked = false;
  let pasteBuf = null;
  let held = null; // { text, dialog, timer }

  const realUI = () => session?.extensionRunner?.getUIContext?.();
  const draftText = () => {
    try {
      return String(realUI()?.getEditorText?.() ?? "");
    } catch {
      return "";
    }
  };

  function sendTyped(text, dialog) {
    if (!text.trim()) return;
    let deliverAs;
    if (dialog) deliverAs = "followUp";
    else if (session?.isStreaming) deliverAs = "steer";
    debug(`typed message kept apart (${dialog ? "dialog open" : "draft kept"})`);
    pi.sendUserMessage(text, deliverAs ? { deliverAs } : undefined);
  }

  function releaseHeld() {
    if (!held) return;
    const h = held;
    held = null;
    clearTimeout(h.timer);
    if (h.dialog) {
      // Under a dialog a paste is never an answer: it waits as a message.
      sendTyped(h.text, true);
      return;
    }
    realUI()?.pasteToEditor?.(h.text);
  }

  function hookInput(ctx) {
    if (inputHooked || !ctx?.ui?.onTerminalInput) return;
    inputHooked = true;
    ctx.ui.onTerminalInput((data) => {
      const d = String(data);
      if (pasteBuf !== null || d.includes(PASTE_START)) {
        const dialog = pending.size > 0;
        if (pasteBuf === null && !dialog && !draftText().trim()) return undefined; // nothing to protect
        pasteBuf = (pasteBuf ?? "") + d;
        const end = pasteBuf.indexOf(PASTE_END);
        if (end < 0) return { consume: true };
        const text = pasteBuf.slice(pasteBuf.indexOf(PASTE_START) + PASTE_START.length, end);
        const rest = pasteBuf.slice(end + PASTE_END.length);
        pasteBuf = null;
        releaseHeld(); // an earlier held paste was not followed by Enter
        if (/^[\r\n]/.test(rest)) {
          sendTyped(text, dialog);
          return { consume: true };
        }
        held = { text, dialog, timer: setTimeout(releaseHeld, PASTE_WINDOW_MS) };
        return { consume: true };
      }
      if (held) {
        if (d === "\r" || d === "\n") {
          const h = held;
          held = null;
          clearTimeout(h.timer);
          sendTyped(h.text, h.dialog);
          return { consume: true };
        }
        releaseHeld();
      }
      return undefined;
    });
  }

  // --- herdr ------------------------------------------------------------------------
  //
  // The bridge is the pane's one reporter (source lasso:bot, agent omp).
  // herdr's own omp extension is kept inert for a bot (the task unsets
  // HERDR_ENV and hands the socket and pane over as LASSO_HERDR_*): herdr takes
  // a restore command only from the source holding the pane, and it must be
  // the bot's launch task, so a restore brings back the env, the secrets and
  // this bridge. omp is the pane's foreground process, which herdr requires of
  // a reporter that sets one.
  const herdrSock = env.LASSO_HERDR_SOCKET;
  const herdrPane = env.LASSO_HERDR_PANE;
  const resumeArgv = (() => {
    try {
      return JSON.parse(env.LASSO_BOT_RESUME ?? "null");
    } catch {
      return null;
    }
  })();
  let herdrHolds = false;
  let herdrLast = "";

  function herdrCall(method, params) {
    return new Promise((resolve) => {
      if (!herdrSock || !herdrPane) return resolve(null);
      const c = net.createConnection(herdrSock);
      let buf = "";
      const done = (v) => {
        c.destroy();
        resolve(v);
      };
      c.on("connect", () => {
        // seq rises across every report from this source, restarts included.
        const req = {
          id: `lasso:bot:${nextId()}`,
          method,
          params: { pane_id: herdrPane, source: "lasso:bot", agent: "omp", seq: Date.now() * 1e6 + (++seq % 1e6), ...params },
        };
        c.write(`${JSON.stringify(req)}\n`);
      });
      c.on("data", (d) => {
        buf += d;
        const nl = buf.indexOf("\n");
        if (nl < 0) return;
        let r = null;
        try {
          r = JSON.parse(buf.slice(0, nl));
        } catch {}
        if (r?.error) debug(`herdr ${method}: ${r.error.code}: ${r.error.message}`);
        done(r);
      });
      c.on("error", (e) => {
        debug(`herdr ${method}: ${e.message}`);
        done(null);
      });
      setTimeout(() => done(null), 2000).unref?.();
    });
  }

  async function reportHerdr() {
    if (!herdrSock || !herdrPane || !session || shuttingDown) return;
    const open = [...pending.values()][0]?.frame;
    const state = open ? "blocked" : session.isStreaming ? "working" : "idle";
    const message = open ? String(open.title ?? "").split("\n")[0] : undefined;
    const key = JSON.stringify([state, message, session.sessionId]);
    if (key === herdrLast) return;
    herdrLast = key;
    if (!herdrHolds) {
      // A restore command is taken only from a reporter already holding the
      // pane, so the first report claims it with the state alone.
      const r = await herdrCall("pane.report_agent", { state });
      if (!r?.result) {
        herdrLast = "";
        return;
      }
      herdrHolds = true;
      // The pane goes by the bot's name: herdr's agent commands and lasso's
      // findBotPane address it so.
      herdrCall("agent.rename", { target: herdrPane, name: env.LASSO_BOT_NAME ?? "bot" });
    }
    const params = { state, agent_session_id: session.sessionId, agent_session_path: session.sessionFile };
    if (message) params.message = message;
    if (resumeArgv) params.resume_argv = resumeArgv;
    const r = await herdrCall("pane.report_agent", params);
    if (!r?.result) herdrLast = "";
  }

  // --- dialogs: the terminal and lasso race -------------------------------------
  //
  // A wrapper remembers the function it wraps, so a context copied from an
  // already patched one is patched over the original, never over another
  // wrapper. inRace guards the rest: a dialog shown from inside a race is the
  // local half, never mirrored again.
  const ORIG = Symbol.for("lasso.bridge.orig");
  const inRace = new AsyncLocalStorage();

  function patchUI(ui) {
    if (!ui) return;
    const label = (o) => (typeof o === "string" ? o : (o?.label ?? String(o)));
    const value = (r) => ("value" in r ? r.value : undefined);
    const specs = {
      select: (fn) => (title, options, opts) =>
        race(
          { method: "select", title, options: options.map(label) },
          value,
          undefined,
          (signal) => fn.call(ui, title, options, { ...opts, signal }),
          opts?.signal,
          () => fn.call(ui, title, options, opts)
        ),
      confirm: (fn) => (title, message, opts) =>
        race(
          { method: "confirm", title, message },
          (r) => r.confirmed === true,
          false,
          (signal) => fn.call(ui, title, message, { ...opts, signal }),
          opts?.signal,
          () => fn.call(ui, title, message, opts)
        ),
      input: (fn) => (title, placeholder, opts) =>
        race(
          { method: "input", title, placeholder },
          value,
          undefined,
          (signal) => fn.call(ui, title, placeholder, { ...opts, signal }),
          opts?.signal,
          () => fn.call(ui, title, placeholder, opts)
        ),
      editor: (fn) => (title, prefill, opts, editorOpts) =>
        race(
          { method: "editor", title, prefill },
          value,
          undefined,
          (signal) => fn.call(ui, title, prefill, { ...opts, signal }, editorOpts),
          opts?.signal,
          () => fn.call(ui, title, prefill, opts, editorOpts)
        ),
    };
    for (const [name, make] of Object.entries(specs)) {
      const cur = ui[name];
      if (typeof cur !== "function" || cur.owner === ui) continue;
      const orig = cur[ORIG] ?? cur;
      const wrapper = make(orig);
      wrapper[ORIG] = orig;
      wrapper.owner = ui;
      ui[name] = wrapper;
    }
  }

  // race shows the dialog in the terminal and mirrors it to lasso; the first
  // answer wins and closes the other side. With no lasso connected it is just
  // the terminal's dialog, still pending so a lasso connecting later sees it
  // in host_hello.
  function race(request, parse, cancelled, local, parent, plain) {
    if (inRace.getStore()) return plain();
    const id = nextId();
    const frame = { type: "extension_ui_request", id, ...request };
    const localAbort = new AbortController();
    const localSignal = parent ? AbortSignal.any([parent, localAbort.signal]) : localAbort.signal;
    let remoteSettle;
    const remote = new Promise((resolve) => {
      remoteSettle = resolve;
    });
    pending.set(id, { frame, settle: (r) => remoteSettle(r.cancelled ? cancelled : parse(r)) });
    broadcast(frame);
    reportHerdr();
    const done = () => {
      if (!pending.delete(id)) return;
      reportHerdr();
      broadcast({ type: "extension_ui_request", id: nextId(), method: "cancel", targetId: id });
    };
    parent?.addEventListener("abort", done, { once: true });
    const localWin = inRace.run(id, () => Promise.resolve(local(localSignal))).then((v) => ({ local: true, value: v }));
    const remoteWin = remote.then((v) => ({ local: false, value: v }));
    return Promise.race([localWin, remoteWin]).then((w) => {
      if (w.local) done();
      else localAbort.abort();
      return w.value;
    });
  }

  // --- commands -----------------------------------------------------------------------

  function respond(sock, f, data, error) {
    const r = { type: "response", id: f.id, command: f.type, success: !error };
    if (error) r.error = error;
    else if (data !== undefined) r.data = data;
    write(sock, r);
  }

  function state() {
    const s = session;
    return {
      model: s.model,
      thinkingLevel: s.thinkingLevel,
      isStreaming: s.isStreaming,
      sessionFile: s.sessionFile,
      sessionId: s.sessionId,
      sessionName: s.sessionName,
      queuedMessageCount: s.queuedMessageCount,
      messageCount: s.messages.length,
      contextUsage: s.getContextUsage(),
    };
  }

  async function handle(sock, f) {
    if (f.type === "extension_ui_response") {
      const p = pending.get(f.id);
      if (!p) return;
      pending.delete(f.id);
      broadcast({ type: "host_ui_answered", id: f.id });
      reportHerdr();
      p.settle(f);
      return;
    }
    if (f.type === "host_stop") {
      await session?.abort?.().catch(() => {});
      shutdown();
      return;
    }
    if (!session) return respond(sock, f, undefined, "omp is not ready");
    switch (f.type) {
      case "get_state":
        return respond(sock, f, state());
      case "prompt":
      case "steer":
      case "follow_up": {
        // A pushed message: the human's words from lasso, a job, a channel.
        // Through the extension API, so the TUI shows it like typed input, and
        // never through the input box, so a draft there is left alone.
        let deliverAs;
        if (f.type === "steer") deliverAs = "steer";
        else if (f.type === "follow_up") deliverAs = "followUp";
        else if (session.isStreaming) deliverAs = f.streamingBehavior === "followUp" ? "followUp" : "steer";
        const content = f.images?.length ? [{ type: "text", text: f.message }, ...f.images] : f.message;
        pi.sendUserMessage(content, deliverAs ? { deliverAs } : undefined);
        return respond(sock, f, f.type === "prompt" ? { agentInvoked: true } : undefined);
      }
      case "abort":
        if (lastCtx?.abort) await lastCtx.abort();
        else await session.abort();
        return respond(sock, f);
      case "set_model": {
        const model = session.getAvailableModels().find((m) => m.provider === f.provider && m.id === f.modelId);
        if (!model) return respond(sock, f, undefined, `Model not found: ${f.provider}/${f.modelId}`);
        if (!(await pi.setModel(model))) return respond(sock, f, undefined, "no credentials for that model");
        return respond(sock, f, model);
      }
      case "set_thinking_level":
        pi.setThinkingLevel(f.level);
        return respond(sock, f);
      case "new_session":
        return respond(sock, f, await session.extensionRunner.createCommandContext().newSession());
      case "switch_session":
        return respond(sock, f, await session.extensionRunner.createCommandContext().switchSession(f.sessionPath));
      default:
        return respond(sock, f, undefined, `the omp bridge does not handle ${f.type}`);
    }
  }

  // --- the socket ---------------------------------------------------------------------
  //
  // The socket is full control of the session: 0600, in lasso's 0700 bots dir.
  // A live socket belongs to another copy of this bot, and is never taken: two
  // omps answering for one bot would split its conversation.

  let serving = false;
  const server = net.createServer((sock) => {
    clients.add(sock);
    write(sock, hello());
    let buf = "";
    sock.setEncoding("utf8");
    sock.on("data", (chunk) => {
      buf += chunk;
      for (let i = buf.indexOf("\n"); i >= 0; i = buf.indexOf("\n")) {
        const line = buf.slice(0, i);
        buf = buf.slice(i + 1);
        if (!line.trim()) continue;
        let f;
        try {
          f = JSON.parse(line);
        } catch {
          continue;
        }
        handle(sock, f).catch((err) => respond(sock, f, undefined, String(err?.message ?? err)));
      }
    });
    const drop = () => clients.delete(sock);
    sock.on("close", drop);
    sock.on("error", drop);
  });
  server.on("error", (err) => debug(`socket: ${err.message}`));
  server.unref?.();

  function listen() {
    try {
      fs.mkdirSync(socketPath.slice(0, socketPath.lastIndexOf("/")), { recursive: true, mode: 0o700 });
    } catch {}
    try {
      fs.unlinkSync(socketPath);
    } catch {}
    const old = process.umask(0o177);
    server.listen(socketPath, () => {
      process.umask(old);
      serving = true;
      try {
        fs.chmodSync(socketPath, 0o600);
      } catch {}
    });
  }

  const probe = net.createConnection(socketPath);
  probe.on("connect", () => {
    probe.destroy();
    debug("another copy of this bot holds its socket; this one is not reachable from lasso");
  });
  probe.on("error", () => listen());
}
