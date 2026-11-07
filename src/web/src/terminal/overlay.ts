// Ported from ttyd's html/src/components/terminal/xterm/addons/overlay.ts
// (MIT, Copyright (c) 2016 Shuanglei Tao), itself ported from hterm's
// showOverlay. lib/terminal.ts reads this node's text ("Press ⏎ to
// Reconnect") to press reconnect for the user, so the wording, the inline
// styles and its place as a child of terminal.element are load-bearing.
import type { ITerminalAddon, Terminal } from "@xterm/xterm"

export class OverlayAddon implements ITerminalAddon {
  terminal?: Terminal
  private readonly overlayNode: HTMLElement
  private overlayTimeout?: number

  constructor() {
    this.overlayNode = document.createElement("div")
    this.overlayNode.style.cssText = `border-radius: 15px;
font-size: xx-large;
opacity: 0.75;
padding: 0.2em 0.5em 0.2em 0.5em;
position: absolute;
-webkit-user-select: none;
-webkit-transition: opacity 180ms ease-in;
-moz-user-select: none;
-moz-transition: opacity 180ms ease-in;`
    this.overlayNode.addEventListener(
      "mousedown",
      (e) => {
        e.preventDefault()
        e.stopPropagation()
      },
      true
    )
  }

  activate(terminal: Terminal): void {
    this.terminal = terminal
  }

  dispose(): void {}

  showOverlay = (msg: string, timeout?: number): void => {
    const { terminal, overlayNode } = this
    if (!terminal?.element) return

    overlayNode.style.color = "#101010"
    overlayNode.style.backgroundColor = "#f0f0f0"
    overlayNode.textContent = msg
    overlayNode.style.opacity = "0.75"

    if (!overlayNode.parentNode) terminal.element.appendChild(overlayNode)

    const divSize = terminal.element.getBoundingClientRect()
    const overlaySize = overlayNode.getBoundingClientRect()
    overlayNode.style.top = `${(divSize.height - overlaySize.height) / 2}px`
    overlayNode.style.left = `${(divSize.width - overlaySize.width) / 2}px`

    if (this.overlayTimeout) clearTimeout(this.overlayTimeout)
    if (!timeout) return

    this.overlayTimeout = window.setTimeout(() => {
      overlayNode.style.opacity = "0"
      this.overlayTimeout = window.setTimeout(() => {
        overlayNode.parentNode?.removeChild(overlayNode)
        this.overlayTimeout = undefined
        overlayNode.style.opacity = "0.75"
      }, 200)
    }, timeout)
  }
}
