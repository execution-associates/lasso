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

// One live app per user (SingleInstance): a second launcher's copy exits 0,
// which a KeepAlive LaunchAgent does not restart. The parity harness and a
// fixture run draw no live rail and never take the claim.
var instance: SingleInstance?
if !args.contains("--parity") && !args.contains("--fixture") {
    instance = SingleInstance.claim()
    if instance == nil {
        FileHandle.standardError.write(Data("Lasso is already running; exiting.\n".utf8))
        exit(0)
    }
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
