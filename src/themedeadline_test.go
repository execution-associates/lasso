package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// abortableTarget is a themeTarget whose write blocks like a wedged SFTP channel
// until abortSFTP is called, the way killing the transport fails a blocked op.
type abortableTarget struct {
	themeTargetStub
	aborted chan struct{}
}

func (a *abortableTarget) abortSFTP() { close(a.aborted) }

func withThemeDeadlines(t *testing.T, timeout, grace time.Duration) {
	t.Helper()
	oldT, oldG := themeHostTimeout, themeAbortGrace
	themeHostTimeout, themeAbortGrace = timeout, grace
	t.Cleanup(func() { themeHostTimeout, themeAbortGrace = oldT, oldG })
}

// A host whose write hangs is aborted and reported failed within the deadline,
// instead of holding the fan-out (and every later sync) forever.
func TestThemeHostDeadlineAbortsWedgedWrite(t *testing.T) {
	withThemeDeadlines(t, 50*time.Millisecond, time.Second)
	tgt := &abortableTarget{aborted: make(chan struct{})}
	unwound := make(chan struct{})
	start := time.Now()
	err := withThemeHostDeadline("gigachad", tgt, func() error {
		defer close(unwound)
		<-tgt.aborted
		return errors.New("connection lost")
	})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if d := time.Since(start); d > 900*time.Millisecond {
		t.Fatalf("took %v: the abort did not unblock the write", d)
	}
	select {
	case <-unwound:
	default:
		t.Fatal("returned before the aborted write unwound")
	}
}

// A write that never unblocks, even after the abort, is abandoned after the
// grace period rather than waited on forever.
func TestThemeHostDeadlineAbandonsUnabortableWrite(t *testing.T) {
	withThemeDeadlines(t, 20*time.Millisecond, 20*time.Millisecond)
	block := make(chan struct{})
	defer close(block)
	err := withThemeHostDeadline("stuck", &themeTargetStub{}, func() error {
		<-block
		return nil
	})
	if err == nil {
		t.Fatal("want a timeout error")
	}
}

// A healthy write's own result comes straight back, success or failure.
func TestThemeHostDeadlinePassesResultThrough(t *testing.T) {
	withThemeDeadlines(t, time.Second, time.Second)
	if err := withThemeHostDeadline("ok", &themeTargetStub{}, func() error { return nil }); err != nil {
		t.Fatalf("err = %v", err)
	}
	want := errors.New("permission denied")
	if err := withThemeHostDeadline("ok", &themeTargetStub{}, func() error { return want }); err != want {
		t.Fatalf("err = %v, want %v", err, want)
	}
}
