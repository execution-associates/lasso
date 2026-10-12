import Foundation
import Testing

@testable import LassoDesktopKit

@MainActor
private final class FakeLoginItem: LoginItemService {
    var status: LoginItemStatus
    var calls: [String] = []
    var refuse = false
    init(_ status: LoginItemStatus = .notRegistered) { self.status = status }

    struct Refused: Error, LocalizedError {
        var errorDescription: String? { "Operation not permitted" }
    }

    func register() throws {
        calls.append("register")
        if refuse { throw Refused() }
        status = .enabled
    }

    func unregister() throws {
        calls.append("unregister")
        status = .notRegistered
    }

    func openSystemSettings() { calls.append("open") }
}

private func scratchDefaults() -> UserDefaults {
    UserDefaults(suiteName: "lasso-login-\(UUID().uuidString)")!
}

/// Onboarding registers the app once; turning it off afterwards sticks.
@MainActor @Test func loginItemRegistersOnceAndRespectsOff() {
    let svc = FakeLoginItem()
    let items = LoginItems(service: svc, defaults: scratchDefaults(), devBuild: false)
    #expect(items.state == .off)
    #expect(items.autoRegisterOnce() == nil)
    #expect(svc.calls == ["register"] && items.state == .on)
    items.autoRegisterOnce()
    #expect(svc.calls == ["register"])
    #expect(items.set(false) == nil)
    #expect(items.state == .off && !items.state.startsAtLogin)
    // Another onboarding (a reset Mac connecting again) does not turn it back on.
    items.autoRegisterOnce()
    #expect(svc.calls == ["register", "unregister"] && items.state == .off)
}

/// Turned off by hand before onboarding ever finished: never auto-registered.
@MainActor @Test func aHandChoiceBeatsTheAutomaticOne() {
    let svc = FakeLoginItem()
    let items = LoginItems(service: svc, defaults: scratchDefaults(), devBuild: false)
    items.set(false)
    items.autoRegisterOnce()
    #expect(svc.calls == ["unregister"] && items.state == .off)
}

/// A dev build is started by the dev LaunchAgent: it never touches SMAppService.
@MainActor @Test func aDevBuildNeverRegisters() {
    let svc = FakeLoginItem()
    let items = LoginItems(service: svc, defaults: scratchDefaults(), devBuild: true)
    items.autoRegisterOnce()
    items.set(true)
    #expect(svc.calls.isEmpty && items.state == .devAgent && items.state.startsAtLogin)
}

@MainActor @Test func approvalAndRefusalAreShown() {
    let svc = FakeLoginItem(.requiresApproval)
    let m = SettingsModel(permissions: FixedPermissions([:]), baseURL: { "http://127.0.0.1:18090" })
    m.loginItems = LoginItems(service: svc, defaults: scratchDefaults(), devBuild: false)
    #expect(m.loginItem == .needsApproval && m.appFootnote?.contains("Login Items") == true)
    m.openLoginItems()
    #expect(svc.calls == ["open"])
    m.setStartAtLogin(false)
    #expect(m.loginItem == .off)
    svc.refuse = true
    m.setStartAtLogin(true)
    #expect(m.loginItem == .off && m.loginNote == "Could not turn Start at login on: Operation not permitted")
    // Changed in System Settings while Settings was closed: read again on show.
    svc.status = .enabled
    m.refresh()
    #expect(m.loginItem == .on)
}
