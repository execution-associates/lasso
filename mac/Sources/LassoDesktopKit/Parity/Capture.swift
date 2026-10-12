import AppKit
import SwiftUI

/// Collects anchor frames reported through ParityFramesKey.
@MainActor
final class FrameBox {
    var frames: [String: CGRect] = [:]
}

/// A rendered state: its 2x bitmap in sRGB, and its anchors' frames in
/// points (top-left origin, the root view's space).
@MainActor
public struct Capture {
    public let image: NSBitmapImageRep
    public let frames: [String: CGRect]
    public let scale: CGFloat
    let host: NSView
    /// Kept alive so the accessibility walk sees a view still in its window.
    let window: NSWindow

    /// The sRGB colour of a pixel; x and y in pixels, top-left origin. Read
    /// from the raw bytes: the bitmap holds sRGB values, but colorAt labels
    /// them generic RGB, and converting that "to sRGB" shifts every colour.
    public func pixel(_ x: Int, _ y: Int) -> RGBA {
        guard x >= 0, y >= 0, x < image.pixelsWide, y < image.pixelsHigh, let data = image.bitmapData else {
            return RGBA(r: 0, g: 0, b: 0, a: 0)
        }
        let o = y * image.bytesPerRow + x * (image.bitsPerPixel / 8)
        let a = Double(data[o + 3]) / 255
        guard a > 0 else { return RGBA(r: 0, g: 0, b: 0, a: 0) }
        let premultiplied = !image.bitmapFormat.contains(.alphaNonpremultiplied)
        func ch(_ i: Int) -> Double {
            let v = Double(data[o + i]) / 255
            return premultiplied ? min(1, v / a) : v
        }
        return RGBA(r: ch(0), g: ch(1), b: ch(2), a: a)
    }

    /// Per-channel median of the pixels inside a rect given in points.
    public func median(in r: CGRect) -> RGBA {
        let x0 = Int((r.minX * scale).rounded()), x1 = Int((r.maxX * scale).rounded())
        let y0 = Int((r.minY * scale).rounded()), y1 = Int((r.maxY * scale).rounded())
        var rs: [Double] = [], gs: [Double] = [], bs: [Double] = [], as_: [Double] = []
        for y in y0..<max(y1, y0 + 1) {
            for x in x0..<max(x1, x0 + 1) {
                let p = pixel(x, y)
                rs.append(p.r)
                gs.append(p.g)
                bs.append(p.b)
                as_.append(p.a)
            }
        }
        func med(_ v: [Double]) -> Double {
            let s = v.sorted()
            return s.isEmpty ? 0 : s[s.count / 2]
        }
        return RGBA(r: med(rs), g: med(gs), b: med(bs), a: med(as_))
    }

    /// Pixels along one row (y in points) between two x positions in points.
    public func row(y: CGFloat, from x0: CGFloat, to x1: CGFloat) -> [RGBA] {
        let py = Int((y * scale).rounded(.down))
        return (Int((x0 * scale).rounded(.down))..<Int((x1 * scale).rounded(.up))).map { pixel($0, py) }
    }
}

public enum Capturer {
    /// Mount a view in an offscreen borderless window drawing in sRGB, let
    /// SwiftUI lay it out, and cache its display into a bitmap.
    @MainActor
    public static func capture<V: View>(_ view: V, size: CGSize) async throws -> Capture {
        let box = FrameBox()
        // The capture is a Retina one whatever the Mac's screen: minime's is
        // 1x, and SwiftUI rasterises an image (a Lucide icon's SVG) at the
        // display scale it is given, so at 1x the 2x bitmap below would hold
        // that raster upscaled, its strokes soft and short of their colour.
        let scale: CGFloat = 2
        let root = view
            .environment(\.parityEnabled, true)
            .environment(\.displayScale, scale)
            .coordinateSpace(name: ParityRoot.space)
            .frame(width: size.width, height: size.height, alignment: .topLeading)
            .onPreferenceChange(ParityFramesKey.self) { v in
                MainActor.assumeIsolated { box.frames = v }
            }
        let host = NSHostingView(rootView: root)
        host.frame = NSRect(origin: .zero, size: size)
        let window = NSWindow(
            contentRect: NSRect(x: -10000, y: -10000, width: size.width, height: size.height),
            styleMask: .borderless, backing: .buffered, defer: false)
        window.colorSpace = .sRGB
        window.isOpaque = false
        window.backgroundColor = .clear
        window.isReleasedWhenClosed = false
        window.contentView = host
        // Ordered in (off every screen): SwiftUI builds its accessibility
        // tree only for a window that is on the window list.
        window.orderFrontRegardless()
        host.layoutSubtreeIfNeeded()
        try await Task.sleep(nanoseconds: 400_000_000)
        host.layoutSubtreeIfNeeded()
        // Draw into a bitmap of our own, tagged sRGB at exactly 2x, rather
        // than bitmapImageRepForCachingDisplay's: that one carries the
        // screen's colour space, and reading it back as sRGB shifted every
        // colour.
        guard let blank = NSBitmapImageRep(
            bitmapDataPlanes: nil, pixelsWide: Int(size.width * scale), pixelsHigh: Int(size.height * scale),
            bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false, colorSpaceName: .calibratedRGB,
            bytesPerRow: 0, bitsPerPixel: 0),
            let rep = blank.retagging(with: .sRGB)
        else {
            throw ParityError.capture("no bitmap for the view")
        }
        rep.size = size
        host.cacheDisplay(in: host.bounds, to: rep)
        let srgb = rep
        return Capture(image: srgb, frames: box.frames, scale: scale, host: host, window: window)
    }
}

public enum ParityError: Error, CustomStringConvertible {
    case capture(String)

    public var description: String {
        switch self {
        case .capture(let m): "capture failed: \(m)"
        }
    }
}
