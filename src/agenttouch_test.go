package main

import (
	"testing"
	"time"
)

func TestAgentTouchRecordsPerHostAndPrunesStale(t *testing.T) {
	t.Setenv("LASSO_DIR", t.TempDir())
	if err := openDB(); err != nil {
		t.Fatalf("openDB: %v", err)
	}
	t.Cleanup(closeTestDB)

	old := time.Now().Add(-agentTouchKeep - time.Hour).UnixMilli()
	if _, err := db.Exec(`INSERT INTO agent_touch (host, pane_id, at) VALUES ('local', 'gone', ?)`, old); err != nil {
		t.Fatal(err)
	}
	before := time.Now().UnixMilli()
	touchAgent("local", "w1:p1")
	touchAgent("norm", "w1:p1")
	touchAgent("", "w1:p2") // no host: ignored

	got := agentTouches("local")
	if at := got["w1:p1"]; at < before {
		t.Fatalf("local w1:p1 touched at %d, want >= %d", at, before)
	}
	if _, ok := got["gone"]; ok {
		t.Fatal("a touch older than agentTouchKeep survived a write")
	}
	if len(got) != 1 {
		t.Fatalf("local touches = %v, want only w1:p1", got)
	}
	if len(agentTouches("norm")) != 1 {
		t.Fatal("norm's touch missing: touches are per host")
	}
}
