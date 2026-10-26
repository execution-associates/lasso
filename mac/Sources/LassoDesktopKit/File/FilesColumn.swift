import AppKit
import Combine
import SwiftUI

// The Files column (PLAN 27): the rail's Files button opens it on a hover,
// as the Bots button opens theirs. A row per file agents opened here, newest
// first under Today, Yesterday and Earlier; resting on one previews it to the
// column's left (FilePreview), a click opens its window (PLAN 26), and a
// right-click offers Open, Download, Hide from list and Delete. Typing
// filters, as in the agents column.

/// What a row says under its file for a while: an action under way, done,
/// or refused.
public enum FileRowNote: Equatable, Sendable {
    case busy(String)
    case done(String)
    case failed(String)

    var text: String {
        switch self {
        case .busy(let s), .done(let s), .failed(let s): s
        }
    }
}

/// Why a file was not deleted.
enum RecentFileError: LocalizedError {
    case folder
    case offline

    var errorDescription: String? {
        switch self {
        case .folder: "it is a folder now; delete it yourself"
        case .offline: "not connected to a lasso"
        }
    }
}

@MainActor
public final class FilesListModel: ObservableObject, HoverColumn {
    public let recent: RecentFiles
    @Published public var selected: String?
    @Published public var hovered: String?
    @Published public var keyboardLit = false
    @Published public var filter = ""
    /// The filter's field, for typing from anywhere in the column.
    var filterField: NSTextField?
    /// The row asking "Delete … ?" in place of itself.
    @Published public var confirmDelete: String?
    @Published public var notes: [String: FileRowNote] = [:]
    /// The files whose window is open now (their dot).
    @Published public var openWindows: Set<String> = []
    public var rowFrames: [String: CGRect] = [:] {
        didSet { if rowFrames != oldValue { rowFramesChanged() } }
    }
    public var rowFramesChanged: () -> Void = {}

    public var onOpen: (RecentFile) -> Void = { _ in }
    /// Into ~/Downloads; answers where it went.
    var download: (RecentFile) async throws -> URL = { _ in throw RecentFileError.offline }
    /// Off its machine, then off the list.
    var delete: (RecentFile) async throws -> Void = { _ in throw RecentFileError.offline }
    /// Its window has unsaved edits (the confirm says so).
    var isDirty: (String) -> Bool = { _ in false }
    /// A machine's name ("local" is lasso's own hostname).
    var hostLabel: (String) -> String = { $0 }
    /// A file left the list (its preview's copy can go).
    var onForget: (RecentFile) -> Void = { _ in }
    public var onClose: () -> Void = {}
    public let now: () -> Date
    private var held = FrozenOrder()
    private var sub: AnyCancellable?

    public init(recent: RecentFiles, now: @escaping () -> Date = Date.init) {
        self.recent = recent
        self.now = now
        sub = recent.objectWillChange.sink { [weak self] in self?.objectWillChange.send() }
    }

    /// The listed files in the order drawn: held while the column is open,
    /// so a file arriving under a resting pointer does not move the rows.
    public var files: [RecentFile] { held.apply(recent.items, key: \.id).filter(matches) }

    /// Every word typed must appear in the row somewhere: its name, path,
    /// machine or agent.
    func matches(_ f: RecentFile) -> Bool {
        let words = filter.lowercased().split(whereSeparator: \.isWhitespace)
        guard !words.isEmpty else { return true }
        let hay = "\(f.path) \(hostLabel(f.host)) \(f.from ?? "")".lowercased()
        return words.allSatisfy { hay.contains($0) }
    }

    public struct Section: Identifiable {
        public var title: String
        public var files: [RecentFile]
        public var id: String { title }
    }

    /// Today, Yesterday and Earlier, by when each was last opened.
    public var sections: [Section] {
        let cal = Calendar.current
        let today = now()
        let yesterday = cal.date(byAdding: .day, value: -1, to: today) ?? today
        var out: [Section] = ["Today", "Yesterday", "Earlier"].map { Section(title: $0, files: []) }
        for f in files {
            let i = cal.isDate(f.at, inSameDayAs: today) ? 0 : cal.isDate(f.at, inSameDayAs: yesterday) ? 1 : 2
            out[i].files.append(f)
        }
        return out.filter { !$0.files.isEmpty }
    }

    /// The rows top to bottom, as drawn (sections included).
    var drawn: [RecentFile] { sections.flatMap(\.files) }

    public func freezeOrder() { held.freeze(recent.items.map(\.id)) }
    public func thawOrder() {
        held.thaw()
        objectWillChange.send()
    }

    /// ↑/↓ move the selection.
    public func move(_ delta: Int) {
        let list = drawn
        guard !list.isEmpty else { return }
        let i = list.firstIndex { $0.id == selected } ?? (delta > 0 ? -1 : list.count)
        selected = list[max(0, min(list.count - 1, i + delta))].id
    }

    /// ⏎: the selected file, else the first match.
    public func openSelected() {
        guard let f = drawn.first(where: { $0.id == selected }) ?? drawn.first else { return }
        open(f)
    }

    public func open(_ f: RecentFile) {
        selected = f.id
        notes[f.id] = nil
        onOpen(f)
    }

    /// Opening it came to nothing: the row says why.
    func couldNotOpen(_ f: RecentFile, _ skip: FileWindows.Skip) {
        let host = hostLabel(f.host)
        let why: String =
            switch skip {
            case .missing: "No longer on \(host)"
            case .directory: "It's a folder: open it in the web app's Files tab"
            case .unsupported: "This lasso is too old to open files in the Mac app"
            case .off, .away: "Not opened"
            }
        notes[f.id] = .failed(why)
    }

    func downloadFile(_ f: RecentFile) {
        if case .busy? = notes[f.id] { return }
        notes[f.id] = .busy("Downloading…")
        Task {
            do {
                let dest = try await download(f)
                notes[f.id] = .done("In Downloads as \(dest.lastPathComponent)")
            } catch {
                notes[f.id] = .failed("Couldn't download: \(FileDownloads.reason(error))")
            }
        }
    }

    func hide(_ f: RecentFile) {
        if confirmDelete == f.id { confirmDelete = nil }
        notes[f.id] = nil
        recent.remove(f.id)
        onForget(f)
    }

    func askDelete(_ f: RecentFile) {
        if case .busy? = notes[f.id] { return }
        confirmDelete = f.id
    }

    func cancelDelete() { confirmDelete = nil }

    /// Delete confirmed: off its machine, then off the list. A refusal stays
    /// on the row.
    func deleteFile(_ f: RecentFile) {
        confirmDelete = nil
        notes[f.id] = .busy("Deleting…")
        Task {
            do {
                try await delete(f)
                notes[f.id] = nil
                recent.remove(f.id)
                onForget(f)
            } catch {
                notes[f.id] = .failed("Couldn't delete: \(FileDownloads.reason(error))")
            }
        }
    }

    func clearAll() {
        for f in recent.items { onForget(f) }
        notes = [:]
        confirmDelete = nil
        recent.clear()
    }

    /// The column went: what the rows said goes with it.
    func reset() {
        filter = ""
        confirmDelete = nil
        notes = [:]
    }
}

public struct FilesListView: View {
    @ObservedObject var model: FilesListModel

    public init(model: FilesListModel) { self.model = model }

    private var row: Token { Theme.List.row }

    public var body: some View {
        let sections = model.sections
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                Text("Files").font(Theme.Fonts.title.font).foregroundStyle(Theme.Text.primary.color)
                Spacer()
                Text("\(model.recent.items.count)").font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color)
            }
            .padding(.horizontal, row.number("pad_h"))
            .padding(.top, Theme.space("xl"))
            .padding(.bottom, Theme.space("m"))
            .parityAnchor("files.header")
            if !model.recent.items.isEmpty {
                filterBar
                    .padding(.horizontal, row.number("pad_h"))
                    .padding(.bottom, Theme.space("s"))
            }
            ScrollView(.vertical) {
                VStack(alignment: .leading, spacing: 0) {
                    ForEach(sections) { s in
                        header(s.title)
                        ForEach(s.files) { rowView($0) }
                    }
                    if model.recent.items.isEmpty {
                        Text("Files agents open for you show up here.")
                            .font(Theme.Fonts.body.font)
                            .foregroundStyle(Theme.Text.secondary.color)
                            .padding(row.number("pad_h"))
                    } else if sections.isEmpty {
                        Text("No files match")
                            .font(Theme.Fonts.body.font)
                            .foregroundStyle(Theme.Text.secondary.color)
                            .padding(row.number("pad_h"))
                    }
                }
                .padding(.bottom, Theme.space("m"))
                .coordinateSpace(name: ColumnRows.space)
            }
            Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair"))
            HStack {
                Spacer()
                Button { model.clearAll() } label: {
                    Text("Clear list").font(Theme.Fonts.label.font)
                        .foregroundStyle(model.recent.items.isEmpty ? Theme.Text.placeholder.color : Theme.Text.secondary.color)
                        .padding(.vertical, Theme.space("l"))
                        .padding(.horizontal, row.number("pad_h"))
                        .contentShape(Rectangle())
                }
                .buttonStyle(RailPress())
                .disabled(model.recent.items.isEmpty)
                .accessibilityLabel("Clear the list of files")
                .help("Clear the list. The files themselves stay where they are.")
                .parityAnchor("files.clear")
            }
        }
        .onPreferenceChange(ColumnRowFrames.self) { frames in MainActor.assumeIsolated { model.rowFrames = frames } }
        .panelChrome(Theme.List.size.size, anchor: "files")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Files")
    }

    private var filterBar: some View {
        HStack(spacing: Theme.space("s")) {
            LucideIcon(.search, size: Theme.Fonts.body.number("size")).foregroundStyle(Theme.Text.secondary.color)
            PlainField(text: $model.filter, placeholder: "Filter", label: "Filter files", made: { model.filterField = $0 })
            if !model.filter.isEmpty {
                Text("\(model.files.count)/\(model.recent.items.count)")
                    .font(Theme.Fonts.small.font)
                    .foregroundStyle(Theme.Text.secondary.color)
                    .fixedSize()
                Button { model.filter = "" } label: {
                    LucideIcon(.x, size: Theme.icon.number("control")).contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .foregroundStyle(Theme.Text.secondary.color)
                .accessibilityLabel("Clear filter")
            }
        }
        .font(Theme.Fonts.body.font)
        .parityAnchor("files.filter")
    }

    private func header(_ title: String) -> some View {
        Text(title)
            .font(Theme.Fonts.header.font)
            .foregroundStyle(Theme.Text.secondary.color)
            .padding(.horizontal, row.number("pad_h"))
            .padding(.top, Theme.space("l"))
            .padding(.bottom, Theme.space("xs"))
            .accessibilityAddTraits(.isHeader)
    }

    private func age(_ f: RecentFile) -> String {
        (relativeAge(Int64(f.at.timeIntervalSince1970 * 1000), now: model.now()) ?? "").replacingOccurrences(of: " ago", with: "")
    }

    @ViewBuilder private func rowView(_ f: RecentFile) -> some View {
        let lit = model.keyboardLit ? model.selected == f.id : model.hovered == f.id
        let host = model.hostLabel(f.host)
        Group {
            if model.confirmDelete == f.id {
                confirm(f, host: host)
            } else {
                Button { model.open(f) } label: { label(f, host: host) }
                    .buttonStyle(.plain)
                    .accessibilityLabel(
                        "\(f.name) on \(host), in \(f.folder), \(f.opened ? "opened by \(f.from ?? "an agent")" : "not opened")"
                            + (model.openWindows.contains(f.id) ? ", its window is open" : ""))
            }
        }
        .padding(.horizontal, row.number("pad_h"))
        .padding(.vertical, Theme.space("m"))
        .background(
            RoundedRectangle(cornerRadius: Theme.Row.radius.pt)
                .fill(lit && model.confirmDelete != f.id ? (model.keyboardLit ? Theme.Row.selected.color : Theme.Row.hover.color) : .clear)
                .padding(.horizontal, Theme.space("m")))
        .contentShape(Rectangle())
        .columnRow(f.id)
        .contextMenu {
            Button("Open") { model.open(f) }
            Button("Download") { model.downloadFile(f) }
            Divider()
            Button("Hide from list") { model.hide(f) }
            Divider()
            Button("Delete on \(host)…", role: .destructive) { model.askDelete(f) }
        }
        .accessibilityAction(named: "Download") { model.downloadFile(f) }
        .accessibilityAction(named: "Hide from list") { model.hide(f) }
        .accessibilityAction(named: "Delete") { model.askDelete(f) }
        .parityAnchor("files.row[\(f.name)]")
    }

    private func label(_ f: RecentFile, host: String) -> some View {
        HStack(alignment: .top, spacing: Theme.space("l")) {
            LucideIcon(f.kind.icon, size: Theme.Rail.icon.number("size") - 1)
                .foregroundStyle(Theme.Text.secondary.color)
                .frame(width: Theme.Rail.icon.number("size") + 3)
                .padding(.top, Theme.space("2xs"))
            VStack(alignment: .leading, spacing: Theme.space("2xs")) {
                HStack(spacing: Theme.space("s")) {
                    Text(f.name).font(Theme.Fonts.name.font).foregroundStyle(Theme.Text.primary.color)
                        .lineLimit(1).truncationMode(.middle)
                    if model.openWindows.contains(f.id) { OpenDot().parityAnchor("files.open[\(f.name)]") }
                    Spacer(minLength: Theme.space("m"))
                    Text(age(f)).font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color).fixedSize()
                }
                HStack(spacing: Theme.space("m")) {
                    Text("\(host) · \(f.folder)").font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color)
                        .lineLimit(1).truncationMode(.head)
                    Spacer(minLength: Theme.space("m"))
                    Text(f.opened ? (f.from ?? "") : "not opened")
                        .font(Theme.Fonts.small.font)
                        .foregroundStyle(f.opened ? Theme.Text.secondary.color : Theme.accent.color)
                        .lineLimit(1)
                        .fixedSize()
                }
                if let n = model.notes[f.id] {
                    Text(n.text).font(Theme.Fonts.small.font)
                        .foregroundStyle(n.isFailure ? Theme.dangerText.color : Theme.Text.secondary.color)
                        .lineLimit(2)
                        .parityAnchor("files.note[\(f.name)]")
                }
            }
        }
        .contentShape(Rectangle())
    }

    /// Delete asks in the row's place: there are no alerts in this app.
    private func confirm(_ f: RecentFile, host: String) -> some View {
        VStack(alignment: .leading, spacing: Theme.space("m")) {
            Text("Delete \(f.name) on \(host)?").font(Theme.Fonts.name.font).foregroundStyle(Theme.Text.primary.color)
                .lineLimit(1).truncationMode(.middle)
            Text(model.isDirty(f.id) ? "Its window has unsaved changes. This can't be undone." : "This can't be undone.")
                .font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color)
            HStack(spacing: Theme.space("s")) {
                Spacer()
                PillButton(title: "Cancel", label: "Keep \(f.name)") { model.cancelDelete() }
                PillButton(title: "Delete", textColor: Theme.dangerText.color, label: "Confirm: delete \(f.name) on \(host)") {
                    model.deleteFile(f)
                }
                .parityAnchor("files.delete")
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

extension FileRowNote {
    var isFailure: Bool { if case .failed = self { true } else { false } }
}

struct FilesHost: View {
    @ObservedObject var presence: CardPresence
    let model: FilesListModel

    var body: some View {
        FilesListView(model: model)
            .opacity(presence.shown ? 1 : 0)
            .offset(x: presence.shown ? 0 : Theme.Motion.cardOpen.number("slide"))
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }
}

/// The Files column's panel beside the rail and its preview: the Bots
/// column's machinery (`ColumnHover`) with the agents column's typing.
@MainActor
public final class FilesController {
    private(set) var panel: RailPanel
    let previews: FilePreviewController
    var preview: RailPanel { previews.panel }
    public let model: FilesListModel
    private let presence = CardPresence()
    var railFrame: () -> NSRect = { .zero }
    let hover: ColumnHover

    init(model: FilesListModel, loader: FilePreviewLoader) {
        self.model = model
        let size = Theme.List.size.size
        let host = NSHostingView(rootView: FilesHost(presence: presence, model: model))
        host.frame = NSRect(origin: .zero, size: size)
        panel = RailPanel(content: host, size: size)
        panel.hasShadow = true
        previews = FilePreviewController(loader: loader)
        hover = ColumnHover(panel: panel, column: model)
        previews.make = { [weak self] id in
            guard let self, let f = self.model.recent.items.first(where: { $0.id == id }) else { return nil }
            let m = FilePreviewModel(
                file: f, hostLabel: self.model.hostLabel(f.host), windowOpen: self.model.openWindows.contains(id), now: self.model.now)
            m.onOpen = { [weak self] in self?.model.open(f) }
            return m
        }
        panel.onKey = { [weak self] e in self?.handle(e) ?? false }
        model.onClose = { [weak self] in self?.close() }
        hover.showing = { [weak self] in self?.previews.showing }
        hover.holds = { [weak self] in
            guard let self, self.previews.isOpen else { return false }
            return self.preview.frame.contains(self.hover.pointer())
        }
        hover.onShow = { [weak self] id, row in
            guard let self else { return }
            self.previews.show(id, beside: self.panel.frame, row: row)
        }
        hover.onHide = { [weak self] in self?.previews.close() }
    }

    public var isOpen: Bool { presence.shown }
    var previewOpen: Bool { previews.isOpen }
    var isKey: Bool { panel.isKeyWindow }

    func open(takeKey: Bool) {
        if !presence.shown { model.freezeOrder() }
        panel.setFrameOrigin(originBesideRail(railFrame(), size: Theme.List.size.size))
        front(takeKey)
        withAnimationToken(Theme.Motion.cardOpen) { presence.shown = true }
        hover.start()
    }

    func takeKey() { front(true) }

    private func front(_ takeKey: Bool) {
        panel.orderIn(takeKey: takeKey) {
            panel = $0
            hover.panel = $0
        }
    }

    func close() {
        if panel.isKeyWindow { panel.resignKey() }
        hover.stop()
        hidePreview()
        withAnimationToken(Theme.Motion.cardClose) { presence.shown = false }
        let delay = Theme.Motion.cardClose.value["duration"]?.double ?? 0
        Task { @MainActor [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(delay * 1e9) + 20_000_000)
            guard let self, !self.presence.shown else { return }
            self.panel.orderOut(nil)
            // The next opening lists every file, and says nothing stale.
            self.model.reset()
            self.model.thawOrder()
        }
    }

    func follow() { if isOpen { panel.setFrameOrigin(originBesideRail(railFrame(), size: Theme.List.size.size)) } }

    func hidePreview() { previews.close() }

    /// ↑/↓ move, ⏎ opens, esc steps back (the confirm, the filter, then the
    /// column); typing filters.
    private func handle(_ e: NSEvent) -> Bool {
        switch e.keyCode {
        case KeyCode.escape:
            if model.confirmDelete != nil {
                model.cancelDelete()
            } else if !model.filter.isEmpty {
                model.filter = ""
            } else {
                close()
            }
        case KeyCode.up: move(-1)
        case KeyCode.down: move(1)
        case KeyCode.returnKey, KeyCode.keypadEnter:
            guard model.confirmDelete == nil else { return false }
            model.openSelected()
        default:
            if filters(e) { focusFilter() }
            return false
        }
        return true
    }

    private func move(_ d: Int) {
        model.move(d)
        hover.keyboardMoved()
    }

    /// Plain characters (and delete while there is a filter) filter; keys
    /// with ⌘, ⌃ or ⌥ stay shortcuts.
    private func filters(_ e: NSEvent) -> Bool {
        if !e.modifierFlags.intersection([.command, .control, .option]).isEmpty { return false }
        if e.keyCode == KeyCode.delete { return !model.filter.isEmpty }
        guard let c = e.characters?.unicodeScalars.first else { return false }
        return !CharacterSet.controlCharacters.contains(c) && !(0xF700...0xF8FF).contains(c.value)
    }

    private func focusFilter() {
        guard let f = model.filterField, f.window === panel, !panel.editingText else { return }
        panel.makeFirstResponder(f)
        f.currentEditor()?.selectedRange = NSRange(location: (f.stringValue as NSString).length, length: 0)
    }
}
