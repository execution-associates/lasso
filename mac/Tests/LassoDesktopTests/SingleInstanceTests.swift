import Foundation
import Testing

@testable import LassoDesktopKit

/// A second claim on the same lock is refused while the first is held, and
/// granted again once the holder lets go.
@Test func singleInstanceRefusesASecondCopy() throws {
    let dir = FileManager.default.temporaryDirectory.appendingPathComponent("lasso-instance-\(UUID().uuidString)")
    defer { try? FileManager.default.removeItem(at: dir) }
    let path = dir.appendingPathComponent("app.lock")
    var first = SingleInstance.claim(at: path)
    #expect(first != nil)
    #expect(SingleInstance.claim(at: path) == nil)
    first = nil
    #expect(SingleInstance.claim(at: path) != nil)
}
