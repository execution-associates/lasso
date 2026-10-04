package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The agents lists sort on this value, so what matters is that it follows the
// FILE: a write moves it forward even while the resolved path is cached, and a
// pane with no transcript reads 0 rather than some stale neighbour's time.
func TestPaneTranscriptAt(t *testing.T) {
	b := &localBackend{}
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	p := pane{PaneID: "w1:p1", Agent: "omp", AgentSession: &agentSession{Agent: "omp", Kind: "path", Value: path}}
	t.Cleanup(func() { pruneTranscriptPaths("test", nil) })

	if got := paneTranscriptAt(b, "test", p); got != old.UnixMilli() {
		t.Fatalf("mtime = %d, want %d", got, old.UnixMilli())
	}
	now := time.Now().Truncate(time.Millisecond)
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}
	if got := paneTranscriptAt(b, "test", p); got != now.UnixMilli() {
		t.Fatalf("a write did not move the cached pane forward: %d, want %d", got, now.UnixMilli())
	}

	shell := pane{PaneID: "w1:p2"}
	if got := paneTranscriptAt(b, "test", shell); got != 0 {
		t.Fatalf("bare shell has a transcript time: %d", got)
	}
}
