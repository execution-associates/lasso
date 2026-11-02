package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// omp profiles: the creator's "Profile" select.
//
// A profile is a whole isolated omp agent dir, `~/.omp/profiles/<name>/agent/`,
// that `omp --profile <name>` uses in place of `~/.omp/agent`. Its own
// config.yml holds the model roles and defaultThinkingLevel a model preset used
// to carry, so a profile already IS that bundle — plus its own sessions,
// history, credentials and prompt. lasso therefore launches one with the flag
// and nothing else: no `--config` overlay, no copy of the roles, so the profile
// is what omp reads and not a second carrier of the same keys that only worked
// for one run (the preset overlay this replaced).
//
// `--model` and `--thinking` are runtime overrides above every config layer, so
// the creator's explicit model and effort still win for the main session while
// every other role keeps the profile's — the rule the preset had.
//
// omp reads the profile named `default` as `~/.omp/agent` itself, so
// `--profile default` never reaches `~/.omp/profiles/default/agent`. A directory
// of that name is not a usable profile: it is left out of discovery and refused
// by name, rather than launched as something other than what the record says.

// ompDefaultProfile is the name omp reserves for ~/.omp/agent.
const ompDefaultProfile = "default"

func ompProfilesDir(home string) string { return filepath.Join(home, ".omp", "profiles") }

// ompProfileNames lists the profiles host b has under home, sorted: each
// directory of ~/.omp/profiles that holds an `agent` directory, `default`
// aside. A host without the directory (or one that can't be read) has none.
// The agent directory is Stat-ed rather than trusted from the listing, so a
// profile that is a symlink to a directory elsewhere counts as the directory
// omp will follow it to.
func ompProfileNames(b Backend, home string) []string {
	dir := ompProfilesDir(home)
	entries, err := b.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.Name == ompDefaultProfile {
			continue
		}
		if fi, err := b.Stat(filepath.Join(dir, e.Name, "agent")); err == nil && fi.IsDir() {
			out = append(out, e.Name)
		}
	}
	sort.Strings(out)
	return out
}

// ompProfileCheck says whether name is a profile host b's omp can launch with.
// An error means it is not: the caller must not launch as though it were, since
// an agent recorded as running a profile it did not launch with is the lie this
// check exists to prevent.
func ompProfileCheck(b Backend, name string) error {
	// The host is left out of these messages: they reach /mcp too, where lasso's
	// own box must be named by its hostname (mcphost.go), and every caller
	// already knows which host it asked.
	if name == ompDefaultProfile {
		return fmt.Errorf("omp profile %q is reserved: omp reads it as ~/.omp/agent itself, so leave the profile empty for the host's own setup", name)
	}
	home, err := b.HomeDir()
	if err != nil || home == "" {
		return fmt.Errorf("omp profile %q: the host has no home directory", name)
	}
	have := ompProfileNames(b, home)
	for _, p := range have {
		if p == name {
			return nil
		}
	}
	if len(have) == 0 {
		return fmt.Errorf("omp profile %q not found: the host has no profiles under ~/.omp/profiles", name)
	}
	return fmt.Errorf("omp profile %q not found (profiles on the host: %s)", name, strings.Join(have, ", "))
}

// normalizeModelProfile keeps a profile only for a harness that can take one —
// omp, the only one with `--profile`. Same rule as normalizeAdvisor: the record
// must not claim a profile that never applied.
func normalizeModelProfile(agent, profile string) string {
	if !harnessByID(agent).stagesConfigOverlay {
		return ""
	}
	return strings.TrimSpace(profile)
}

// backfillModelProfiles carries host's old model-preset choices over to the
// profile columns, once per host, so an existing fleet keeps the choice it made.
// The presets `cyber` and `chinese` became profiles of the same name and
// `default` (the anthropic stack) became `claude`; a value that then names no
// profile on the host is cleared rather than copied, since a record claiming a
// profile the host does not have is worse than one claiming none. The check is
// ompProfileNames, the one the create path validates with.
//
// It needs the host's own backend, so it cannot run in openDB (a remote host
// is not reachable yet there); hostAgentConfig runs it the first time a host's
// creator config is read. A marker in settings makes it one-shot: without it a
// creator that later picks None would see the stale last_model_preset come
// back. The old columns are left as they were, readable by an older lasso.
// An unreachable host (no home directory) is retried next time.
func backfillModelProfiles(host string, b Backend) {
	key := "model_profile_backfill:" + host
	if done, _ := getSetting(key); done != "" {
		return
	}
	home, err := b.HomeDir()
	if err != nil || home == "" {
		return
	}
	have := map[string]bool{}
	for _, p := range ompProfileNames(b, home) {
		have[p] = true
	}
	carry := func(preset string) string {
		if preset == ompDefaultProfile {
			preset = "claude"
		}
		if have[preset] {
			return preset
		}
		return ""
	}
	rows, err := db.Query(`SELECT id, model_preset FROM agents WHERE host=? AND model_profile='' AND model_preset<>''`, host)
	if err != nil {
		return
	}
	type fix struct{ id, profile string }
	var fixes []fix
	for rows.Next() {
		var id, preset string
		if rows.Scan(&id, &preset) == nil {
			if p := carry(preset); p != "" {
				fixes = append(fixes, fix{id, p})
			}
		}
	}
	rows.Close()
	for _, f := range fixes {
		if _, err := db.Exec(`UPDATE agents SET model_profile=? WHERE host=? AND id=?`, f.profile, host, f.id); err != nil {
			return
		}
	}
	var last string
	if db.QueryRow(`SELECT last_model_preset FROM host_state WHERE host=? AND last_model_profile=''`, host).Scan(&last) == nil {
		if p := carry(last); p != "" {
			if setLastModelProfile(host, p) != nil {
				return
			}
		}
	}
	_ = setSetting(key, "1")
}
