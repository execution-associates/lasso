// App commands sent by the input dial inside a terminal iframe. Dispatched on
// the PARENT's window — this module runs in the parent realm, so App's listener
// hears a dial in either iframe (herdr's terminal and the sidebar's shell).
export const MOBILE_COMMAND_EVENT = "lasso:mobile-command"

// "views" opens the view picker, which is where every other phone command
// (New, Search, Host, Sidebar) lives.
export type MobileCommand = "views"

export function emitMobileCommand(command: MobileCommand): void {
  window.dispatchEvent(
    new CustomEvent<MobileCommand>(MOBILE_COMMAND_EVENT, { detail: command })
  )
}
