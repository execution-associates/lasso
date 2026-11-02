package main

import (
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

const profileHome = "/home/u"

// profilesDir is the host's ~/.omp/profiles in the fake.
var profilesDir = filepath.Join(profileHome, ".omp", "profiles")

// profileFiles gives a memBackend-based fake a home directory and file reads,
// the calls profile discovery and the staged overlay make on the agent's host.
type profileFiles struct{ fileMap map[string]string }

func (h profileFiles) HomeDir() (string, error) { return profileHome, nil }
func (h profileFiles) ReadFile(p string) ([]byte, error) {
	if c, ok := h.fileMap[p]; ok {
		return []byte(c), nil
	}
	return nil, fs.ErrNotExist
}

type profileMem struct {
	*memBackend
	profileFiles
}

// newProfileMem is a host whose ~/.omp/profiles holds every directory in the
// brief's shape: usable profiles (an agent/ dir), `default` (reserved), one
// without an agent/ dir, one with its agent/ only nested a level deeper, and
// a stray file.
func newProfileMem() *profileMem {
	m := newMemBackend()
	for _, name := range []string{"cyber", "claude", "chinese", "default"} {
		m.addFile(filepath.Join(profilesDir, name, "agent", "config.yml"), "modelRoles: {}\n")
	}
	m.addFile(filepath.Join(profilesDir, "noagent", "notes.md"), "x")
	m.addFile(filepath.Join(profilesDir, "nested", "deep", "agent", "config.yml"), "x")
	m.addFile(filepath.Join(profilesDir, "stray"), "x")
	return &profileMem{memBackend: m, profileFiles: profileFiles{m.files}}
}

// The creator lists the directories of ~/.omp/profiles that hold an agent/
// directory, sorted, `default` left out, for omp only.
func TestHostHarnessesListsOmpProfiles(t *testing.T) {
	var omp harnessDef
	for _, h := range hostHarnesses(newProfileMem()) {
		if h.ID == "omp" {
			omp = h
		} else if len(h.ModelProfiles) != 0 {
			t.Errorf("%s offers profiles %q; only omp has them", h.ID, h.ModelProfiles)
		}
	}
	if got := strings.Join(omp.ModelProfiles, ","); got != "chinese,claude,cyber" {
		t.Errorf("omp profiles = %q, want chinese,claude,cyber", got)
	}
	empty := &profileMem{memBackend: newMemBackend()}
	empty.profileFiles = profileFiles{empty.files}
	if got := hostHarnesses(empty); &got[0] != &harnesses[0] {
		t.Errorf("a host without profiles or an omp config must serve the compiled-in registry")
	}
}

// A refused profile names what the host does have, so the caller can retry;
// `default` is refused by name even though a directory of that name exists.
func TestOmpProfileCheckErrors(t *testing.T) {
	b := newProfileMem()
	if err := ompProfileCheck(b, "cyber"); err != nil {
		t.Errorf("cyber is a profile: %v", err)
	}
	err := ompProfileCheck(b, "nope")
	if err == nil || !strings.Contains(err.Error(), "chinese, claude, cyber") {
		t.Errorf("unknown profile error = %v, want the available profiles listed", err)
	}
	for _, name := range []string{"noagent", "nested"} {
		if ompProfileCheck(b, name) == nil {
			t.Errorf("%s has no agent/ directory and must be refused", name)
		}
	}
	err = ompProfileCheck(b, "default")
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("default must be refused as reserved, got %v", err)
	}
	none := &profileMem{memBackend: newMemBackend()}
	none.profileFiles = profileFiles{none.files}
	if err := ompProfileCheck(none, "cyber"); err == nil || !strings.Contains(err.Error(), "no profiles") {
		t.Errorf("host without profiles: err = %v", err)
	}
}

// The profile is a launch flag after --yolo and before --config, and only omp's
// line has it.
func TestAgentCommandProfile(t *testing.T) {
	cmd := agentCommand("omp", launchOpts{profile: "cyber", configOverlay: "/o.yml", model: "m", prompt: "go"})
	if !strings.HasPrefix(cmd, "omp --yolo --profile 'cyber' --config '/o.yml'") {
		t.Errorf("omp line = %q, want --profile between --yolo and --config", cmd)
	}
	if strings.Contains(agentCommand("omp", launchOpts{prompt: "go"}), "--profile") {
		t.Error("no profile, no --profile")
	}
	for _, agent := range []string{"claude", "codex", "opencode", "pi"} {
		if strings.Contains(agentCommand(agent, launchOpts{profile: "cyber", prompt: "go"}), "--profile") {
			t.Errorf("%s must never get --profile", agent)
		}
	}
}

type profileBootFake struct {
	*ompPlanBootFake
	profileFiles
}

// End to end through bootAgent: the profile is on the launch line next to an
// explicit model and effort (omp ranks those above every config layer), the
// staged overlay carries no roles, and a profile gone by boot time fails the
// boot rather than launching on ~/.omp/agent under a record naming it.
func TestBootAgentLaunchesWithProfile(t *testing.T) {
	m := newProfileMem()
	b := &profileBootFake{ompPlanBootFake: &ompPlanBootFake{memBackend: m.memBackend}}
	b.profileFiles = profileFiles{m.files}
	rec := AgentRecord{
		ID: "ompprofile1", Host: "local", Type: "scratch", Agent: "omp",
		Title: "Profile", Description: "go", ModelProfile: "cyber",
		Model: "deepseek/deepseek-v4-flash", Effort: "low",
		WorkDir: "/work", RootPane: "p1",
	}
	t.Setenv("LASSO_DIR", t.TempDir())
	if err := openDB(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeTestDB)
	if err := appendAgent("local", rec); err != nil {
		t.Fatal(err)
	}
	bootAgent(b, "local", rec, "")

	launch := b.launchLine(t)
	for _, want := range []string{"--profile 'cyber'", "--model 'deepseek/deepseek-v4-flash'", "--thinking 'low'"} {
		if !strings.Contains(launch, want) {
			t.Errorf("launch line lacks %s: %q", want, launch)
		}
	}
	staged := b.files[ompConfigPath(b, rec.ID)]
	if strings.Contains(staged, "modelRoles") || strings.Contains(staged, "defaultThinkingLevel") {
		t.Errorf("the overlay must not carry roles; the profile's config.yml does:\n%s", staged)
	}
	if !strings.Contains(staged, "dark: "+ompThemeName) {
		t.Errorf("theme pin missing from the overlay:\n%s", staged)
	}

	gone := rec
	gone.ID, gone.ModelProfile = "ompprofile2", "deleted"
	_ = appendAgent("local", gone)
	m2 := newMemBackend()
	b2 := &profileBootFake{ompPlanBootFake: &ompPlanBootFake{memBackend: m2}}
	b2.profileFiles = profileFiles{m2.files}
	bootAgent(b2, "local", gone, "")
	got, err := findAgentRecordAny("local", gone.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BootStatus != BootFailed || !strings.Contains(got.BootError, "deleted") {
		t.Errorf("boot with a missing profile = %s %q, want failed", got.BootStatus, got.BootError)
	}
}

type profileCreateBackend struct {
	*createAgentBackend
	profileFiles
}

// The profile an omp agent was created with is persisted on the record,
// remembered per host and served back as last_model_profile — none included,
// so choosing None sticks; a create for another harness drops it and leaves
// the memory alone; an unknown profile or `default` is refused before anything
// is created.
func TestCreateAgentRemembersModelProfile(t *testing.T) {
	t.Setenv("LASSO_DIR", t.TempDir())
	if err := openDB(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeTestDB)
	m := newProfileMem()
	b := &profileCreateBackend{createAgentBackend: &createAgentBackend{memBackend: m.memBackend}}
	b.profileFiles = profileFiles{m.files}
	prev := defaultBackend()
	setDefaultBackend(b)
	t.Cleanup(func() { setDefaultBackend(prev) })

	last := func() string {
		t.Helper()
		c, err := loadLassoConfig("local")
		if err != nil {
			t.Fatal(err)
		}
		return c.LastModelProfile
	}
	create := func(agent, profile string) (AgentRecord, error) {
		return createAgent(b, createAgentReq{
			Type: "git", Repo: "/repo/app", BaseBranch: "main", Prompt: "x",
			Agent: agent, ModelProfile: profile,
		})
	}

	rec, err := create("omp", "cyber")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ModelProfile != "cyber" || last() != "cyber" {
		t.Errorf("record %q / last %q, want cyber", rec.ModelProfile, last())
	}
	stored, err := findAgentRecordAny("local", rec.ID)
	if err != nil || stored.ModelProfile != "cyber" {
		t.Errorf("stored record = %q (%v), want cyber", stored.ModelProfile, err)
	}
	if rec, _ := create("claude", "chinese"); rec.ModelProfile != "" || last() != "cyber" {
		t.Errorf("claude create: record %q, last %q — must drop the profile and keep the last", rec.ModelProfile, last())
	}
	for _, name := range []string{"nope", "default"} {
		_, err = create("omp", name)
		var ce *createErr
		if err == nil || !asCreateErr(err, &ce) || ce.code != http.StatusBadRequest {
			t.Errorf("profile %q: err %v, want a 400", name, err)
		}
	}
	if _, err := create("omp", "nope"); err == nil || !strings.Contains(err.Error(), "chinese, claude, cyber") {
		t.Errorf("refusal must list the host's profiles: %v", err)
	}
	if last() != "cyber" {
		t.Errorf("a refused create must not move the last profile: %q", last())
	}
	if _, err := create("omp", ""); err != nil || last() != "" {
		t.Errorf("an omp create with none must remember none: err %v, last %q", err, last())
	}
}

func asCreateErr(err error, out **createErr) bool {
	ce, ok := err.(*createErr)
	*out = ce
	return ok
}

// Old preset choices carry over once: `default` becomes `claude`, a name the
// host has as a profile stays, anything else is cleared, and a later None is
// not undone by a second pass.
func TestBackfillModelProfiles(t *testing.T) {
	t.Setenv("LASSO_DIR", t.TempDir())
	if err := openDB(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeTestDB)
	for id, preset := range map[string]string{"a1": "default", "a2": "cyber", "a3": "smut", "a4": ""} {
		if err := appendAgent("local", AgentRecord{ID: id, Host: "local", Type: "scratch", Agent: "omp"}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE agents SET model_preset=? WHERE id=?`, preset, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := upsertHostField("local", "last_model_preset", "default"); err != nil {
		t.Fatal(err)
	}

	backfillModelProfiles("local", newProfileMem())
	want := map[string]string{"a1": "claude", "a2": "cyber", "a3": "", "a4": ""}
	for id, w := range want {
		rec, err := findAgentRecordAny("local", id)
		if err != nil || rec.ModelProfile != w {
			t.Errorf("agent %s profile = %q (%v), want %q", id, rec.ModelProfile, err, w)
		}
	}
	if hs, _ := getHostState("local"); hs.LastModelProfile != "claude" {
		t.Errorf("last profile = %q, want claude", hs.LastModelProfile)
	}

	_ = setLastModelProfile("local", "")
	backfillModelProfiles("local", newProfileMem())
	if hs, _ := getHostState("local"); hs.LastModelProfile != "" {
		t.Errorf("a second pass resurrected the old preset: %q", hs.LastModelProfile)
	}
}
