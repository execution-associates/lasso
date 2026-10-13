import AppKit
import SwiftUI

/// The app's icons are Lucide's (lucide.dev), the set lasso's web app draws
/// through lucide-react, so a thing wears the same icon in both: Bots is
/// `bot`, the inbox `inbox`, a terminal `square-terminal`. Each is an SVG in
/// Resources/Icons, written by mac/Scripts/lucide-icons.sh from the web's own
/// pinned lucide-react, with Lucide's ISC license beside them, and drawn as a
/// template image tinted by the foreground style. A case is the only way to
/// name one, one per line (the script reads them from here), and IconTests
/// holds every case to a bundled SVG. The window controls are the exception
/// (`WindowControl`).
public enum Lucide: String, CaseIterable, Sendable {
    case arrowRight = "arrow-right"
    case arrowUp = "arrow-up"
    case asterisk
    case audioLines = "audio-lines"
    case bell
    case bot
    case brain
    case check
    case chevronDown = "chevron-down"
    case chevronRight = "chevron-right"
    case chevronUp = "chevron-up"
    case clock
    case codeXml = "code-xml"
    case cpu
    case download
    case ellipsis
    case externalLink = "external-link"
    case file
    case fileCode = "file-code"
    case fileText = "file-text"
    case film
    case folder
    case folderGit2 = "folder-git-2"
    case gitBranch = "git-branch"
    case globe
    case image
    case inbox
    case info
    case keyRound = "key-round"
    case keyboard
    case laptop
    case listTodo = "list-todo"
    case link
    case lock
    case mail
    case messageSquare = "message-square"
    case network
    case paperclip
    case pencil
    case pause
    case phone
    case pi
    case pin
    case play
    case plus
    case refreshCw = "refresh-cw"
    case search
    case server
    case settings
    case shield
    case sparkles
    case square
    case squareTerminal = "square-terminal"
    case tag
    case triangleAlert = "triangle-alert"
    case user
    case users
    case wrench
    case x

    /// What VoiceOver says when the control around it gives nothing better,
    /// as SF Symbols name theirs.
    public var spoken: String { rawValue.replacingOccurrences(of: "-", with: " ") }
}

/// The bundled SVGs, loaded once per size, stroke and fill.
@MainActor
public enum LucideAssets {
    /// The bundle's Resources/Icons; tests point it at the package's.
    public static var directory: URL? = Bundle.main.resourceURL?.appendingPathComponent("Icons") {
        didSet { cache.removeAll() }
    }

    private static var cache: [String: NSImage] = [:]

    /// The side of the box an icon is drawn in for a glyph `size` points
    /// across. Lucide draws on a 24-unit grid inside a 2-unit margin, so the
    /// glyph spans what an SF Symbol at that point size did, and the sizes in
    /// Theme keep their meaning.
    public static func box(_ size: CGFloat) -> CGFloat {
        size * Theme.lucide.number("grid") / Theme.lucide.number("live")
    }

    /// The SVG's text, or nil when the bundle lacks it.
    public static func svg(_ icon: Lucide) -> String? {
        guard let url = directory?.appendingPathComponent("\(icon.rawValue).svg") else { return nil }
        return try? String(contentsOf: url, encoding: .utf8)
    }

    /// A template image `box(size)` points square. `stroke` is in grid units
    /// (Lucide's 2 by default; the web thickens a few small or emphatic ones);
    /// `filled` fills the shapes as the web's Stop does (`fill="currentColor"`).
    public static func image(_ icon: Lucide, size: CGFloat, stroke: Double? = nil, filled: Bool = false) -> NSImage {
        let side = box(size)
        let width = stroke ?? Double(Theme.lucide.number("stroke"))
        let key = "\(icon.rawValue) \(side) \(width) \(filled)"
        if let hit = cache[key] { return hit }
        var image = NSImage(size: NSSize(width: side, height: side))
        if var text = svg(icon) {
            text = text.replacingOccurrences(of: "stroke-width=\"2\"", with: "stroke-width=\"\(width)\"")
            if filled { text = text.replacingOccurrences(of: "fill=\"none\"", with: "fill=\"currentColor\"") }
            if let loaded = NSImage(data: Data(text.utf8)) {
                image = loaded
                image.size = NSSize(width: side, height: side)
            }
        }
        image.isTemplate = true
        image.accessibilityDescription = icon.spoken
        cache[key] = image
        return image
    }
}

/// A Lucide icon whose glyph spans `size` points, tinted by the foreground
/// style. Where SwiftUI wants an `Image` itself (a menu's items and label),
/// use `LucideIcon.image`.
public struct LucideIcon: View {
    let icon: Lucide
    let size: CGFloat
    var stroke: Double?
    var filled = false

    public init(_ icon: Lucide, size: CGFloat, stroke: Double? = nil, filled: Bool = false) {
        self.icon = icon
        self.size = size
        self.stroke = stroke
        self.filled = filled
    }

    public var body: some View {
        Self.image(icon, size: size, stroke: stroke, filled: filled)
            .accessibilityLabel(icon.spoken)
    }

    public static func image(_ icon: Lucide, size: CGFloat, stroke: Double? = nil, filled: Bool = false) -> Image {
        Image(nsImage: LucideAssets.image(icon, size: size, stroke: stroke, filled: filled)).renderingMode(.template)
    }
}

/// The controls that work a window from its header stay SF Symbols, so they
/// feel like the system's own: close, minimise, expand and the resize grip.
/// This is the one place the app names an SF Symbol (IconTests holds it to that).
enum WindowControl: String {
    case close = "xmark"
    case minimize = "minus"
    case expand = "arrow.up.left.and.arrow.down.right"
    case resize = "line.diagonal"

    func image(size: CGFloat, weight: Font.Weight) -> some View {
        Image(systemName: rawValue).font(.system(size: size, weight: weight))
    }
}

/// What a round or header button shows: a Lucide icon, or a window control.
enum Glyph {
    case lucide(Lucide, stroke: Double? = nil, filled: Bool = false)
    case window(WindowControl)

    @ViewBuilder func view(size: CGFloat, weight: Font.Weight) -> some View {
        switch self {
        case .lucide(let icon, let stroke, let filled): LucideIcon(icon, size: size, stroke: stroke, filled: filled)
        case .window(let c): c.image(size: size, weight: weight)
        }
    }
}

extension Label where Title == Text, Icon == Image {
    /// A label with a Lucide icon, as `Label(_:systemImage:)` makes one with
    /// an SF Symbol; an `Image`, so it also works in a menu.
    init(_ title: String, lucide icon: Lucide, size: CGFloat) {
        self.init { Text(title) } icon: { LucideIcon.image(icon, size: size) }
    }
}
