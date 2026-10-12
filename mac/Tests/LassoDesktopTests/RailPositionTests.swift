import AppKit
import Foundation
import Testing

@testable import LassoDesktopKit

/// The rail's place along the edge: a fraction of its travel, 0.5 (nothing
/// stored) the vertical middle, and "centred" means within a point of it.
@Test func railPositionIsAFractionOfItsTravel() {
    let v = NSRect(x: 0, y: 40, width: 1440, height: 800)
    #expect(RailController.y(0.5, visible: v, height: 300) == 290)
    #expect(RailController.y(0, visible: v, height: 300) == 540)
    #expect(RailController.y(1, visible: v, height: 300) == 40)
    #expect(RailController.y(7, visible: v, height: 300) == 40)
    // A screen shorter than the rail: it hangs from the top.
    #expect(RailController.y(0.5, visible: NSRect(x: 0, y: 0, width: 800, height: 200), height: 300) == -100)
    #expect(RailController.isCentred(290, visible: v, height: 300))
    #expect(RailController.isCentred(291, visible: v, height: 300))
    #expect(!RailController.isCentred(293, visible: v, height: 300))
}

/// Center vertically forgets the dragged place, as a fresh install has none,
/// and puts the rail in the middle of its screen's edge; Settings then reads
/// it as centred and greys the button.
@MainActor @Test func centreVerticallyPutsTheRailBack() {
    let d = UserDefaults.standard
    let saved = d.object(forKey: RailController.positionKey)
    defer { if let saved { d.set(saved, forKey: RailController.positionKey) } else { d.removeObject(forKey: RailController.positionKey) } }
    d.set(0.1, forKey: RailController.positionKey)
    let rail = RailController(model: RailModel(fleet: FleetModel()))
    defer { rail.panel.orderOut(nil) }
    #expect(!rail.isCentred)
    var moved = 0
    rail.onMoved = { moved += 1 }
    let settings = SettingsModel(permissions: FixedPermissions([:]), baseURL: { "" }, version: { nil }, usableHosts: { nil })
    settings.isRailCentred = { rail.isCentred }
    settings.centreRail = { rail.centre() }
    settings.refresh()
    #expect(!settings.railCentred)
    settings.centreRailNow()
    #expect(d.object(forKey: RailController.positionKey) == nil)
    #expect(rail.isCentred && settings.railCentred && moved == 1)
}
