package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func writeTestLog(t *testing.T, path string, n int, tag string) int64 {
	t.Helper()
	var sb strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, "{\"n\":%d,\"tag\":%q}\n", i, tag)
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return int64(sb.Len())
}

// The cache is dropped when the file shrinks or the cache outgrows
// logCacheKept. That reset runs under c.mu; it used to assign a fresh
// transcriptLog over the struct, zeroing the held mutex, so the deferred
// Unlock in logLinesBefore was a fatal "sync: unlock of unlocked mutex"
// (production lasso, GET /api/bots, 2026-10-12). Fatal, not a panic: before
// the fix this test kills the test binary.
func TestTranscriptLogResetKeepsLock(t *testing.T) {
	b := &localBackend{}
	path := filepath.Join(t.TempDir(), "s.jsonl")

	t.Run("shrunk file", func(t *testing.T) {
		size := writeTestLog(t, path, 50, "old")
		if got := logLinesBefore(b, path, size, size, 1<<20); len(got) != 50 {
			t.Fatalf("first read: %d lines, want 50", len(got))
		}
		size = writeTestLog(t, path, 5, "new")
		got := logLinesBefore(b, path, size, size, 1<<20)
		if len(got) != 5 || !strings.Contains(string(got[4].data), `"new"`) {
			t.Fatalf("after shrink: %d lines, want 5 new ones", len(got))
		}
		if got := logLinesFrom(b, path, size, 0, 1<<20); len(got) != 5 {
			t.Fatalf("logLinesFrom after shrink: %d lines, want 5", len(got))
		}
	})

	t.Run("cache over its bound", func(t *testing.T) {
		size := writeTestLog(t, path, 20, "big")
		logLinesBefore(b, path, size, size, 1<<20)
		c := transcriptLogFor(b.Name(), path)
		c.mu.Lock()
		c.kept = logCacheKept + 1
		c.mu.Unlock()
		if got := logLinesBefore(b, path, size, size, 1<<20); len(got) != 20 {
			t.Fatalf("after reset: %d lines, want 20", len(got))
		}
		c.mu.Lock()
		c.kept = logCacheKept + 1
		c.mu.Unlock()
		if got := logLinesFrom(b, path, size, 0, 1<<20); len(got) != 20 {
			t.Fatalf("logLinesFrom after reset: %d lines, want 20", len(got))
		}
	})

	// Readers racing a reset: with the old code the mutex was zeroed while
	// held, letting a waiter in alongside the holder (-race sees it) or
	// tripping the fatal unlock.
	t.Run("concurrent readers across resets", func(t *testing.T) {
		var wg sync.WaitGroup
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for i := 0; i < 50; i++ {
					st, err := os.Stat(path)
					if err != nil {
						t.Error(err)
						return
					}
					c := transcriptLogFor(b.Name(), path)
					c.mu.Lock()
					c.kept = logCacheKept + 1
					c.mu.Unlock()
					logLinesBefore(b, path, st.Size(), st.Size(), 1<<20)
					logLinesFrom(b, path, st.Size(), 0, 1<<20)
				}
			}(g)
		}
		wg.Wait()
	})
}
