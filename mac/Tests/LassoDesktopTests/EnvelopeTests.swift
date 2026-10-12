import Foundation
import Testing

@testable import LassoDesktopKit

// Claude Code's tagged envelopes, read as the web chat reads them
// (ChatView.tsx taskNotification, channelMessage).

private let notice = #"<task-notification><task-id>b2v8x1q</task-id><tool-use-id>toolu_01AbCdEf</tool-use-id><output-file>/tmp/claude/tasks/b2v8x1q.output</output-file><status>completed</status><summary>Background command "Render Lorenz natively in sandbox for timing comparison" completed (exit code 0)</summary></task-notification>"#

private let mail = #"<channel source="gmail-channel" from_name="Dana &amp; Co" from_email="dana@example.com" subject="Thursday at 3?">Can we move it to Thursday at 3pm?</channel>"#

private func items(_ json: String) -> [ChatItem] {
    try! JSONDecoder().decode([ChatItem].self, from: Data(json.utf8))
}

/// A background task's envelope is its summary line, its status, and the
/// result to unfold; it is not the human speaking.
@Test func aTaskNotificationFoldsToItsSummary() throws {
    let n = try #require(TaskNotification.parse(notice))
    #expect(n.summary == #"Background command "Render Lorenz natively in sandbox for timing comparison" completed (exit code 0)"#)
    #expect(n.status == "completed" && !n.failed && n.result.isEmpty)
    let failed = try #require(TaskNotification.parse("<task-notification><status>failed</status><result>exit 2</result></task-notification>"))
    #expect(failed.summary == "Background task finished" && failed.failed && failed.result == "exit 2")
    #expect(TaskNotification.parse("Please render <task-notification> later") == nil)
}

/// A channel's envelope is an incoming message labelled with its source;
/// an unknown channel keeps its name, and a body with no envelope its row's
/// source.
@Test func aChannelMessageIsLabelledWithItsSource() {
    let m = ChannelMessage.parse(mail)
    #expect(m == ChannelMessage(label: "Email", from: "Dana & Co", subject: "Thursday at 3?", body: "Can we move it to Thursday at 3pm?"))
    #expect(m.icon == .mail && !m.long)
    let mm = ChannelMessage.parse(#"<channel source="mattermost-channel" user="clem" channel_name="ops">deploy done</channel>"#)
    #expect(mm.label == "Mattermost" && mm.from == "clem" && mm.subject == "#ops" && mm.icon == .messageSquare)
    let loop = ChannelMessage.parse(#"<channel source="everloop" loop="digest">time for the digest</channel>"#)
    #expect(loop.label == "Scheduled" && loop.subject == "digest" && loop.icon == .clock)
    let other = ChannelMessage.parse(#"<channel source="weather-channel">rain at 4</channel>"#)
    #expect(other.label == "weather" && other.body == "rain at 4")
    let bare = ChannelMessage.parse("plain text", source: "triage-channel")
    #expect(bare.label == "triage" && bare.body == "plain text")
    #expect(ChannelMessage.parse("<channel>" + String(repeating: "x", count: 300) + "</channel>").long)
}

/// In a window, a preview and the inbox card alike: the notice folds to a
/// task row and the delivery to an incoming card, neither a bubble of
/// yours; and the preview's latest turn starts after your last message, not
/// after a notice.
@Test func envelopesAreNotTheHumansBubbles() {
    let list = items(
        """
        [{"kind":"user","id":"u1","text":"Render it"},
         {"kind":"agent","id":"a1","text":"Started in the background."},
         {"kind":"user","id":"u2","text":\(String(data: try! JSONEncoder().encode(notice), encoding: .utf8)!)},
         {"kind":"agent","id":"a2","text":"Done: 41 ms natively."},
         {"kind":"incoming","id":"i1","source":"gmail-channel","text":\(String(data: try! JSONEncoder().encode(mail), encoding: .utf8)!)}]
        """)
    let entries = ConversationEntry.fold(list)
    #expect(entries.count == 5)
    if case .task(_, let n) = entries[2] { #expect(n.status == "completed") } else { Issue.record("not a task row: \(entries[2])") }
    if case .incoming(_, let m) = entries[4] { #expect(m.label == "Email") } else { Issue.record("not incoming: \(entries[4])") }
    let mine = entries.filter { if case .message(_, _, true) = $0 { return true } else { return false } }
    #expect(mine.map(\.id) == ["u1"])
    #expect(ConversationEntry.latestTurn(list).map(\.id) == ["a1", "u2", "a2", "i1"])
}
