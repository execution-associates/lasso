import AppKit

/// The paperclip's file chooser: any files, several at once. The app is an
/// accessory with no Dock icon, so it comes forward first for the panel to.
@MainActor
enum FilePicker {
    static func choose() -> [URL] {
        let panel = NSOpenPanel()
        panel.allowsMultipleSelection = true
        panel.canChooseDirectories = false
        panel.message = "Choose files to attach: each is copied to the agent's machine and its path goes in your message"
        NSApp.activate(ignoringOtherApps: true)
        return panel.runModal() == .OK ? panel.urls : []
    }
}
