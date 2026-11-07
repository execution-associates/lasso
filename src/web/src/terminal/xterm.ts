// lasso's port of ttyd's terminal client, html/src/components/terminal/xterm
// (MIT, Copyright (c) 2016 Shuanglei Tao), on a current xterm.js with the image
// addon's kitty graphics protocol turned on. ttyd still owns the PTY and the
// wire protocol; only the document it used to serve is ours now.
//
// What changed from ttyd's, and why:
//   - xterm.js 6 has no canvas renderer. `rendererType=canvas` (which
//     withLiveTtydTheme asks for, to dodge ttyd 1.7's WebGL alpha bug) now
//     means the DOM renderer; `webgl` still loads the WebGL addon.
//   - The image addon is always loaded (kitty, sixel and iTerm IIP), not only
//     under `enableSixel`, since kitty graphics is the reason this page exists.
//   - No zmodem/trzsz: lasso never enables them, and they pulled in a file
//     transfer UI with no place in an iframe.
//
// Kept on purpose, because lib/terminal.ts and the Mac app depend on them:
// `window.term` (with `fit()`), the option merge order {built-ins,
// server-sent -t options, URL query}, the class-less overlay node and its
// "Press ⏎ to Reconnect" text, and the one-shot Enter listener behind it.
import { ClipboardAddon } from "@xterm/addon-clipboard"
import { FitAddon } from "@xterm/addon-fit"
import { ImageAddon } from "@xterm/addon-image"
import { Unicode11Addon } from "@xterm/addon-unicode11"
import { WebLinksAddon } from "@xterm/addon-web-links"
import { WebglAddon } from "@xterm/addon-webgl"
import type { IDisposable, ITerminalOptions } from "@xterm/xterm"
import { Terminal } from "@xterm/xterm"
import { OverlayAddon } from "./overlay"

interface TtydTerminal extends Terminal {
  fit(): void
}

// Not a `declare global`: this file shares a tsconfig with the app, whose
// lib/terminal.ts types the same global its own way, from the parent's side.
type TermGlobal = { term?: TtydTerminal }

// ttyd's wire commands: the first byte of every frame.
const Cmd = {
  // server → client
  OUTPUT: "0",
  SET_WINDOW_TITLE: "1",
  SET_PREFERENCES: "2",
  // client → server
  INPUT: "0",
  RESIZE_TERMINAL: "1",
  PAUSE: "2",
  RESUME: "3",
} as const

export type RendererType = "dom" | "canvas" | "webgl"

export interface ClientOptions {
  rendererType: RendererType
  disableLeaveAlert: boolean
  disableResizeOverlay: boolean
  enableSixel: boolean
  titleFixed?: string
  unicodeVersion: string
  closeOnDisconnect: boolean
}

type Preferences = ITerminalOptions &
  ClientOptions &
  Record<string, unknown> & { disableReconnect?: boolean }

export interface FlowControl {
  limit: number
  highWater: number
  lowWater: number
}

export interface XtermOptions {
  wsUrl: string
  tokenUrl: string
  flowControl: FlowControl
  clientOptions: ClientOptions
  termOptions: ITerminalOptions
}

function toDisposable(f: () => void): IDisposable {
  return { dispose: f }
}

function listen(
  target: EventTarget,
  type: string,
  listener: EventListener
): IDisposable {
  target.addEventListener(type, listener)
  return toDisposable(() => target.removeEventListener(type, listener))
}

export class Xterm {
  private disposables: IDisposable[] = []
  private textEncoder = new TextEncoder()
  private textDecoder = new TextDecoder()
  private written = 0
  private pending = 0

  private terminal!: Terminal
  readonly fitAddon = new FitAddon()
  private overlayAddon = new OverlayAddon()
  private webglAddon?: WebglAddon
  private imageAddon?: ImageAddon
  private unicodeLoaded = false

  private socket?: WebSocket
  private token = ""
  private opened = false
  private title?: string
  private titleFixed?: string
  private resizeOverlay = true
  private reconnect = true
  doReconnect = true
  private closeOnDisconnect = false
  private readonly options: XtermOptions

  constructor(options: XtermOptions) {
    this.options = options
  }

  dispose() {
    for (const d of this.disposables) d.dispose()
    this.disposables.length = 0
  }

  register = <T extends IDisposable>(d: T): T => {
    this.disposables.push(d)
    return d
  }

  refreshToken = async () => {
    try {
      const resp = await fetch(this.options.tokenUrl)
      if (resp.ok) {
        const json = (await resp.json()) as { token?: string }
        this.token = json.token ?? ""
      }
    } catch (e) {
      console.error(`[ttyd] fetch ${this.options.tokenUrl}: `, e)
    }
  }

  private onWindowUnload = (event: BeforeUnloadEvent) => {
    event.preventDefault()
    if (this.socket?.readyState === WebSocket.OPEN) {
      const message = "Close terminal? this will also terminate the command."
      event.returnValue = message
      return message
    }
    return undefined
  }

  open(parent: HTMLElement) {
    this.terminal = new Terminal(this.options.termOptions)
    const { terminal, fitAddon, overlayAddon } = this
    const t = terminal as TtydTerminal
    t.fit = () => fitAddon.fit()
    ;(window as unknown as TermGlobal).term = t

    terminal.loadAddon(fitAddon)
    terminal.loadAddon(overlayAddon)
    terminal.loadAddon(new ClipboardAddon())
    terminal.loadAddon(new WebLinksAddon())

    terminal.open(parent)
    // The image addon reads the renderer's cell size, so it goes in after
    // open(). Loaded before the first byte arrives, it is also what answers
    // a program's kitty support query (`ESC _ G a=q … ESC \`): herdr asks
    // once per client and only forwards graphics to a client that answered.
    this.imageAddon = new ImageAddon({ kittySupport: true })
    terminal.loadAddon(this.imageAddon)
    fitAddon.fit()
  }

  private initListeners() {
    const { terminal, fitAddon, overlayAddon, register } = this
    register(
      terminal.onTitleChange((data) => {
        if (data && !this.titleFixed) document.title = `${data} | ${this.title}`
      })
    )
    register(terminal.onData((data) => this.sendData(data)))
    register(
      terminal.onBinary((data) =>
        this.sendData(Uint8Array.from(data, (v) => v.charCodeAt(0)))
      )
    )
    register(
      terminal.onResize(({ cols, rows }) => {
        const msg = JSON.stringify({ columns: cols, rows })
        this.socket?.send(this.textEncoder.encode(Cmd.RESIZE_TERMINAL + msg))
        if (this.resizeOverlay) overlayAddon.showOverlay(`${cols}x${rows}`, 300)
      })
    )
    register(
      terminal.onSelectionChange(() => {
        if (this.terminal.getSelection() === "") return
        try {
          document.execCommand("copy")
        } catch {
          return
        }
        this.overlayAddon.showOverlay("✂", 200)
      })
    )
    register(listen(window, "resize", () => fitAddon.fit()))
    register(
      listen(window, "beforeunload", this.onWindowUnload as EventListener)
    )
  }

  writeData = (data: string | Uint8Array) => {
    const { terminal, textEncoder } = this
    const { limit, highWater, lowWater } = this.options.flowControl

    this.written += data.length
    if (this.written > limit) {
      terminal.write(data, () => {
        this.pending = Math.max(this.pending - 1, 0)
        if (this.pending < lowWater) {
          this.socket?.send(textEncoder.encode(Cmd.RESUME))
        }
      })
      this.pending++
      this.written = 0
      if (this.pending > highWater) {
        this.socket?.send(textEncoder.encode(Cmd.PAUSE))
      }
    } else {
      terminal.write(data)
    }
  }

  sendData = (data: string | Uint8Array) => {
    const { socket, textEncoder } = this
    if (socket?.readyState !== WebSocket.OPEN) return

    if (typeof data === "string") {
      const payload = new Uint8Array(data.length * 3 + 1)
      payload[0] = Cmd.INPUT.charCodeAt(0)
      const stats = textEncoder.encodeInto(data, payload.subarray(1))
      socket.send(payload.subarray(0, stats.written + 1))
    } else {
      const payload = new Uint8Array(data.length + 1)
      payload[0] = Cmd.INPUT.charCodeAt(0)
      payload.set(data, 1)
      socket.send(payload)
    }
  }

  connect = () => {
    this.socket = new WebSocket(this.options.wsUrl, ["tty"])
    const { socket, register } = this
    socket.binaryType = "arraybuffer"
    register(listen(socket, "open", this.onSocketOpen))
    register(listen(socket, "message", this.onSocketData as EventListener))
    register(listen(socket, "close", this.onSocketClose as EventListener))
    register(
      listen(socket, "error", () => {
        this.doReconnect = false
      })
    )
  }

  private onSocketOpen = () => {
    console.log("[ttyd] websocket connection opened")
    const { textEncoder, terminal, overlayAddon } = this
    const msg = JSON.stringify({
      AuthToken: this.token,
      columns: terminal.cols,
      rows: terminal.rows,
    })
    this.socket?.send(textEncoder.encode(msg))

    if (this.opened) {
      terminal.reset()
      terminal.options.disableStdin = false
      overlayAddon.showOverlay("Reconnected", 300)
    } else {
      this.opened = true
    }

    this.doReconnect = this.reconnect
    this.initListeners()
    terminal.focus()
  }

  private onSocketClose = (event: CloseEvent) => {
    console.log(`[ttyd] websocket connection closed with code: ${event.code}`)
    const { refreshToken, connect, doReconnect, overlayAddon } = this
    overlayAddon.showOverlay("Connection Closed")
    this.dispose()

    // 1000: CLOSE_NORMAL
    if (event.code !== 1000 && doReconnect) {
      overlayAddon.showOverlay("Reconnecting...")
      refreshToken().then(connect)
    } else if (this.closeOnDisconnect) {
      window.close()
    } else {
      const keyDispose = this.terminal.onKey((e) => {
        if (e.domEvent.key === "Enter") {
          keyDispose.dispose()
          overlayAddon.showOverlay("Reconnecting...")
          refreshToken().then(connect)
        }
      })
      overlayAddon.showOverlay("Press ⏎ to Reconnect")
    }
  }

  private parseOptsFromUrlQuery(query: string): Preferences {
    const clientOptions = this.options.clientOptions as unknown as Record<
      string,
      unknown
    >
    const termOptions = this.terminal.options as unknown as Record<
      string,
      unknown
    >
    const prefs: Record<string, unknown> = {}
    for (const [k, queryVal] of new URLSearchParams(query)) {
      let v = clientOptions[k]
      if (v === undefined) v = termOptions[k]
      switch (typeof v) {
        case "boolean":
          prefs[k] = queryVal === "true" || queryVal === "1"
          break
        case "number":
        case "bigint":
          prefs[k] = Number.parseInt(queryVal, 10)
          break
        case "string":
          prefs[k] = queryVal
          break
        case "object":
          try {
            prefs[k] = JSON.parse(queryVal)
          } catch {
            console.warn(`[ttyd] bad JSON for option ${k}, ignored`)
          }
          break
        default:
          console.warn(
            `[ttyd] maybe unknown option: ${k}=${queryVal}, treating as string`
          )
          prefs[k] = queryVal
          break
      }
    }
    return prefs as Preferences
  }

  private onSocketData = (event: MessageEvent) => {
    const rawData = event.data as ArrayBuffer
    const cmd = String.fromCharCode(new Uint8Array(rawData)[0])
    const data = rawData.slice(1)

    switch (cmd) {
      case Cmd.OUTPUT:
        this.writeData(new Uint8Array(data))
        break
      case Cmd.SET_WINDOW_TITLE:
        this.title = this.textDecoder.decode(data)
        document.title = this.title
        break
      case Cmd.SET_PREFERENCES:
        this.applyPreferences({
          ...this.options.clientOptions,
          ...JSON.parse(this.textDecoder.decode(data)),
          ...this.parseOptsFromUrlQuery(window.location.search),
        } as Preferences)
        break
      default:
        console.warn(`[ttyd] unknown command: ${cmd}`)
        break
    }
  }

  private applyPreferences(prefs: Preferences) {
    const { terminal, fitAddon } = this
    const termOptions = terminal.options as unknown as Record<string, unknown>
    for (const [key, value] of Object.entries(prefs)) {
      switch (key) {
        case "rendererType":
          this.setRendererType(value as RendererType)
          break
        case "disableLeaveAlert":
          if (value) {
            window.removeEventListener("beforeunload", this.onWindowUnload)
          }
          break
        case "disableResizeOverlay":
          if (value) this.resizeOverlay = false
          break
        case "disableReconnect":
          if (value) {
            this.reconnect = false
            this.doReconnect = false
          }
          break
        case "enableSixel":
        case "enableZmodem":
        case "enableTrzsz":
        case "trzszDragInitTimeout":
        case "isWindows":
          // Images are always on (see open()); file transfer is not offered.
          break
        case "closeOnDisconnect":
          if (value) {
            this.closeOnDisconnect = true
            this.reconnect = false
            this.doReconnect = false
          }
          break
        case "titleFixed":
          if (typeof value !== "string" || value === "") break
          this.titleFixed = value
          document.title = value
          break
        case "unicodeVersion":
          // ttyd's default is 11; "6" leaves xterm's built-in tables.
          if (String(value) !== "6" && !this.unicodeLoaded) {
            this.unicodeLoaded = true
            terminal.loadAddon(new Unicode11Addon())
            terminal.unicode.activeVersion = "11"
          }
          break
        default:
          if (termOptions[key] instanceof Object) {
            termOptions[key] = Object.assign({}, termOptions[key], value)
          } else {
            termOptions[key] = value
          }
          if (key.startsWith("font")) fitAddon.fit()
          break
      }
    }
  }

  private setRendererType(value: RendererType) {
    const disposeWebgl = () => {
      try {
        this.webglAddon?.dispose()
      } catch {
        // ignore
      }
      this.webglAddon = undefined
    }
    if (value !== "webgl") {
      disposeWebgl()
      return
    }
    if (this.webglAddon) return
    this.webglAddon = new WebglAddon()
    try {
      this.webglAddon.onContextLoss(() => disposeWebgl())
      this.terminal.loadAddon(this.webglAddon)
    } catch (e) {
      console.log("[ttyd] WebGL renderer unavailable, using the DOM one", e)
      disposeWebgl()
    }
  }
}
