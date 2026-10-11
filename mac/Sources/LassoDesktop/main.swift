import AppKit
import LassoDesktopKit

// An accessory app: no Dock icon or menu bar (PLAN Q2); the rail is the UI.
let app = NSApplication.shared
let delegate = AppDelegate(arguments: CommandLine.arguments)
app.delegate = delegate
app.setActivationPolicy(.accessory)
app.run()
