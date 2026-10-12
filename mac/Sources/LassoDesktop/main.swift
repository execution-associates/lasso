import AppKit
import LassoDesktopKit
import Sparkle

let args = CommandLine.arguments

// --smoke needs no window server, so it runs before NSApplication exists and
// works from an ssh session.
if let i = args.firstIndex(of: "--smoke") {
    let seconds = i + 1 < args.count ? Double(args[i + 1]) ?? 30 : 30
    Task { exit(await Smoke.run(seconds: seconds)) }
    dispatchMain()
}

if args.contains("--dump-tokens") {
    FileHandle.standardOutput.write(Theme.dumpTokens())
    exit(0)
}

// An accessory app: no Dock icon or menu bar (PLAN Q2); the rail is the UI.
let app = NSApplication.shared
let delegate = AppDelegate(arguments: args)
app.delegate = delegate
app.setActivationPolicy(.accessory)
// Updates (Sparkle), only in a release build: a dev build names no feed.
let updater: SPUStandardUpdaterController? =
    AppUpdates.configured ? SPUStandardUpdaterController(startingUpdater: true, updaterDelegate: nil, userDriverDelegate: nil) : nil
if let updater {
    AppUpdates.checkNow = {
        NSApp.activate(ignoringOtherApps: true)
        updater.checkForUpdates(nil)
    }
}
app.run()
