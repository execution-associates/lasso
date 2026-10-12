import AppKit
import Foundation
import Testing

@testable import LassoDesktopKit

// The app's icons are the web's Lucide ones, bundled as SVGs
// (Resources/Icons, Theme/Lucide.swift). Nothing may fall back at runtime.

private let packageDir = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
    .deletingLastPathComponent()
private let iconsDir = packageDir.appendingPathComponent("Resources/Icons")

/// Opaque pixels when drawn at 2x: zero means it drew nothing.
@MainActor private func inked(_ image: NSImage) -> Int {
    let side = Int((image.size.width * 2).rounded(.up))
    let rep = NSBitmapImageRep(
        bitmapDataPlanes: nil, pixelsWide: side, pixelsHigh: side, bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true,
        isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
    image.draw(in: NSRect(x: 0, y: 0, width: side, height: side))
    NSGraphicsContext.restoreGraphicsState()
    var n = 0
    for y in 0..<side {
        for x in 0..<side where (rep.colorAt(x: x, y: y)?.alphaComponent ?? 0) > 0.5 { n += 1 }
    }
    return n
}

/// Every icon the code can name (a case of `Lucide`, the only way to name
/// one) has its SVG in the bundle, and the bundle holds no other.
@Test func everyIconTheCodeNamesIsBundled() throws {
    let files = Set(try FileManager.default.contentsOfDirectory(atPath: iconsDir.path).filter { $0.hasSuffix(".svg") })
    let named = Set(Lucide.allCases.map { "\($0.rawValue).svg" })
    #expect(named.subtracting(files).isEmpty, "missing: \(named.subtracting(files).sorted())")
    #expect(files.subtracting(named).isEmpty, "not in enum Lucide: \(files.subtracting(named).sorted())")
    let license = try String(contentsOf: iconsDir.appendingPathComponent("LICENSE"), encoding: .utf8)
    #expect(license.hasPrefix("ISC License") && license.contains("Lucide"))
}

/// Each is Lucide's 24-unit drawing, loads as an SVG, and comes out a
/// template image at the size asked that draws something.
@MainActor @Test func everyIconLoadsAsATemplateImage() throws {
    LucideAssets.directory = iconsDir
    defer { LucideAssets.directory = Bundle.main.resourceURL?.appendingPathComponent("Icons") }
    let size: CGFloat = 17
    let box = LucideAssets.box(size)
    #expect(abs(box - 20.4) < 0.001)
    for icon in Lucide.allCases {
        let svg = try #require(LucideAssets.svg(icon), "\(icon.rawValue)")
        #expect(svg.contains(#"viewBox="0 0 24 24""#) && svg.contains(#"stroke="currentColor""#), "\(icon.rawValue)")
        #expect(svg.contains(#"stroke-width="2""#) && svg.contains(#"fill="none""#), "\(icon.rawValue)")
        let raw = try #require(NSImage(data: Data(svg.utf8)), "\(icon.rawValue)")
        #expect(raw.size == NSSize(width: 24, height: 24), "\(icon.rawValue)")
        let image = LucideAssets.image(icon, size: size)
        #expect(image.isTemplate, "\(icon.rawValue)")
        #expect(image.size == NSSize(width: box, height: box), "\(icon.rawValue)")
        #expect(inked(image) > 0, "\(icon.rawValue) draws nothing")
        #expect(image.accessibilityDescription == icon.spoken)
    }
    // The variants the web uses: a filled Stop, a bolder send arrow.
    #expect(inked(LucideAssets.image(.square, size: size, filled: true)) > inked(LucideAssets.image(.square, size: size)))
    #expect(inked(LucideAssets.image(.arrowUp, size: size, stroke: 2.5)) > inked(LucideAssets.image(.arrowUp, size: size)))
}

/// SF Symbols are only the window controls (`WindowControl`), named in
/// Theme/Lucide.swift alone; every other icon is Lucide's.
@Test func sfSymbolsAreOnlyTheWindowControls() throws {
    let sources = packageDir.appendingPathComponent("Sources")
    let files = try #require(FileManager.default.enumerator(at: sources, includingPropertiesForKeys: nil))
    var offenders: [String] = []
    for case let url as URL in files where url.pathExtension == "swift" && url.lastPathComponent != "Lucide.swift" {
        let text = try String(contentsOf: url, encoding: .utf8)
        for needle in ["systemName:", "systemImage:", "systemSymbolName"] where text.contains(needle) {
            offenders.append("\(url.lastPathComponent): \(needle)")
        }
    }
    #expect(offenders.isEmpty, "\(offenders)")
}

/// The web's icons for the same things.
@MainActor @Test func theRailAndEnvelopesWearTheWebsIcons() {
    #expect(Lucide.agent("claude") == .asterisk && Lucide.agent("codex") == .codeXml && Lucide.agent("omp") == .pi)
    #expect(Lucide.agent("") == .squareTerminal)
    #expect(Lucide.toolFamily("shell") == .squareTerminal && Lucide.toolFamily("edit") == .pencil && Lucide.toolFamily(nil) == .wrench)
    #expect(FirstRunModel.examples.map(\.icon) == [.network, .shield, .laptop])
}
