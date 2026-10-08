package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// The reported bug: minime's herdr listed a pane (wY0:p1) whose claude session
// had run on titan, so its log was on titan's disk and every read on minime said
// "This session's transcript is not on this host yet." These stand a small fleet
// up — the pane's host, a host that has the log, hosts that do not — and check
// the chat and the pane listing follow the session to the host that has it.

// diskHost is one fake machine: chatScreenBackend's herdr, plus a real
// filesystem under its own home. gate, when set, holds every Stat until it is
// closed — a host that does not answer.
type diskHost struct {
	*chatScreenBackend
	gate  chan struct{}
	stats atomic.Int64
}

func (b *diskHost) Stat(p string) (fs.FileInfo, error) {
	b.stats.Add(1)
	if b.gate != nil {
		<-b.gate
	}
	return os.Stat(p)
}

func (b *diskHost) Open(p string) (io.ReadSeekCloser, error) { return os.Open(p) }

func (b *diskHost) ReadDir(p string) ([]fileEntry, error) {
	ents, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	out := make([]fileEntry, 0, len(ents))
	for _, e := range ents {
		out = append(out, fileEntry{Name: e.Name(), Dir: e.IsDir()})
	}
	return out, nil
}

// newDiskHost names a host uniquely per test (the pane.list cache and the
// transcript search's slow-host list are keyed by name and outlive a test).
func newDiskHost(t *testing.T, prefix, panes string) *diskHost {
	return &diskHost{chatScreenBackend: &chatScreenBackend{
		name:  fmt.Sprintf("%s-%d", prefix, fakeHostSeq.Add(1)),
		home:  t.TempDir(),
		panes: panes,
	}}
}

// crossHostSession is the session the fixtures share: the pane's cwd as titan
// knows it, and claude's id.
const (
	crossHostCwd = "/home/stephan/.lasso/worktrees/lasso/herdr-tool-group"
	crossHostID  = "7d1c0a3e-5b2f-4c1e-9a77-1f0e2d3c4b5a"
)

// writeClaudeLog puts a two-turn claude log for crossHostID under h's home,
// where claude itself would: ~/.claude/projects/<slug of the cwd>/<id>.jsonl.
func writeClaudeLog(t *testing.T, h *diskHost) string {
	t.Helper()
	dir := filepath.Join(h.home, ".claude", "projects", claudeProjectSlug(crossHostCwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, crossHostID+".jsonl")
	body := chatLog(
		`{"type":"user","uuid":"u1","timestamp":"2026-10-07T10:00:00.000Z","message":{"role":"user","content":"rename the herdr tools"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-10-07T10:00:05.000Z","message":{"role":"assistant","model":"claude-opus-5-5","stop_reason":"end_turn","content":[{"type":"text","text":"Renamed."}]}}`,
	)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// crossHostPane is the pane as the pane's host reports it.
func crossHostPane() string {
	return fmt.Sprintf(`{"panes":[{"pane_id":"wY0:p1","workspace_id":"w1","tab_id":"t1","cwd":%q,"focused":true,`+
		`"agent":"claude","agent_status":"idle",`+
		`"agent_session":{"agent":"claude","kind":"id","source":"herdr:claude","value":%q}}]}`,
		crossHostCwd, crossHostID)
}

// crossHostFleet makes hosts addressable by name and the transcript search's
// whole candidate list, with a default backend that is none of them. It also
// gives the test a clean search cache.
func crossHostFleet(t *testing.T, hosts ...*diskHost) {
	t.Helper()
	byName := map[string]*diskHost{}
	var names []string
	for _, h := range hosts {
		byName[h.name] = h
		names = append(names, h.name)
		invalidatePaneList(h.name)
	}
	stubSSHHosts(t, names...)
	stubProbedHosts(t, names...)

	prevFn, prevHosts, prevDefault := hostBackendFn, transcriptHostsFn, defaultBackend()
	hostBackendFn = func(host string) (Backend, error) {
		if h, ok := byName[host]; ok {
			return h, nil
		}
		return nil, fmt.Errorf("host %q not available", host)
	}
	transcriptHostsFn = func(context.Context) []string { return names }
	setDefaultBackend(&chatFakeBackend{})
	resetTranscriptLocs()
	t.Cleanup(func() {
		// Release every held host and wait for the probes a search abandoned
		// before putting the globals back: those probes read hostBackendFn.
		for _, h := range hosts {
			if h.gate != nil {
				select {
				case <-h.gate:
				default:
					close(h.gate)
				}
			}
		}
		waitTranscriptProbes()
		hostBackendFn, transcriptHostsFn = prevFn, prevHosts
		setDefaultBackend(prevDefault)
		for _, h := range hosts {
			invalidatePaneList(h.name)
		}
		resetTranscriptLocs()
	})
}

// waitTranscriptProbes returns once no probe is in flight, by taking every
// slot of the probe semaphore.
func waitTranscriptProbes() {
	for range cap(transcriptProbeSem) {
		transcriptProbeSem <- struct{}{}
	}
	for range cap(transcriptProbeSem) {
		<-transcriptProbeSem
	}
}

func resetTranscriptLocs() {
	transcriptLocs.Lock()
	transcriptLocs.m = map[string]transcriptLocation{}
	transcriptLocs.slow = map[string]time.Time{}
	transcriptLocs.Unlock()
}

func getChat(t *testing.T, host string) chatPayload {
	t.Helper()
	rec := httptest.NewRecorder()
	serveChat(rec, httptest.NewRequest(http.MethodGet, "/api/chat?pane=wY0:p1&host="+host, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/chat on %s = %d: %s", host, rec.Code, rec.Body.String())
	}
	var out chatPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestServeChatFollowsSessionToAnotherHost(t *testing.T) {
	minime := newDiskHost(t, "minime", crossHostPane())
	titan := newDiskHost(t, "titan", "")
	exa := newDiskHost(t, "exa", "")
	path := writeClaudeLog(t, titan)
	crossHostFleet(t, minime, titan, exa)

	out := getChat(t, minime.name)
	if len(out.Items) == 0 {
		t.Fatalf("items empty (note %q, unavailable %+v), want the log titan holds", out.Note, out.Unavailable)
	}
	if out.Host != minime.name {
		t.Errorf("host = %q, want %q — host stays the pane's machine, where the composer types", out.Host, minime.name)
	}
	if out.ServedBy != titan.name {
		t.Errorf("served_by = %q, want %q, the host that has the log", out.ServedBy, titan.name)
	}
	if out.Path != path {
		t.Errorf("path = %q, want %q", out.Path, path)
	}
	if out.Note != "" || out.Unavailable != nil || out.Starting {
		t.Errorf("note %q / unavailable %+v / starting %v on a served log, want none", out.Note, out.Unavailable, out.Starting)
	}

	// Paging stays on the host the first page came from.
	rec := httptest.NewRecorder()
	serveChat(rec, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/chat?pane=wY0:p1&host=%s&before=%d", minime.name, out.StartOffset+1), nil))
	var page chatPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.ServedBy != titan.name {
		t.Errorf("paged served_by = %q, want %q", page.ServedBy, titan.name)
	}
}

// An ordinary pane reports served_by as its own host, so a client can always
// read the field rather than special-casing its absence.
func TestServeChatServedByOwnHost(t *testing.T) {
	minime := newDiskHost(t, "minime", crossHostPane())
	writeClaudeLog(t, minime)
	titan := newDiskHost(t, "titan", "")
	crossHostFleet(t, minime, titan)

	out := getChat(t, minime.name)
	if out.ServedBy != minime.name || len(out.Items) == 0 {
		t.Errorf("served_by = %q with %d items, want %q", out.ServedBy, len(out.Items), minime.name)
	}
	if n := titan.stats.Load(); n != 0 {
		t.Errorf("titan was asked %d times for a log the pane's own host has, want 0", n)
	}
}

// No host has it: the note path is unchanged, still a wait, and the reason says
// which hosts were asked.
func TestServeChatNoHostHasTheLog(t *testing.T) {
	minime := newDiskHost(t, "minime", crossHostPane())
	titan := newDiskHost(t, "titan", "")
	exa := newDiskHost(t, "exa", "")
	crossHostFleet(t, minime, titan, exa)

	out := getChat(t, minime.name)
	if len(out.Items) != 0 || out.ServedBy != "" || out.Path != "" {
		t.Fatalf("items %d / served_by %q / path %q, want an empty view", len(out.Items), out.ServedBy, out.Path)
	}
	if out.Note != "This session's transcript is not on this host yet." || !out.Starting {
		t.Errorf("note %q / starting %v, want the unchanged wait", out.Note, out.Starting)
	}
	want := &chatUnavailable{
		Reason: chatReasonNotFound,
		// The pane's host first, then the search's order: by name.
		Checked: []string{minime.name, exa.name, titan.name},
	}
	if !reflect.DeepEqual(out.Unavailable, want) {
		t.Errorf("unavailable = %+v, want %+v", out.Unavailable, want)
	}
}

// Other empty views carry their reason too, and are never searched for.
func TestServeChatUnavailableReasons(t *testing.T) {
	noAgent := `{"panes":[{"pane_id":"wY0:p1","focused":true}]}`
	minime := newDiskHost(t, "minime", noAgent)
	titan := newDiskHost(t, "titan", "")
	crossHostFleet(t, minime, titan)

	out := getChat(t, minime.name)
	if out.Unavailable == nil || out.Unavailable.Reason != chatReasonNoSession || out.Unavailable.Checked != nil {
		t.Errorf("unavailable = %+v, want no_session with no hosts checked", out.Unavailable)
	}
	if n := titan.stats.Load(); n != 0 {
		t.Errorf("titan asked %d times for a pane with no session, want 0", n)
	}
}

// The answer is cached per session: a poll does not re-ask the fleet, and a
// cached path that stops existing is dropped at once rather than served stale.
func TestTranscriptSearchIsCached(t *testing.T) {
	minime := newDiskHost(t, "minime", crossHostPane())
	titan := newDiskHost(t, "titan", "")
	exa := newDiskHost(t, "exa", "")
	path := writeClaudeLog(t, titan)
	crossHostFleet(t, minime, titan, exa)

	getChat(t, minime.name)
	exaAsked := exa.stats.Load()
	for range 3 {
		if out := getChat(t, minime.name); out.ServedBy != titan.name {
			t.Fatalf("served_by = %q on a repeat poll, want %q", out.ServedBy, titan.name)
		}
	}
	if n := exa.stats.Load(); n != exaAsked {
		t.Errorf("exa asked %d more times across cached polls, want 0", n-exaAsked)
	}

	// The log goes away on titan: the next poll must not serve the cached path.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	out := getChat(t, minime.name)
	if out.ServedBy != "" || out.Unavailable == nil || out.Unavailable.Reason != chatReasonNotFound {
		t.Errorf("after the log vanished: served_by %q, unavailable %+v, want not_found", out.ServedBy, out.Unavailable)
	}
	// And the poll after that searches afresh rather than holding a stale hit.
	writeClaudeLog(t, titan)
	transcriptLocs.Lock()
	for k, v := range transcriptLocs.m { // age the miss out instead of sleeping 30s
		v.at = time.Time{}
		transcriptLocs.m[k] = v
	}
	transcriptLocs.Unlock()
	if out := getChat(t, minime.name); out.ServedBy != titan.name {
		t.Errorf("served_by = %q once the log is back, want %q", out.ServedBy, titan.name)
	}
}

// A host that does not answer costs one search the budget and no more: it is
// reported unanswered, the next search skips it, and a hit it lands late is
// kept for the next poll.
func TestTranscriptSearchBudget(t *testing.T) {
	prev := transcriptLocateBudget
	transcriptLocateBudget = 150 * time.Millisecond
	t.Cleanup(func() { transcriptLocateBudget = prev })

	minime := newDiskHost(t, "minime", crossHostPane())
	slow := newDiskHost(t, "slow", "")
	slow.gate = make(chan struct{})
	writeClaudeLog(t, slow)
	crossHostFleet(t, minime, slow)

	start := time.Now()
	out := getChat(t, minime.name)
	if took := time.Since(start); took > time.Second {
		t.Errorf("a silent host held the chat for %v, want about the %v budget", took, transcriptLocateBudget)
	}
	want := &chatUnavailable{Reason: chatReasonNotFound, Checked: []string{minime.name}, Unanswered: []string{slow.name}}
	if !reflect.DeepEqual(out.Unavailable, want) {
		t.Errorf("unavailable = %+v, want %+v", out.Unavailable, want)
	}

	// Marked slow: the next search does not even ask (the cached miss is aged
	// out so a search runs at all).
	transcriptLocs.Lock()
	transcriptLocs.m = map[string]transcriptLocation{}
	transcriptLocs.Unlock()
	asked := slow.stats.Load()
	start = time.Now()
	out = getChat(t, minime.name)
	if took := time.Since(start); took > transcriptLocateBudget {
		t.Errorf("second search took %v, want it to skip the slow host", took)
	}
	if n := slow.stats.Load(); n != asked {
		t.Errorf("slow host asked again (%d new stats), want skipped", n-asked)
	}
	if out.Unavailable == nil || !reflect.DeepEqual(out.Unavailable.Unanswered, []string{slow.name}) {
		t.Errorf("unavailable = %+v, want the skipped host still listed as unanswered", out.Unavailable)
	}

	// The first search's probe finally answers, with the log: the cached miss
	// becomes a hit, so the next poll reads it without another search.
	close(slow.gate)
	deadline := time.Now().Add(2 * time.Second)
	for {
		transcriptLocs.Lock()
		hit := false
		for _, v := range transcriptLocs.m {
			hit = hit || v.host == slow.name
		}
		transcriptLocs.Unlock()
		if hit {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the late hit never replaced the cached miss")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if out := getChat(t, minime.name); out.ServedBy != slow.name || len(out.Items) == 0 {
		t.Errorf("served_by = %q with %d items after the late hit, want %q", out.ServedBy, len(out.Items), slow.name)
	}
}

// A better-ranked host that is silent does not stop a lower-ranked hit from
// being served once the budget is spent.
func TestTranscriptSearchServesHitPastSilentHost(t *testing.T) {
	prev := transcriptLocateBudget
	transcriptLocateBudget = 150 * time.Millisecond
	t.Cleanup(func() { transcriptLocateBudget = prev })

	minime := newDiskHost(t, "minime", crossHostPane())
	// "aaa" sorts first, so it outranks titan and is waited for — until the
	// deadline.
	silent := newDiskHost(t, "aaa", "")
	silent.gate = make(chan struct{})
	titan := newDiskHost(t, "titan", "")
	writeClaudeLog(t, titan)
	crossHostFleet(t, minime, silent, titan)

	start := time.Now()
	out := getChat(t, minime.name)
	if out.ServedBy != titan.name {
		t.Errorf("served_by = %q, want %q", out.ServedBy, titan.name)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("took %v, want about the %v budget", took, transcriptLocateBudget)
	}
}

// host=local is a single local read: no fleet list, no probe, whatever the
// result.
func TestLocalPaneIsNeverSearched(t *testing.T) {
	local := &diskHost{chatScreenBackend: &chatScreenBackend{name: "local", home: t.TempDir()}}
	called := 0
	prev := transcriptHostsFn
	transcriptHostsFn = func(context.Context) []string { called++; return []string{"titan"} }
	t.Cleanup(func() { transcriptHostsFn = prev })

	var pl struct{ Panes []pane }
	if err := json.Unmarshal([]byte(crossHostPane()), &pl); err != nil {
		t.Fatal(err)
	}
	lg := resolvePaneLog(local, pl.Panes[0], false)
	if lg.tx.Path != "" || called != 0 {
		t.Errorf("path %q, fleet listed %d times, want no search from the local host", lg.tx.Path, called)
	}
	if want := (&chatUnavailable{Reason: chatReasonNotFound, Checked: []string{"local"}}); !reflect.DeepEqual(lg.unavailable, want) {
		t.Errorf("unavailable = %+v, want %+v", lg.unavailable, want)
	}
}

// The pane listing's recency is the log's real mtime, on whichever host has it.
func TestPaneTranscriptAtFollowsSession(t *testing.T) {
	minime := newDiskHost(t, "minime", crossHostPane())
	titan := newDiskHost(t, "titan", "")
	path := writeClaudeLog(t, titan)
	crossHostFleet(t, minime, titan)
	when := time.Date(2026, 10, 7, 10, 0, 5, 0, time.UTC)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}

	var pl struct{ Panes []pane }
	if err := json.Unmarshal([]byte(crossHostPane()), &pl); err != nil {
		t.Fatal(err)
	}
	p := pl.Panes[0]
	key := transcriptPathKey(minime.name, p)
	transcriptPaths.Delete(key)
	t.Cleanup(func() { transcriptPaths.Delete(key) })

	for i := range 2 { // resolved, then from the cached host + path
		if got := paneTranscriptAt(minime, minime.name, p); got != when.UnixMilli() {
			t.Errorf("poll %d: transcript_at = %d, want titan's mtime %d", i, got, when.UnixMilli())
		}
	}
}

func TestOrderTranscriptHosts(t *testing.T) {
	hosts := []string{"norm", "minime", "local", "exa", "blackbird"}
	got := orderTranscriptHosts("minime", hosts, []string{"norm", "gone"})
	// Known holders of the cwd, then lasso's own machine, then by name; never
	// the pane's host, never a host that is not in the fleet.
	want := []string{"norm", "local", "blackbird", "exa"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}

	var many []string
	for i := range transcriptLocateMaxHosts + 5 {
		many = append(many, fmt.Sprintf("h%02d", i))
	}
	if n := len(orderTranscriptHosts("x", many, nil)); n != transcriptLocateMaxHosts {
		t.Errorf("searched %d hosts, want the cap of %d", n, transcriptLocateMaxHosts)
	}
}

// lasso's own record of where it created a worktree is what "the machine that
// hosts the pane's cwd" means when the pane's herdr is a mirror.
func TestTranscriptHomeHostsFromRecords(t *testing.T) {
	openTestDB(t)
	if err := appendAgent("gigachad", AgentRecord{ID: "a1", Title: "x", Type: "git", Agent: "claude", WorkDir: "/srv/other", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := appendAgent("local", AgentRecord{ID: "a2", Title: "y", Type: "git", Agent: "claude", WorkDir: crossHostCwd, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got := transcriptHomeHosts([]string{crossHostCwd + "/src"}, nil)
	if !reflect.DeepEqual(got, []string{"local"}) {
		t.Errorf("home hosts = %v, want [local]", got)
	}
	if got := transcriptHomeHosts(nil, nil); got != nil {
		t.Errorf("home hosts for no cwd = %v, want none", got)
	}
}

func TestPathWithin(t *testing.T) {
	for _, c := range []struct {
		dir, root string
		want      bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b/c", "/a/b", true},
		{"/a/bc", "/a/b", false},
		{"/a/b", "", false},
		{"/a/b", "relative", false},
	} {
		if got := pathWithin(c.dir, c.root); got != c.want {
			t.Errorf("pathWithin(%q, %q) = %v, want %v", c.dir, c.root, got, c.want)
		}
	}
}
