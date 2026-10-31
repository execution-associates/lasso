package main

import (
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const presetHome = "/home/u"

// presetGlobal is a global omp config.yml with two presets and global roles a
// preset can leave out.
const presetGlobal = `modelRoles:
  default: anthropic/claude-opus-5-5:max
  vision: anthropic/claude-sonnet-5-5
  web: web/exa
modelPresets:
  cyber:
    defaultThinkingLevel: max
    modelRoles:
      default: refuseless/deepseek-v4.1-flash-abliterated:max
      plan: "@slow"
      slow: refuseless/glm-5.3-abliterated:max
  hidden: null
  chinese:
    modelRoles:
      default: deepseek/deepseek-v4.1-flash:max
`

// homeFiles gives a memBackend-based fake a home directory and file reads, the
// two calls preset resolution makes on the agent's host.
type homeFiles struct{ fileMap map[string]string }

func (h homeFiles) HomeDir() (string, error) { return presetHome, nil }
func (h homeFiles) ReadFile(p string) ([]byte, error) {
	if c, ok := h.fileMap[p]; ok {
		return []byte(c), nil
	}
	return nil, fs.ErrNotExist
}

type presetMem struct {
	*memBackend
	homeFiles
}

func newPresetMem(global string) *presetMem {
	m := newMemBackend()
	if global != "" {
		m.addFile(filepath.Join(presetHome, ".omp", "agent", "config.yml"), global)
	}
	return &presetMem{memBackend: m, homeFiles: homeFiles{m.files}}
}

// overlayRoles decodes an overlay the way omp reads it: the roles it sets
// (nil value = an explicit null) and its defaultThinkingLevel.
func overlayRoles(t *testing.T, body []byte) (map[string]*string, string) {
	t.Helper()
	var doc struct {
		ModelRoles           map[string]*string `yaml:"modelRoles"`
		DefaultThinkingLevel string             `yaml:"defaultThinkingLevel"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		t.Fatalf("overlay is not valid YAML: %v\n%s", err, body)
	}
	return doc.ModelRoles, doc.DefaultThinkingLevel
}

// The creator lists the presets the host's omp config defines, in config
// order; a null entry (how a higher layer hides a preset) is not one.
func TestHostHarnessesListsOmpPresets(t *testing.T) {
	b := newPresetMem(presetGlobal)
	var omp harnessDef
	for _, h := range hostHarnesses(b) {
		if h.ID == "omp" {
			omp = h
		} else if len(h.ModelPresets) != 0 {
			t.Errorf("%s offers presets %q; only omp has them", h.ID, h.ModelPresets)
		}
	}
	if got := strings.Join(omp.ModelPresets, ","); got != "cyber,chinese" {
		t.Errorf("omp presets = %q, want cyber,chinese", got)
	}
	if got := hostHarnesses(newPresetMem("")); &got[0] != &harnesses[0] {
		t.Errorf("a host without an omp config must serve the compiled-in registry")
	}
}

// Applying a preset makes the agent's roles exactly the preset's: its own
// roles verbatim (an @alias survives the YAML round trip as a string), a null
// for each role the user's config assigns that the preset leaves out — which
// is what /modelpreset switch does by clearing them — and its thinking level.
func TestOmpPresetOverlayIsTheWholePreset(t *testing.T) {
	body, err := ompPresetOverlay(newPresetMem(presetGlobal), "", "cyber")
	if err != nil {
		t.Fatal(err)
	}
	roles, thinking := overlayRoles(t, body)
	want := map[string]string{
		"default": "refuseless/deepseek-v4.1-flash-abliterated:max",
		"plan":    "@slow",
		"slow":    "refuseless/glm-5.3-abliterated:max",
	}
	for r, sel := range want {
		if roles[r] == nil || *roles[r] != sel {
			t.Errorf("role %s = %v, want %q", r, roles[r], sel)
		}
	}
	for _, r := range []string{"vision", "web"} {
		v, ok := roles[r]
		if !ok || v != nil {
			t.Errorf("role %s left out of the preset must be nulled, got present=%v value=%v", r, ok, v)
		}
	}
	if len(roles) != len(want)+2 {
		t.Errorf("overlay sets roles %v, want only the preset's and the nulls", roles)
	}
	if thinking != "max" {
		t.Errorf("defaultThinkingLevel = %q, want max", thinking)
	}

	// A preset without a thinking level leaves the user's alone.
	body, err = ompPresetOverlay(newPresetMem(presetGlobal), "", "chinese")
	if err != nil {
		t.Fatal(err)
	}
	if _, thinking := overlayRoles(t, body); thinking != "" {
		t.Errorf("chinese sets no thinking level, overlay has %q", thinking)
	}
}

// omp resolves a preset whole from the highest layer defining it: the project
// config's preset of the same name replaces the global one (no merging), and a
// null project entry is ignored. A role only the project assigns is nulled too.
func TestOmpPresetOverlayProjectLayer(t *testing.T) {
	b := newPresetMem(presetGlobal)
	b.addFile("/repo/.omp/config.yml", `modelRoles:
  task: zai/glm-5.3
modelPresets:
  cyber:
    modelRoles:
      default: openai-codex/gpt-5.6-sol
  chinese: null
`)
	body, err := ompPresetOverlay(b, "/repo", "cyber")
	if err != nil {
		t.Fatal(err)
	}
	roles, thinking := overlayRoles(t, body)
	if roles["default"] == nil || *roles["default"] != "openai-codex/gpt-5.6-sol" {
		t.Errorf("project cyber must win: default = %v", roles["default"])
	}
	if v, ok := roles["slow"]; ok && v != nil {
		t.Errorf("global cyber's slow leaked into the project preset: %q", *v)
	}
	if v, ok := roles["task"]; !ok || v != nil {
		t.Errorf("a project-only role the preset omits must be nulled, got %v", v)
	}
	if thinking != "" {
		t.Errorf("project cyber sets no thinking level; got %q from the global one", thinking)
	}

	body, err = ompPresetOverlay(b, "/repo", "chinese")
	if err != nil {
		t.Fatalf("a null project entry must fall through to the global preset: %v", err)
	}
	if roles, _ := overlayRoles(t, body); roles["default"] == nil || *roles["default"] != "deepseek/deepseek-v4.1-flash:max" {
		t.Errorf("chinese default = %v, want the global preset's", roles["default"])
	}
}

func TestOmpPresetOverlayUnknownNamesDefined(t *testing.T) {
	_, err := ompPresetOverlay(newPresetMem(presetGlobal), "", "hidden")
	if err == nil || !strings.Contains(err.Error(), "cyber, chinese") {
		t.Errorf("a null global entry is no preset, and the error must list the real ones: %v", err)
	}
	if _, err := ompPresetOverlay(newPresetMem(""), "", "cyber"); err == nil {
		t.Error("a host without an omp config has no presets")
	}
}

// End to end through bootAgent: the preset rides in the staged overlay, and an
// explicit model and effort still go on the launch line, where omp ranks them
// above every config layer.
func TestBootAgentStagesOmpPreset(t *testing.T) {
	b := &presetBootFake{ompPlanBootFake: &ompPlanBootFake{memBackend: newMemBackend()}}
	b.addFile(filepath.Join(presetHome, ".omp", "agent", "config.yml"), presetGlobal)
	b.homeFiles = homeFiles{b.files}
	rec := AgentRecord{
		ID: "omppreset1", Host: "local", Type: "scratch", Agent: "omp",
		Title: "Preset", Description: "go", ModelPreset: "cyber",
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

	staged := b.files[ompConfigPath(b, rec.ID)]
	roles, thinking := overlayRoles(t, []byte(staged))
	if roles["default"] == nil || *roles["default"] != "refuseless/deepseek-v4.1-flash-abliterated:max" || thinking != "max" {
		t.Errorf("staged overlay lacks the preset:\n%s", staged)
	}
	if !strings.Contains(staged, "dark: "+ompThemeName) {
		t.Errorf("the preset must not displace the theme pin:\n%s", staged)
	}
	launch := b.launchLine(t)
	for _, want := range []string{"--model 'deepseek/deepseek-v4-flash'", "--thinking 'low'"} {
		if !strings.Contains(launch, want) {
			t.Errorf("launch line lacks %s: %q", want, launch)
		}
	}

	// A preset gone by boot time fails the boot rather than launching on the
	// user's own roles under a record that names the preset.
	gone := rec
	gone.ID, gone.ModelPreset = "omppreset2", "deleted"
	_ = appendAgent("local", gone)
	b2 := &presetBootFake{ompPlanBootFake: &ompPlanBootFake{memBackend: newMemBackend()}}
	b2.homeFiles = homeFiles{b2.files}
	bootAgent(b2, "local", gone, "")
	got, err := findAgentRecordAny("local", gone.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BootStatus != BootFailed || !strings.Contains(got.BootError, "deleted") {
		t.Errorf("boot with a missing preset = %s %q, want failed", got.BootStatus, got.BootError)
	}
}

type presetBootFake struct {
	*ompPlanBootFake
	homeFiles
}

// The last preset an omp agent was created with is remembered per host and
// served back as last_model_preset — none included, so choosing None sticks;
// a create for another harness leaves it alone; an unknown preset is refused
// before anything is created.
func TestCreateAgentRemembersModelPreset(t *testing.T) {
	t.Setenv("LASSO_DIR", t.TempDir())
	if err := openDB(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeTestDB)
	b := &presetCreateBackend{createAgentBackend: &createAgentBackend{memBackend: newMemBackend()}}
	b.addFile(filepath.Join(presetHome, ".omp", "agent", "config.yml"), presetGlobal)
	b.homeFiles = homeFiles{b.files}
	prev := defaultBackend()
	setDefaultBackend(b)
	t.Cleanup(func() { setDefaultBackend(prev) })

	last := func() string {
		t.Helper()
		c, err := loadLassoConfig("local")
		if err != nil {
			t.Fatal(err)
		}
		return c.LastModelPreset
	}
	create := func(agent, preset string) (AgentRecord, error) {
		return createAgent(b, createAgentReq{
			Type: "git", Repo: "/repo/app", BaseBranch: "main", Prompt: "x",
			Agent: agent, ModelPreset: preset,
		})
	}

	rec, err := create("omp", "cyber")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ModelPreset != "cyber" || last() != "cyber" {
		t.Errorf("record %q / last %q, want cyber", rec.ModelPreset, last())
	}
	if rec, _ := create("claude", "chinese"); rec.ModelPreset != "" || last() != "cyber" {
		t.Errorf("claude create: record %q, last %q — must drop the preset and keep the last", rec.ModelPreset, last())
	}
	_, err = create("omp", "nope")
	var ce *createErr
	if err == nil || !asCreateErr(err, &ce) || ce.code != http.StatusBadRequest {
		t.Errorf("unknown preset: err %v, want a 400", err)
	}
	if last() != "cyber" {
		t.Errorf("a refused create must not move the last preset: %q", last())
	}
	if _, err := create("omp", ""); err != nil || last() != "" {
		t.Errorf("an omp create with none must remember none: err %v, last %q", err, last())
	}
}

type presetCreateBackend struct {
	*createAgentBackend
	homeFiles
}

func asCreateErr(err error, out **createErr) bool {
	ce, ok := err.(*createErr)
	*out = ce
	return ok
}
