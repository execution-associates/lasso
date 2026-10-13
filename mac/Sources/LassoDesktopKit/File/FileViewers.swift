import AVKit
import AppKit
import PDFKit
import Quartz

// The Mac's own viewers for what lasso's page does not show (PLAN 26): an
// image, audio and video, a PDF, and QuickLook for the rest (Word, Excel,
// Keynote, Pages, a 3D scene). Each shows a downloaded copy (FileCache), and
// each keeps its place when the file changes and the new copy comes in: the
// zoom and scroll, the playhead and whether it played, the page and scale.
// They are view-only.

@MainActor
protocol FileViewer: AnyObject {
    var view: NSView { get }
    /// Show `url`, where the last one was (the first: from the start).
    func show(_ url: URL)
    /// The window is closing: stop playing, let go of the file.
    func close()
}

@MainActor
enum FileViewers {
    static func make(_ kind: FileKind) -> FileViewer {
        switch kind {
        case .image: ImageViewer()
        case .av: MediaViewer()
        case .pdf: PDFViewer()
        default: QuickLookViewer()
        }
    }
}

/// An image, scrolled and pinch-zoomed; an animated GIF plays.
@MainActor
final class ImageViewer: FileViewer {
    let scroll = NSScrollView()
    let image = NSImageView()
    private var shown = false

    var view: NSView { scroll }

    init() {
        scroll.contentView = CenteringClipView()
        scroll.hasVerticalScroller = true
        scroll.hasHorizontalScroller = true
        scroll.autohidesScrollers = true
        scroll.allowsMagnification = true
        scroll.minMagnification = 0.05
        scroll.maxMagnification = 16
        scroll.drawsBackground = true
        scroll.backgroundColor = Theme.Panel.surface.nsColor
        image.imageScaling = .scaleProportionallyUpOrDown
        image.animates = true
        image.isEditable = false
        scroll.documentView = image
        image.setAccessibilityLabel("Image")
    }

    func show(_ url: URL) {
        guard let img = NSImage(contentsOf: url) else { return }
        let size = img.size
        if shown {
            // Kept: the zoom and what was in view.
            let zoom = scroll.magnification
            let origin = scroll.contentView.bounds.origin
            image.image = img
            image.frame = NSRect(origin: .zero, size: size)
            scroll.magnification = zoom
            scroll.contentView.scroll(to: origin)
            scroll.reflectScrolledClipView(scroll.contentView)
            return
        }
        shown = true
        image.image = img
        image.frame = NSRect(origin: .zero, size: size)
        // A picture larger than the window opens fitted; a small one at its size.
        let box = scroll.contentSize
        if size.width > 0, size.height > 0, box.width > 0, box.height > 0 {
            scroll.magnification = min(1, box.width / size.width, box.height / size.height)
        }
    }

    func close() { image.image = nil }
}

/// A picture smaller than the window sits in its middle, not its corner.
final class CenteringClipView: NSClipView {
    override func constrainBoundsRect(_ proposed: NSRect) -> NSRect {
        var r = super.constrainBoundsRect(proposed)
        guard let doc = documentView else { return r }
        if r.width > doc.frame.width { r.origin.x = (doc.frame.width - r.width) / 2 }
        if r.height > doc.frame.height { r.origin.y = (doc.frame.height - r.height) / 2 }
        return r
    }
}

/// Audio or video, with the system's controls and no full-screen button
/// (no Lasso window goes full screen, PLAN 16).
@MainActor
final class MediaViewer: FileViewer {
    let player = AVPlayerView()

    var view: NSView { player }

    init() {
        player.controlsStyle = .inline
        player.showsFullScreenToggleButton = false
        player.allowsPictureInPicturePlayback = false
        player.setAccessibilityLabel("Player")
    }

    func show(_ url: URL) {
        guard let p = player.player else {
            player.player = AVPlayer(url: url)
            return
        }
        // Kept: where it was and whether it was playing.
        let at = p.currentTime()
        let playing = p.rate != 0
        p.replaceCurrentItem(with: AVPlayerItem(url: url))
        p.seek(to: at, toleranceBefore: .zero, toleranceAfter: .zero)
        if playing { p.play() }
    }

    func close() {
        player.player?.pause()
        player.player = nil
    }
}

/// A PDF.
@MainActor
final class PDFViewer: FileViewer {
    let pdf = PDFView()

    var view: NSView { pdf }

    init() {
        pdf.autoScales = true
        pdf.displayMode = .singlePageContinuous
        pdf.backgroundColor = Theme.Panel.surface.nsColor
    }

    func show(_ url: URL) {
        guard let doc = PDFDocument(url: url) else { return }
        guard let old = pdf.document else {
            pdf.document = doc
            return
        }
        // Kept: the page in view and the scale.
        let page = pdf.currentPage.map { old.index(for: $0) } ?? 0
        let scale = pdf.scaleFactor
        let auto = pdf.autoScales
        pdf.document = doc
        if !auto { pdf.scaleFactor = scale }
        if let p = doc.page(at: min(page, max(0, doc.pageCount - 1))) { pdf.go(to: p) }
    }

    func close() { pdf.document = nil }
}

/// Everything else QuickLook can show.
@MainActor
final class QuickLookViewer: FileViewer {
    let preview: QLPreviewView

    var view: NSView { preview }

    init() {
        preview = QLPreviewView(frame: .zero, style: .normal)!
        preview.shouldCloseWithWindow = false
        preview.autostarts = false
    }

    func show(_ url: URL) { preview.previewItem = url as NSURL }

    func close() {
        preview.previewItem = nil
        preview.close()
    }
}
