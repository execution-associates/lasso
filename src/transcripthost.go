package main

import (
	"context"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// A pane's session log is not always on the pane's machine.
//
// chatScreenPane already follows the two hops that put another machine's pane
// on screen (a selected herdr machine, an ssh attach). This is the case neither
// hop describes: herdr on one box REPORTS a pane whose agent wrote its log on
// another — a herdr machine mirroring a workspace whose agent ran on titan is
// the one seen in the field (minime listed wY0:p1 with a claude session id and
// a /home/stephan cwd, and every read of it was "This session's transcript is
// not on this host yet." while titan held the 40-row log). No hop is visible
// from the pane, so nothing resolves to the right backend; the session id is
// the only thing that travels.
//
// So when the pane names a session whose log is not on the pane's host, the
// other hosts lasso already drives are asked for it, by the session's own
// coordinates rather than by any listing: claude's id under the cwd-derived
// project slug, codex's id under its UUIDv7 day directory, omp's absolute path
// as-is. Bounded on every axis, because this runs inside a 2s chat poll and the
// pane aggregation's per-host deadline:
//
//   - ORDER. The hosts lasso knows hold the pane's cwd (its own agent records,
//     the repo warmer's listings) first, then lasso's own machine, then the rest
//     by name. Probes run in parallel; the order only decides which hit wins,
//     and a hit is taken as soon as every host ranked above it has said no.
//   - BUDGET. transcriptLocateBudget for the whole search. A host still silent
//     at the deadline is reported as unanswered and skipped by every search for
//     transcriptSlowTTL, so one wedged box cannot cost each pane the budget. Its
//     probe is left to finish, and a hit it lands late is kept for the next poll.
//   - COST. A remote host gets only the paths the session names (a stat or two,
//     or one ReadDir per codex day); the scans the local lookup falls back to
//     — every claude project dir, the codex day walk for a legacy id — run only
//     on lasso's own disk, where they cost nothing.
//   - CACHE. The answer is kept per session (transcriptPathTTL for a hit,
//     transcriptMissTTL for a miss, transcriptat.go's rules) and dropped as soon
//     as a stat of the cached path fails.
//
// The pane's own host always wins and is always asked first by the caller, so a
// log that later appears there is read there. A pane on the LOCAL host is never
// searched for: lasso's own disk is the one host every remote log would be
// copied from, and the local read stays a single read.

var (
	// transcriptLocateBudget bounds one search, end to end. A var so a test can
	// shrink it.
	transcriptLocateBudget = 1500 * time.Millisecond
	// transcriptHostsFn lists the hosts a search may ask: the set /api/all-panes
	// aggregates (and /api/hosts lists). A var so a test can stand a fleet in.
	transcriptHostsFn = transcriptCandidateHosts
)

const (
	// transcriptLocateMaxHosts caps how many hosts one search asks.
	transcriptLocateMaxHosts = 12
	// transcriptSlowTTL is how long a host that missed a search's deadline is
	// skipped (and reported as unanswered) without being asked.
	transcriptSlowTTL = 30 * time.Second
	// transcriptLateWait is how long an abandoned probe may still land a hit.
	transcriptLateWait = 30 * time.Second
)

// transcriptProbeSem bounds probes in flight across every search, so a pane
// listing full of mirrored panes cannot open a stat storm on the fleet.
var transcriptProbeSem = make(chan struct{}, 16)

// sessionRef is what names a pane's session independently of any machine.
type sessionRef struct {
	harness string
	// path is an omp session's absolute log path, tried as-is.
	path string
	// claudeID is looked up under each of dirs' project slugs.
	claudeID string
	dirs     []string
	// codexHerdrID is herdr's codex session, trusted only when its session_meta
	// was recorded in codexCwd (when the pane's codex process told us one), for
	// codexPaneTranscript's reason; codexResumeID is the `codex resume <id>`
	// argv, trusted outright.
	codexHerdrID  string
	codexResumeID string
	codexCwd      string
}

func (r sessionRef) zero() bool {
	return r.path == "" && r.claudeID == "" && r.codexHerdrID == "" && r.codexResumeID == ""
}

func (r sessionRef) key(paneHost string) string {
	return strings.Join([]string{paneHost, r.harness, r.path, r.claudeID, r.codexHerdrID, r.codexResumeID}, "\x00")
}

// paneSessionRef names the pane's session from what its own host reports. It
// mirrors paneTranscript's reading of agent_session, without the lookups.
func paneSessionRef(b Backend, p pane) sessionRef {
	s := p.AgentSession
	harness := p.Agent
	if s != nil {
		harness = s.Agent
	}
	if isCodex(harness) {
		ref := sessionRef{harness: "codex"}
		if s != nil && s.Kind == "id" {
			ref.codexHerdrID = safeSessionID(strings.TrimSpace(s.Value))
		}
		if proc, ok := codexProcess(paneForeground(b, p.PaneID)); ok {
			ref.codexCwd = filepath.Clean(proc.Cwd)
			ref.codexResumeID = codexResumeID(proc.Argv)
		}
		return ref
	}
	if s == nil {
		return sessionRef{}
	}
	agent := strings.ToLower(strings.TrimSpace(s.Agent))
	v := strings.TrimSpace(s.Value)
	switch s.Kind {
	case "path":
		if filepath.IsAbs(v) && strings.HasSuffix(v, ".jsonl") {
			return sessionRef{harness: agent, path: v}
		}
	case "id":
		if agent == "claude" {
			if id := safeSessionID(v); id != "" {
				return sessionRef{harness: agent, claudeID: id, dirs: uniqueNonEmpty(p.Cwd, p.ForegroundCwd)}
			}
		}
	}
	return sessionRef{}
}

func uniqueNonEmpty(vs ...string) []string {
	var out []string
	for _, v := range vs {
		if v != "" && !containsString(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func containsString(vs []string, s string) bool {
	for _, v := range vs {
		if v == s {
			return true
		}
	}
	return false
}

// probeTranscript looks for ref's log on one host. `thorough` allows the scans
// that are free on a local disk and a round trip per entry over SFTP.
func probeTranscript(b Backend, ref sessionRef, thorough bool) string {
	switch {
	case ref.path != "":
		if isFile(b, ref.path) {
			return ref.path
		}
	case ref.claudeID != "":
		return findClaudeTranscriptIn(b, ref.claudeID, thorough, ref.dirs...)
	case ref.harness == "codex":
		if id := ref.codexHerdrID; id != "" {
			if path := findCodexTranscriptIn(b, id, thorough); path != "" &&
				(ref.codexCwd == "" || codexSessionCwd(b, path) == ref.codexCwd) {
				return path
			}
		}
		if id := ref.codexResumeID; id != "" {
			return findCodexTranscriptIn(b, id, thorough)
		}
	}
	return ""
}

// transcriptLocation is a search's answer. host is "" when no host asked had
// the log; checked and unanswered then say who was asked.
type transcriptLocation struct {
	host string
	path string
	// checked are hosts that answered and do not have the log.
	checked []string
	// unanswered are hosts that could not be reached, missed the deadline, or
	// were skipped for having missed a recent one: the log may be on one.
	unanswered []string
	at         time.Time
}

func (l transcriptLocation) fresh() bool {
	ttl := transcriptPathTTL
	if l.host == "" {
		ttl = transcriptMissTTL
	}
	return time.Since(l.at) < ttl
}

var transcriptLocs = struct {
	sync.Mutex
	m        map[string]transcriptLocation
	inflight map[string]chan struct{}
	slow     map[string]time.Time
}{
	m:        map[string]transcriptLocation{},
	inflight: map[string]chan struct{}{},
	slow:     map[string]time.Time{},
}

// locateTranscript finds ref's log on a host other than paneHost, the pane's
// own host having already been read and found wanting. Concurrent callers for
// one session share one search.
func locateTranscript(paneHost string, ref sessionRef) transcriptLocation {
	if isLocalHost(paneHost) || ref.zero() {
		return transcriptLocation{}
	}
	key := ref.key(paneHost)
	for {
		transcriptLocs.Lock()
		if loc, ok := transcriptLocs.m[key]; ok && loc.fresh() {
			transcriptLocs.Unlock()
			return loc
		}
		if wait, ok := transcriptLocs.inflight[key]; ok {
			transcriptLocs.Unlock()
			<-wait
			continue
		}
		done := make(chan struct{})
		transcriptLocs.inflight[key] = done
		transcriptLocs.Unlock()
		loc := searchTranscript(key, paneHost, ref)
		transcriptLocs.Lock()
		delete(transcriptLocs.inflight, key)
		transcriptLocs.Unlock()
		close(done)
		return loc
	}
}

// forgetTranscriptLocation drops a cached answer whose path no longer stats.
func forgetTranscriptLocation(paneHost string, ref sessionRef) {
	transcriptLocs.Lock()
	delete(transcriptLocs.m, ref.key(paneHost))
	transcriptLocs.Unlock()
}

// storeTranscriptLocation caches loc, pruning expired answers so the map does
// not grow with every session ever seen. Called with transcriptLocs held.
func storeTranscriptLocation(key string, loc transcriptLocation) {
	if len(transcriptLocs.m) >= 512 {
		for k, v := range transcriptLocs.m {
			if !v.fresh() {
				delete(transcriptLocs.m, k)
			}
		}
	}
	transcriptLocs.m[key] = loc
}

func transcriptHostSlow(host string) bool {
	transcriptLocs.Lock()
	defer transcriptLocs.Unlock()
	until, ok := transcriptLocs.slow[host]
	if ok && time.Now().After(until) {
		delete(transcriptLocs.slow, host)
		return false
	}
	return ok
}

func setTranscriptHostSlow(host string, slow bool) {
	transcriptLocs.Lock()
	defer transcriptLocs.Unlock()
	if slow {
		transcriptLocs.slow[host] = time.Now().Add(transcriptSlowTTL)
	} else {
		delete(transcriptLocs.slow, host)
	}
}

// searchTranscript asks every candidate host for ref's log, within the budget,
// and caches the answer under key.
func searchTranscript(key, paneHost string, ref sessionRef) transcriptLocation {
	ctx, cancel := context.WithTimeout(context.Background(), transcriptLocateBudget)
	defer cancel()
	candidates := transcriptHostsFn(ctx)
	hosts := orderTranscriptHosts(paneHost, candidates, transcriptHomeHosts(ref.dirs, candidates))

	type answer struct {
		i    int
		path string
		ok   bool // false: the host could not be asked
	}
	const (
		pending int8 = iota
		absent
		found
		silent
	)
	answers := make(chan answer, len(hosts))
	state := make([]int8, len(hosts))
	paths := make([]string, len(hosts))
	waiting := 0
	for i, h := range hosts {
		if transcriptHostSlow(h) {
			state[i] = silent
			continue
		}
		waiting++
		go func(i int, h string) {
			path, ok := probeTranscriptHost(ctx, h, ref)
			answers <- answer{i, path, ok}
		}(i, h)
	}
	// decided: the best-ranked host that has not said no has said yes.
	decided := func() bool {
		for _, s := range state {
			switch s {
			case found:
				return true
			case pending:
				return false
			}
		}
		return true
	}
	expired := false
	for waiting > 0 && !decided() {
		select {
		case a := <-answers:
			waiting--
			switch {
			case !a.ok:
				state[a.i] = silent
			case a.path != "":
				state[a.i], paths[a.i] = found, a.path
			default:
				state[a.i] = absent
			}
		case <-ctx.Done():
			expired = true
		}
		if expired {
			break
		}
	}

	loc := transcriptLocation{at: time.Now()}
	for i, h := range hosts {
		switch state[i] {
		case found:
			if loc.host == "" {
				loc.host, loc.path = h, paths[i]
			}
		case absent:
			loc.checked = append(loc.checked, h)
		case silent:
			loc.unanswered = append(loc.unanswered, h)
		case pending:
			if expired {
				setTranscriptHostSlow(h, true)
			}
			loc.unanswered = append(loc.unanswered, h)
		}
	}
	if loc.host != "" {
		loc.checked, loc.unanswered = nil, nil
	}
	transcriptLocs.Lock()
	storeTranscriptLocation(key, loc)
	transcriptLocs.Unlock()

	// Probes still out are left to finish. A host answering late is no longer
	// slow, and a late hit replaces a cached miss so the next poll reads it — but
	// never a hit, which an earlier-ranked host already won.
	if waiting > 0 {
		go func(waiting int) {
			timeout := time.After(transcriptLateWait)
			for ; waiting > 0; waiting-- {
				select {
				case a := <-answers:
					if !a.ok {
						continue
					}
					setTranscriptHostSlow(hosts[a.i], false)
					if a.path == "" {
						continue
					}
					transcriptLocs.Lock()
					if cur, ok := transcriptLocs.m[key]; ok && cur.host == "" {
						storeTranscriptLocation(key, transcriptLocation{host: hosts[a.i], path: a.path, at: time.Now()})
					}
					transcriptLocs.Unlock()
				case <-timeout:
					return
				}
			}
		}(waiting)
	}
	return loc
}

// probeTranscriptHost asks one host. ok is false when the host could not be
// asked at all (no connection, or the budget ran out waiting for a slot).
func probeTranscriptHost(ctx context.Context, host string, ref sessionRef) (path string, ok bool) {
	select {
	case transcriptProbeSem <- struct{}{}:
	case <-ctx.Done():
		return "", false
	}
	defer func() { <-transcriptProbeSem }()
	b, err := namedHostBackend(host)
	if err != nil {
		return "", false
	}
	return probeTranscript(b, ref, isLocalHost(host)), true
}

// transcriptCandidateHosts is every host the pane aggregation would ask: the
// local machine, every reachable compatible herdr host, and pooled ones.
func transcriptCandidateHosts(ctx context.Context) []string {
	targets := paneTargets(localHostname(), discoverHosts(ctx, false), hostPoolHosts())
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, t.host)
	}
	return out
}

// orderTranscriptHosts is the search order: the hosts known to hold the pane's
// cwd, then lasso's own machine, then the rest by name — never the pane's own
// host, which the caller has read already.
func orderTranscriptHosts(paneHost string, hosts, preferred []string) []string {
	avail := map[string]bool{}
	for _, h := range hosts {
		if h != "" && h != paneHost {
			avail[h] = true
		}
	}
	var out []string
	take := func(h string) {
		if avail[h] {
			out = append(out, h)
			delete(avail, h)
		}
	}
	for _, h := range preferred {
		take(h)
	}
	take("local")
	rest := make([]string, 0, len(avail))
	for h := range avail {
		rest = append(rest, h)
	}
	sort.Strings(rest)
	out = append(out, rest...)
	if len(out) > transcriptLocateMaxHosts {
		out = out[:transcriptLocateMaxHosts]
	}
	return out
}

// transcriptHomeHosts names the hosts lasso already knows hold one of dirs, the
// pane's cwds — the machine an agent working there most likely ran on. Two
// sources, neither of which costs a round trip: lasso's own agent records (the
// host it created a worktree on), newest first, and the repo warmer's cached
// listings (peeked, never fetched).
func transcriptHomeHosts(dirs, hosts []string) []string {
	if len(dirs) == 0 {
		return nil
	}
	var out []string
	add := func(h string) {
		if !containsString(out, h) {
			out = append(out, h)
		}
	}
	if db != nil {
		if recs, err := listAllAgentsIncludingClosed(); err == nil {
			for i := len(recs) - 1; i >= 0; i-- {
				for _, d := range dirs {
					if pathWithin(d, recs[i].Agent.WorkDir) {
						add(recs[i].Host)
					}
				}
			}
		}
	}
	for _, h := range hosts {
		l, ok := repoCache.peek(hostCacheKey(h))
		if !ok {
			continue
		}
		for _, r := range l.repos {
			for _, d := range dirs {
				if pathWithin(d, r.Path) {
					add(h)
				}
			}
		}
	}
	return out
}

// pathWithin reports whether dir is root or below it.
func pathWithin(dir, root string) bool {
	if dir == "" || root == "" || !filepath.IsAbs(root) {
		return false
	}
	dir, root = filepath.Clean(dir), filepath.Clean(root)
	return dir == root || strings.HasPrefix(dir, root+string(filepath.Separator))
}

// paneLog is where a pane's transcript was found, on whichever host has it.
type paneLog struct {
	tx chatTranscript
	// host and be are the machine tx.Path is on — the pane's own, or the one the
	// search found it on. Empty / nil when there is no path.
	host string
	be   Backend
	info fs.FileInfo
	// unavailable explains an empty answer, machine-readably. nil when found.
	unavailable *chatUnavailable
	// pathNamed is set when herdr named the log by path and it was not on the
	// pane's host: resolving that again costs nothing, so the pane listing does
	// not cache the miss (transcriptat.go).
	pathNamed bool
}

// resolvePaneLog resolves the pane's transcript on b, the pane's own host, and
// — when the pane names a session whose log is not there — on the other hosts
// lasso drives (locateTranscript). Both the chat and the pane listing's
// transcript_at come through here, so the conversation and its recency are read
// from the same file.
func resolvePaneLog(b Backend, p pane, booting bool) paneLog {
	host := b.Name()
	tx := paneTranscript(b, p, booting)
	pathNamed := false
	if tx.Path != "" {
		info, err := b.Stat(tx.Path)
		if err == nil && !info.IsDir() {
			return paneLog{tx: tx, host: host, be: b, info: info}
		}
		// herdr named a transcript and the pane is running an agent, so this is
		// not a missing file so much as one not written yet: every harness here
		// creates its log with the first message. That is the state a freshly
		// created agent sits in until it is prompted, so it is a wait rather
		// than a dead end — unless another host has it.
		tx = chatTranscript{
			Harness:  tx.Harness,
			Note:     "The agent's transcript is not readable yet.",
			Starting: true,
			Reason:   chatReasonNotFound,
		}
		pathNamed = true
	}
	un := &chatUnavailable{Reason: tx.Reason}
	if tx.Reason != chatReasonNotFound {
		return paneLog{tx: tx, unavailable: un}
	}
	un.Checked = []string{host}
	if isLocalHost(host) {
		return paneLog{tx: tx, unavailable: un, pathNamed: pathNamed}
	}
	ref := paneSessionRef(b, p)
	loc := locateTranscript(host, ref)
	if loc.host != "" {
		if sb, err := namedHostBackend(loc.host); err == nil {
			if info, err := sb.Stat(loc.path); err == nil && !info.IsDir() {
				return paneLog{
					tx:   chatTranscript{Path: loc.path, Harness: ref.harness},
					host: loc.host, be: sb, info: info,
				}
			}
		}
		// The file moved or the host blinked: search again next time rather
		// than trusting the cached answer for its whole TTL.
		forgetTranscriptLocation(host, ref)
		un.Unanswered = []string{loc.host}
	}
	un.Checked = append(un.Checked, loc.checked...)
	un.Unanswered = append(un.Unanswered, loc.unanswered...)
	return paneLog{tx: tx, unavailable: un, pathNamed: pathNamed}
}
