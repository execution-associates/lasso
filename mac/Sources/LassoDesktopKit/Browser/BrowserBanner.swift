import AppKit
import Combine
import SwiftUI

// An agent opened a page for the human (PLAN 24): the rail's Browser button
// pulses and a banner beside the rail offers it. Nothing opens by itself, and
// neither takes the keyboard.

/// The page an agent offered that the human has not taken or waved away.
@MainActor
public final class BrowserAttention: ObservableObject {
    /// The offer: the rail's Browser button pulses while there is one.
    @Published public private(set) var request: BrowserOpen?
    /// The banner is up.
    @Published public private(set) var bannerShown = false
    /// The rail was revealed (the pulse in view) since the banner went.
    private var seen = false

    public init() {}

    public var pulsing: Bool { request != nil }

    /// An agent opened a page: a newer one replaces an older one.
    public func arrived(_ o: BrowserOpen) {
        request = o
        bannerShown = true
        seen = false
    }

    /// The banner went on its own: the pulse stays until it is seen.
    public func bannerLingered() { bannerShown = false }

    /// Open or Dismiss: nothing is waiting any more.
    public func clear() {
        request = nil
        bannerShown = false
        seen = false
    }

    /// The rail opened or closed. A pulse left by a banner nobody answered
    /// stops once the rail has shown it and closes again.
    public func rail(expanded: Bool) {
        guard request != nil, !bannerShown else { return }
        if expanded { seen = true } else if seen { clear() }
    }
}

/// The banner: who opened which page, and Dismiss / Open.
struct BrowserBannerView: View {
    @ObservedObject var attention: BrowserAttention
    var onOpen: () -> Void = {}
    var onDismiss: () -> Void = {}

    var body: some View {
        let o = attention.request
        VStack(alignment: .leading, spacing: Theme.space("m")) {
            HStack(alignment: .top, spacing: Theme.space("m")) {
                Image(systemName: "globe")
                    .font(.system(size: Theme.Rail.icon.number("size"), weight: .regular))
                    .foregroundStyle(Theme.accent.color)
                    .parityAnchor("banner.icon")
                VStack(alignment: .leading, spacing: Theme.space("xs")) {
                    Text("\(o?.who ?? "An agent") opened a page for you")
                        .font(Theme.Fonts.label.font)
                        .foregroundStyle(Theme.Text.primary.color)
                        .lineLimit(1)
                    Text(o?.shownURL ?? "")
                        .font(Theme.Fonts.small.font)
                        .foregroundStyle(Theme.Text.secondary.color)
                        .lineLimit(1)
                        .truncationMode(.middle)
                        .parityAnchor("banner.url")
                }
                Spacer(minLength: 0)
            }
            HStack(spacing: Theme.space("m")) {
                Spacer(minLength: 0)
                PillButton(title: "Dismiss", label: "Dismiss: leave the page where it is") { onDismiss() }
                    .parityAnchor("banner.dismiss")
                PillButton(title: "Open", primary: true, label: "Open the page in the Browser window") { onOpen() }
                    .parityAnchor("banner.open")
            }
        }
        .padding(Theme.Panel.inset.pt)
        .frame(width: Theme.Browser.banner.size2("size").width, height: Theme.Browser.banner.size2("size").height, alignment: .topLeading)
        .background(RoundedRectangle(cornerRadius: Theme.Panel.radius.pt, style: .continuous).fill(Theme.Panel.surface.color))
        .overlay(
            RoundedRectangle(cornerRadius: Theme.Panel.radius.pt, style: .continuous)
                .strokeBorder(Theme.Panel.border.color, lineWidth: 1))
        .parityAnchor("banner")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("\(o?.who ?? "An agent") opened a page for you: \(o?.shownURL ?? "")")
    }
}

/// A panel that is never key: the banner's buttons take a click without
/// taking the keyboard from whatever has it.
final class BannerPanel: RailPanel {
    override var canBecomeKey: Bool { false }
}

/// The banner beside the rail.
@MainActor
final class BrowserBanner {
    let panel: BannerPanel
    private let attention: BrowserAttention
    var railFrame: () -> NSRect = { .zero }
    private var linger: Task<Void, Never>?

    init(attention: BrowserAttention, onOpen: @escaping () -> Void, onDismiss: @escaping () -> Void) {
        self.attention = attention
        let size = Theme.Browser.banner.size2("size")
        let host = NSHostingView(rootView: BrowserBannerView(attention: attention, onOpen: onOpen, onDismiss: onDismiss))
        host.frame = NSRect(origin: .zero, size: size)
        panel = BannerPanel(content: host, size: size)
        panel.hasShadow = true
    }

    var isShown: Bool { panel.isVisible }

    /// Up beside the rail, without the keyboard; it goes by itself after
    /// the token's linger.
    func show() {
        panel.setFrameOrigin(originBesideRail(railFrame(), size: panel.frame.size))
        panel.orderFrontRegardless()
        linger?.cancel()
        let seconds = Theme.Browser.banner.number("linger")
        linger = Task { @MainActor [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(seconds * 1e9))
            guard let self, !Task.isCancelled else { return }
            self.panel.orderOut(nil)
            self.attention.bannerLingered()
        }
    }

    func follow() { if isShown { panel.setFrameOrigin(originBesideRail(railFrame(), size: panel.frame.size)) } }

    func hide() {
        linger?.cancel()
        linger = nil
        panel.orderOut(nil)
    }
}
