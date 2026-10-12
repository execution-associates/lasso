import { describe, expect, test } from "bun:test"

import { inboxEntries, inboxNotices } from "@/simple/inbox-rule"
import rules from "@/simple/inbox-rules.json"

// The cases the Mac app's tests read too (mac/Spec/inbox/rules.json; this
// copy is kept equal to it by the Go test TestInboxRulesCopyInSync), so the
// phone and the Mac list the same entries for the same lasso.
describe("the shared Inbox rule", () => {
  for (const c of rules.cases) {
    test(c.name, () => {
      const dismissed = c.ui_state.dismissed_cards ?? {}
      const entries = inboxEntries({
        panes: c.panes,
        bots: c.bots,
        dismissed,
        since: c.ui_state.inbox_since,
      })
      expect(
        entries.map((e) => ({
          kind: e.kind,
          pane: e.key,
          ack_key: e.ackKey,
          ack_at: e.ackAt,
        }))
      ).toEqual(c.want.entries)
      const notices = inboxNotices(c.notices, dismissed).map((n) => n.id)
      expect(notices).toEqual(c.want.notices)
      expect(entries.length + notices.length).toBe(c.want.badge)
    })
  }
})
