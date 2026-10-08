package main

import (
	"strings"
	"sync"
	"time"
)

// transcriptAt answers "when did this agent last write to its transcript" for
// the pane listing, which is what the agents lists sort by. It runs on the
// aggregation's poll for every agent pane on every host, so the expensive half
// — RESOLVING the path, which for claude can mean probing every project dir and
// for codex a herdr process read — is cached, and a poll costs one stat per
// agent. Remote hosts pay that stat over SFTP.
//
// A resolved path is re-resolved after transcriptPathTTL anyway: codex's log is
// picked from the pane's live process and a `codex resume` swaps it without the
// pane or herdr's session changing. A miss is cached for transcriptMissTTL, so
// an agent whose log has not arrived yet is not a directory scan every poll.
//
// Resolution is resolvePaneLog's, the chat's own, so a pane whose log lives on
// another host (transcripthost.go) sorts by THAT file's mtime rather than by
// nothing; the entry remembers which host the path is on.
const (
	transcriptPathTTL = time.Minute
	transcriptMissTTL = 30 * time.Second
)

type transcriptPathEntry struct {
	// host is the machine path is on: the pane's own, or the one the cross-host
	// search found it on (resolvePaneLog), so recency is the log's real mtime
	// wherever it lives.
	host string
	path string
	at   time.Time
}

var transcriptPaths sync.Map // transcriptPathKey -> transcriptPathEntry

func transcriptPathKey(host string, p pane) string {
	k := host + "\x00" + p.PaneID + "\x00" + p.Agent
	if s := p.AgentSession; s != nil {
		k += "\x00" + s.Agent + "\x00" + s.Kind + "\x00" + s.Value
	}
	return k
}

// paneTranscriptAt is the transcript's mtime in unix milliseconds, 0 when the
// pane has no readable transcript.
func paneTranscriptAt(b Backend, host string, p pane) int64 {
	key := transcriptPathKey(host, p)
	if v, ok := transcriptPaths.Load(key); ok {
		e := v.(transcriptPathEntry)
		ttl := transcriptPathTTL
		if e.path == "" {
			ttl = transcriptMissTTL
		}
		if time.Since(e.at) < ttl {
			if e.path == "" {
				return 0
			}
			sb := b
			if e.host != b.Name() {
				var err error
				if sb, err = namedHostBackend(e.host); err != nil {
					transcriptPaths.Delete(key)
					return 0
				}
			}
			fi, err := sb.Stat(e.path)
			if err != nil {
				// The file moved (codex archives a session) or the host blinked:
				// resolve again next poll rather than trusting the cached path for
				// a minute.
				transcriptPaths.Delete(key)
				return 0
			}
			return fi.ModTime().UnixMilli()
		}
	}
	lg := resolvePaneLog(b, p, false)
	if lg.tx.Path == "" {
		// A log herdr named by path costs nothing to look for again, so only a
		// lookup's miss is cached — the cross-host search keeps its own.
		if lg.pathNamed {
			transcriptPaths.Delete(key)
		} else {
			transcriptPaths.Store(key, transcriptPathEntry{at: time.Now()})
		}
		return 0
	}
	transcriptPaths.Store(key, transcriptPathEntry{host: lg.host, path: lg.tx.Path, at: time.Now()})
	return lg.info.ModTime().UnixMilli()
}

// pruneTranscriptPaths drops cache entries for panes a host no longer lists, so
// the map does not grow with every pane ever seen.
func pruneTranscriptPaths(host string, live map[string]bool) {
	prefix := host + "\x00"
	transcriptPaths.Range(func(k, _ any) bool {
		key := k.(string)
		if strings.HasPrefix(key, prefix) && !live[key] {
			transcriptPaths.Delete(k)
		}
		return true
	})
}
