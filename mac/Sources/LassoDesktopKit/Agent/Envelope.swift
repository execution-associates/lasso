import Foundation
import SwiftUI

// Claude Code's tagged envelopes, read as the web chat reads them
// (src/web/src/components/ChatView.tsx: taskNotification, channelMessage):
// a background task's result and a channel's delivered message arrive in
// the transcript as turns on the human's side, but neither is the human
// speaking, so neither is drawn as the human's bubble.

/// The `<task-notification>` envelope Claude Code writes into the log as a
/// USER turn when a background agent or shell finishes. It is the harness
/// talking, and its result can run to pages, so it folds to its summary.
public struct TaskNotification: Equatable, Sendable {
    public var summary: String
    /// "completed", "failed", "killed"…, or "" when the envelope has none.
    public var status: String
    public var result: String

    /// Anything but a plain completion is shown, as the web does.
    public var failed: Bool { !status.isEmpty && status != "completed" }

    /// The envelope's parts, nil for any other text.
    public static func parse(_ text: String) -> TaskNotification? {
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard t.hasPrefix("<task-notification>") else { return nil }
        let summary = Envelope.tag("summary", in: t)
        return TaskNotification(
            summary: summary.isEmpty ? "Background task finished" : summary, status: Envelope.tag("status", in: t),
            result: Envelope.tag("result", in: t))
    }
}

/// The envelope a Claude Code channel delivers a message in:
/// `<channel source="gmail-channel" from_name="…" …>body</channel>`. For a
/// bot most of what it answers arrives this way (mail, texts, Mattermost,
/// its own scheduled loops), not typed by the human, so it is an incoming
/// message labelled with where it came from. A channel this does not know
/// keeps its source name as the label and its body.
public struct ChannelMessage: Equatable, Sendable {
    public var label: String
    public var from: String
    public var subject: String
    public var body: String

    /// `source` is the row's own (lasso's `source`), for a body with no envelope.
    public static func parse(_ text: String, source: String? = nil) -> ChannelMessage {
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let m = t.wholeMatch(of: /<channel\b([^>]*)>([\s\S]*?)<\/channel>/) else {
            let label = (source ?? "").isEmpty ? "Message" : Envelope.dropChannel(source!)
            return ChannelMessage(label: label, from: "", subject: "", body: t)
        }
        let attrs = String(m.1)
        func attr(_ name: String) -> String { Envelope.attr(name, in: attrs) }
        let src = attr("source").isEmpty ? (source ?? "") : attr("source")
        var label = Envelope.dropChannel(src)
        if label.isEmpty { label = "Message" }
        var from = ""
        var subject = ""
        switch src {
        case "gmail-channel":
            label = "Email"
            from = attr("from_name").isEmpty ? attr("from_email") : attr("from_name")
            subject = attr("subject")
        case "mattermost-channel":
            label = "Mattermost"
            from = attr("user")
            subject = attr("channel_name").isEmpty ? "" : "#" + attr("channel_name")
        case "triage-channel":
            // One channel for several services (iMessage, texts, …): the service is the label.
            label = attr("service").isEmpty ? "Message" : attr("service")
            from = attr("sender_name").isEmpty ? attr("sender") : attr("sender_name")
            subject = attr("thread_name").isEmpty ? attr("subject") : attr("thread_name")
        case "everloop":
            label = "Scheduled"
            subject = attr("loop")
        case "hotline":
            label = "Hotline"
            from = attr("from")
        default: break
        }
        return ChannelMessage(
            label: label, from: from, subject: subject, body: String(m.2).trimmingCharacters(in: .whitespacesAndNewlines))
    }

    /// A long one (a forwarded thread) is clamped until asked for.
    public var long: Bool { body.count > 280 || body.split(separator: "\n", omittingEmptySubsequences: false).count > 4 }

    /// Its icon, the web chat's (`channelIcon`): the family lives in the
    /// icon, colour stays for state.
    public var icon: Lucide {
        let l = label.lowercased()
        if l == "email" || l.contains("mail") { return .mail }
        if l == "scheduled" { return .clock }
        if l == "hotline" { return .phone }
        if ["mattermost", "imessage", "text", "sms", "slack", "whatsapp", "telegram"].contains(where: { l.contains($0) }) {
            return .messageSquare
        }
        return .inbox
    }
}

enum Envelope {
    /// `<name>…</name>`'s trimmed text, "" when absent.
    static func tag(_ name: String, in t: String) -> String {
        guard let open = t.range(of: "<\(name)>"), let close = t.range(of: "</\(name)>", range: open.upperBound..<t.endIndex)
        else { return "" }
        return String(t[open.upperBound..<close.lowerBound]).trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// `name="…"`'s value among an opening tag's attributes, entities decoded.
    static func attr(_ name: String, in attrs: String) -> String {
        var rest = Substring(attrs)
        while let r = rest.range(of: "\(name)=\"") {
            // A whole attribute name: not the tail of a longer one.
            let before = r.lowerBound == rest.startIndex ? " " : rest[rest.index(before: r.lowerBound)]
            if before.isLetter || before.isNumber || before == "_" {
                rest = rest[r.upperBound...]
                continue
            }
            guard let end = rest[r.upperBound...].firstIndex(of: "\"") else { return "" }
            return decode(String(rest[r.upperBound..<end]))
        }
        return ""
    }

    static func decode(_ s: String) -> String {
        s.replacingOccurrences(of: "&quot;", with: "\"").replacingOccurrences(of: "&#39;", with: "'")
            .replacingOccurrences(of: "&apos;", with: "'").replacingOccurrences(of: "&lt;", with: "<")
            .replacingOccurrences(of: "&gt;", with: ">").replacingOccurrences(of: "&amp;", with: "&")
    }

    static func dropChannel(_ s: String) -> String { s.hasSuffix("-channel") ? String(s.dropLast(8)) : s }
}

/// Which folded rows are open: rows are drawn afresh on every poll, so their
/// open state lives here, by row, rather than in the row.
@MainActor
final class Unfolded: ObservableObject {
    static let shared = Unfolded()
    @Published private(set) var open: Set<String> = []
    func toggle(_ id: String) { if open.contains(id) { open.remove(id) } else { open.insert(id) } }
}

/// A finished background task, folded to its summary line in a tool run's
/// look: the harness speaking, not the human. Its status shows when it is
/// not a plain completion; a click unfolds the result.
struct TaskNotificationRow: View {
    let id: String
    let note: TaskNotification
    @ObservedObject private var unfolded = Unfolded.shared

    init(id: String, note: TaskNotification) {
        self.id = id
        self.note = note
    }

    var body: some View {
        let open = unfolded.open.contains(id)
        VStack(alignment: .leading, spacing: Theme.space("s")) {
            Button { unfolded.toggle(id) } label: {
                HStack(spacing: Theme.space("s")) {
                    LucideIcon(open ? .chevronDown : .chevronRight, size: Theme.icon.number("pin"))
                    Text(note.summary).lineLimit(1).truncationMode(.tail)
                    if note.failed {
                        Text(note.status).foregroundStyle(Theme.dangerText.color)
                    }
                    Spacer(minLength: 0)
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .font(Theme.Fonts.small.font)
            .foregroundStyle(Theme.Text.secondary.color)
            .accessibilityLabel("Background task: \(note.summary)\(note.failed ? ", \(note.status)" : "")")
            .accessibilityHint(note.result.isEmpty ? "" : (open ? "Fold the result" : "Show the result"))
            if open, !note.result.isEmpty {
                MarkdownView(text: note.result)
                    .padding(.leading, Theme.space("l"))
                    .overlay(alignment: .leading) { Rectangle().fill(Theme.Panel.divider.color).frame(width: Theme.space("2xs")) }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// One delivered message: where it came from on top, the body under it,
/// clamped when long (a forwarded thread can run to pages, and the bot's
/// answer to it is what the reader came for).
struct ChannelCard: View {
    let id: String
    let msg: ChannelMessage
    @ObservedObject private var unfolded = Unfolded.shared

    init(id: String, msg: ChannelMessage) {
        self.id = id
        self.msg = msg
    }

    var body: some View {
        let b = Theme.Chat.bubble
        let open = unfolded.open.contains(id)
        VStack(alignment: .leading, spacing: Theme.space("s")) {
            HStack(spacing: Theme.space("s")) {
                LucideIcon(msg.icon, size: Theme.Fonts.body.number("size")).foregroundStyle(Theme.Text.secondary.color)
                Text(msg.label.uppercased())
                    .font(Theme.Fonts.small.font)
                    .foregroundStyle(Theme.Text.secondary.color)
                    .padding(.horizontal, Theme.space("s"))
                    .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt).fill(Theme.Panel.inner.color("fill")))
                if !msg.from.isEmpty {
                    Text(msg.from).font(Theme.Fonts.label.font).foregroundStyle(Theme.Text.primary.color).lineLimit(1)
                }
                if !msg.subject.isEmpty {
                    Text(msg.subject).font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color).lineLimit(1)
                }
                Spacer(minLength: 0)
            }
            if !msg.body.isEmpty {
                Text(msg.body)
                    .font(Theme.Fonts.chat.font)
                    .foregroundStyle(Theme.Text.primary.color)
                    .lineLimit(msg.long && !open ? 4 : nil)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            if msg.long {
                Button(open ? "Show less" : "Show more") { unfolded.toggle(id) }
                    .buttonStyle(.plain)
                    .font(Theme.Fonts.small.font)
                    .foregroundStyle(Theme.Text.secondary.color)
            }
        }
        .padding(.horizontal, b.number("pad_h"))
        .padding(.vertical, b.number("pad_v"))
        .background(
            RoundedRectangle(cornerRadius: b.number("radius"), style: .continuous)
                .strokeBorder(Theme.Panel.divider.color, lineWidth: Theme.space("hair")))
        .padding(.trailing, Theme.Layout.chat.number("bubble_inset"))
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .contain)
        .accessibilityLabel("\(msg.label) message\(msg.from.isEmpty ? "" : " from \(msg.from)")")
    }
}
