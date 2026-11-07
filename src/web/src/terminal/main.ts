// Entry point of terminal.html, the document lasso serves at /terminal/<slug>/
// (see xterm.ts for why it replaces ttyd's own). The defaults below are ttyd's;
// lasso's spawn-time `-t` options and the URL query override them.
import type { ITerminalOptions } from "@xterm/xterm"
import "@xterm/xterm/css/xterm.css"
import "./terminal.css"
import { type ClientOptions, type FlowControl, Xterm } from "./xterm"

const protocol = window.location.protocol === "https:" ? "wss:" : "ws:"
const path = window.location.pathname.replace(/[/]+$/, "")
const wsUrl = `${protocol}//${window.location.host}${path}/ws${window.location.search}`
const tokenUrl = `${window.location.protocol}//${window.location.host}${path}/token`

const clientOptions: ClientOptions = {
  rendererType: "webgl",
  disableLeaveAlert: false,
  disableResizeOverlay: false,
  enableSixel: false,
  closeOnDisconnect: false,
  unicodeVersion: "11",
}

const termOptions: ITerminalOptions = {
  fontSize: 13,
  fontFamily: "Consolas,Liberation Mono,Menlo,Courier,monospace",
  theme: {
    foreground: "#d2d2d2",
    background: "#2b2b2b",
    cursor: "#adadad",
    black: "#000000",
    red: "#d81e00",
    green: "#5ea702",
    yellow: "#cfae00",
    blue: "#427ab3",
    magenta: "#89658e",
    cyan: "#00a7aa",
    white: "#dbded8",
    brightBlack: "#686a66",
    brightRed: "#f54235",
    brightGreen: "#99e343",
    brightYellow: "#fdeb61",
    brightBlue: "#84b0d8",
    brightMagenta: "#bc94b7",
    brightCyan: "#37e6e8",
    brightWhite: "#f1f1f0",
  },
  // The image addon and lasso's parser hooks (OSC 52) are proposed API.
  allowProposedApi: true,
}

const flowControl: FlowControl = { limit: 100000, highWater: 10, lowWater: 4 }

async function start() {
  const container = document.getElementById("terminal-container")
  if (!container) return
  const xterm = new Xterm({
    wsUrl,
    tokenUrl,
    clientOptions,
    termOptions,
    flowControl,
  })
  await xterm.refreshToken()
  xterm.open(container)
  xterm.connect()
}

void start()
