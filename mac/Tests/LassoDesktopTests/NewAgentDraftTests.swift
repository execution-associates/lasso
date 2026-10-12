import Foundation
import Testing

@testable import LassoDesktopKit

/// The New form's unsent prompt outlives the form: kept as typed, newlines
/// and all, and gone once cleared.
@MainActor @Test func newFormDraftOutlivesTheForm() throws {
    let suite = "lasso.test.newAgentDraft"
    let d = try #require(UserDefaults(suiteName: suite))
    d.removePersistentDomain(forName: suite)
    let saved = NewAgentDraft.defaults
    NewAgentDraft.defaults = d
    defer {
        NewAgentDraft.defaults = saved
        d.removePersistentDomain(forName: suite)
    }
    #expect(NewAgentDraft.text == "")
    NewAgentDraft.text = "Fix the hover\nthen the rail"
    #expect(NewAgentDraft.text == "Fix the hover\nthen the rail")
    NewAgentDraft.text = ""
    #expect(d.object(forKey: NewAgentDraft.key) == nil)
}
