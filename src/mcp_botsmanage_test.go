package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateBotSwitchesRuntime(t *testing.T) {
	useBotTestEnv(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# exa\n\nBe useful.\n"), 0o644)
	rec := &botRecord{Name: "exa", Runtime: botRuntimeOmp, Dir: dir, Model: "deepseek/deepseek-v4-pro", Effort: "auto", PermissionMode: "yolo",
		MCP: []botMCPServer{{Name: "mail", Type: "http", URL: "https://mail.example/mcp"}}, KeepRunning: true, Stopped: true}
	if err := rec.normalize(); err != nil {
		t.Fatal(err)
	}
	if err := insertBot(rec); err != nil {
		t.Fatal(err)
	}
	setBotSession("exa", "omp-session")
	claude := botRuntimeClaude
	_, out, err := updateBotTool(context.Background(), nil, updateBotIn{Name: "exa", Runtime: &claude})
	if err != nil || !out.OK || out.Note == "" {
		t.Fatalf("update_bot runtime: %v %+v", err, out)
	}
	got, _ := getBot("exa")
	if got.Runtime != botRuntimeClaude || got.Model != "" || got.Effort != "" || got.PermissionMode != "" {
		t.Errorf("runtime switch kept the other runtime's names: %+v", got)
	}
	if len(got.MCP) != 1 || got.MCP[0].Name != "mail" || !got.KeepRunning || got.LastSessionID != "" {
		t.Errorf("runtime switch lost settings or kept the session: %+v", got)
	}
	if md, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md")); string(md) != "@AGENTS.md\n" {
		t.Errorf("CLAUDE.md = %q, want an import of AGENTS.md", md)
	}
	task, _ := os.ReadFile(botTaskPath(dir))
	if !bytes.Contains(task, []byte("'--mcp-config'")) || !bytes.Contains(task, []byte("hasCompletedOnboarding")) {
		t.Errorf("task is not claude's:\n%s", task)
	}
	if mcpj, err := os.ReadFile(botMCPPath(dir)); err != nil || !bytes.Contains(mcpj, []byte("mail.example")) {
		t.Errorf("claude mcp.json lacks the bot's server: %v %s", err, mcpj)
	}

	// Values passed in the same call win over the reset; a level both
	// runtimes have survives.
	omp, model, effort := botRuntimeOmp, "anthropic/claude-opus-5", "high"
	if _, _, err := updateBotTool(context.Background(), nil, updateBotIn{Name: "exa", Effort: &effort}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := updateBotTool(context.Background(), nil, updateBotIn{Name: "exa", Runtime: &omp, Model: &model}); err != nil {
		t.Fatal(err)
	}
	got, _ = getBot("exa")
	if got.Runtime != botRuntimeOmp || got.Model != model || got.Effort != "high" {
		t.Errorf("switch back: %+v", got)
	}
	if md, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md")); !bytes.Contains(md, []byte("Be useful.")) {
		t.Errorf("AGENTS.md was replaced: %q", md)
	}
	bad := "codex"
	if _, _, err := updateBotTool(context.Background(), nil, updateBotIn{Name: "exa", Runtime: &bad}); err == nil {
		t.Error("an unknown runtime was accepted")
	}
}

func TestSeedInstructionsFromOtherRuntime(t *testing.T) {
	b := &localBackend{}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# old\n\nrules\n"), 0o644)
	r := &botRecord{Name: "x", Runtime: botRuntimeOmp}
	if err := botSeedInstructionsFile(b, r, dir); err != nil {
		t.Fatal(err)
	}
	if md, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md")); string(md) != "# old\n\nrules\n" {
		t.Errorf("AGENTS.md = %q, want CLAUDE.md's text", md)
	}
	// A CLAUDE.md that only imports AGENTS.md is not copied back into it.
	dir2 := t.TempDir()
	os.WriteFile(filepath.Join(dir2, "CLAUDE.md"), []byte("@AGENTS.md\n"), 0o644)
	if err := botSeedInstructionsFile(b, r, dir2); err != nil {
		t.Fatal(err)
	}
	if md, _ := os.ReadFile(filepath.Join(dir2, "AGENTS.md")); bytes.Contains(md, []byte("@AGENTS.md")) {
		t.Errorf("AGENTS.md imports itself: %q", md)
	}
}

// The onboarding seed in the claude task: run it against each shape
// ~/.claude.json can have and check the result is JSON with the key set and
// nothing else lost.
func TestBotTaskSeedsClaudeOnboarding(t *testing.T) {
	script := botTaskScript(&botRecord{Name: "x", Runtime: botRuntimeClaude}, "/tmp/x", nil)
	start := strings.Index(script, `cfg="`)
	end := strings.Index(script[start:], "\nfi\n")
	if start < 0 || end < 0 {
		t.Fatalf("no onboarding seed in the task:\n%s", script)
	}
	seed := script[start : start+end+4]
	for name, tc := range map[string]struct{ in, keep string }{
		"absent": {"", ""},
		"empty":  {"{}\n", ""},
		"pretty": {"{\n  \"theme\": \"dark\",\n  \"projects\": {\"/a\": {}}\n}\n", "theme"},
		"set":    {"{\"hasCompletedOnboarding\": false}\n", ""},
	} {
		home := t.TempDir()
		cfg := filepath.Join(home, ".claude.json")
		if tc.in != "" {
			os.WriteFile(cfg, []byte(tc.in), 0o600)
		}
		cmd := exec.Command("sh", "-c", seed)
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v %s", name, err, out)
		}
		raw, _ := os.ReadFile(cfg)
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s: not JSON after the seed: %v %q", name, err, raw)
		}
		if name == "set" {
			if m["hasCompletedOnboarding"] != false {
				t.Errorf("set: an existing value was changed: %q", raw)
			}
			continue
		}
		if m["hasCompletedOnboarding"] != true {
			t.Errorf("%s: key not set: %q", name, raw)
		}
		if tc.keep != "" && m[tc.keep] == nil {
			t.Errorf("%s: lost %s: %q", name, tc.keep, raw)
		}
		if fi, _ := os.Stat(cfg); fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode %v", name, fi.Mode().Perm())
		}
	}
}

func TestBotTaskAnswersBypassDisclaimer(t *testing.T) {
	plain := botTaskScript(&botRecord{Name: "x"}, "/tmp/x", nil)
	if strings.Contains(plain, "Bypass Permissions") {
		t.Error("a bot without bypass mode answers its disclaimer")
	}
	bypass := botTaskScript(&botRecord{Name: "x", PermissionMode: "bypassPermissions"}, "/tmp/x", nil)
	if !strings.Contains(bypass, "running in Bypass Permissions mode") || !strings.Contains(bypass, "❯ Yes, I accept") {
		t.Errorf("bypass mode's disclaimer is not answered:\n%s", bypass)
	}
}

func TestCreateDeleteBotTools(t *testing.T) {
	useBotTestEnv(t)
	dir := filepath.Join(t.TempDir(), "made")
	_, out, err := createBotTool(context.Background(), nil, createBotIn{Name: "made", Runtime: "claude", Dir: dir, Instructions: "# made\n"})
	if err != nil || out["error"] != nil {
		t.Fatalf("create_bot: %v %v", err, out)
	}
	got, err := getBot("made")
	if err != nil || got.Runtime != botRuntimeClaude || !got.KeepRunning || !got.Notify || !got.Stopped {
		t.Fatalf("created bot: %v %+v", err, got)
	}
	if md, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md")); string(md) != "# made\n" {
		t.Errorf("instructions: %q", md)
	}
	if _, _, err := createBotTool(context.Background(), nil, createBotIn{Name: "made", Dir: dir}); err == nil {
		t.Error("a second bot with the same name was created")
	}
	if _, _, err := createBotTool(context.Background(), nil, createBotIn{Name: "Bad Name"}); err == nil {
		t.Error("a bad name was accepted")
	}

	// Skills: copy one in from a library folder, then remove it.
	lib := filepath.Join(t.TempDir(), "notes")
	os.MkdirAll(lib, 0o755)
	os.WriteFile(filepath.Join(lib, "SKILL.md"), []byte("---\ndescription: take notes\n---\n"), 0o644)
	_, sk, err := addBotSkillTool(context.Background(), nil, botSkillIn{Name: "made", From: lib})
	if err != nil || len(sk.Skills) != 1 || sk.Skills[0].Name != "notes" {
		t.Fatalf("add_bot_skill: %v %+v", err, sk)
	}
	if _, _, err := addBotSkillTool(context.Background(), nil, botSkillIn{Name: "made", From: lib}); err == nil {
		t.Error("the same skill was copied twice")
	}
	if _, sk, err = removeBotSkillTool(context.Background(), nil, botSkillIn{Name: "made", Skill: "notes"}); err != nil || len(sk.Skills) != 0 {
		t.Errorf("remove_bot_skill: %v %+v", err, sk)
	}
	if _, _, err := removeBotSkillTool(context.Background(), nil, botSkillIn{Name: "made", Skill: "../x"}); err == nil {
		t.Error("a path was accepted as a skill name")
	}

	// A stopped bot's model is saved for its next start.
	m := "opus"
	if _, ch, err := setBotModelTool(context.Background(), nil, setBotModelIn{Name: "made", Model: &m}); err != nil || ch.RestartNeeded {
		t.Errorf("set_bot_model: %v %+v", err, ch)
	}
	if got, _ := getBot("made"); got.Model != "opus" {
		t.Errorf("model not saved: %+v", got)
	}

	if _, _, err := readBotChatTool(context.Background(), nil, readBotChatIn{Name: "made"}); err == nil || !strings.Contains(err.Error(), "read_agent") {
		t.Errorf("read_bot_chat on a claude bot: %v", err)
	}
	if _, _, err := reorderBotsTool(context.Background(), nil, reorderBotsIn{Names: []string{"made", "ghost"}}); err == nil {
		t.Error("reorder_bots accepted a missing bot")
	}

	_, del, err := deleteBotTool(context.Background(), nil, deleteBotIn{Name: "made", DeleteFolder: true})
	if err != nil || del["folder_deleted"] != true {
		t.Fatalf("delete_bot: %v %v", err, del)
	}
	if _, err := getBot("made"); err == nil {
		t.Error("the bot is still there")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the folder is still there: %v", err)
	}
}

func TestPreviewBotScheduleTool(t *testing.T) {
	_, out, err := previewBotScheduleTool(context.Background(), nil, previewScheduleIn{Cron: "47 7 * * *", Timezone: "America/Los_Angeles"})
	if err != nil || out["error"] != nil {
		t.Errorf("valid schedule: %v %v", err, out)
	}
	_, out, _ = previewBotScheduleTool(context.Background(), nil, previewScheduleIn{Cron: "nope"})
	if out["error"] == nil {
		t.Errorf("a bad schedule had no error: %v", out)
	}
}

func TestUpdateSettingsRefusesPluginApprovals(t *testing.T) {
	for _, action := range []string{"enable", "trust", "isolation", "install", "update/confirm"} {
		_, _, err := updateSettingsTool(context.Background(), nil, updateSettingsIn{Plugin: &pluginActionIn{Name: "x", Action: action}})
		if err == nil || !strings.Contains(err.Error(), "need a human") {
			t.Errorf("plugin action %q was accepted", action)
		}
	}
}
