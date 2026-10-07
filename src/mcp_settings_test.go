package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// A ui patch sent through update_settings must land exactly as the Settings
// tab's would: merged onto the stored state, and visible to get_settings.
func TestUpdateSettingsUIPatchRoundTrips(t *testing.T) {
	openTestDB(t)
	ctx := context.Background()

	if _, _, err := updateSettingsTool(ctx, nil, updateSettingsIn{UI: map[string]any{
		"appearance_mode": "dark",
		"palette_dark":    "rose-pine",
		"terminal_text":   map[string]any{"size": 15},
	}}); err != nil {
		t.Fatalf("update_settings: %v", err)
	}
	// A second patch naming one field must not drop the others.
	if _, _, err := updateSettingsTool(ctx, nil, updateSettingsIn{UI: map[string]any{"usage_compact": true}}); err != nil {
		t.Fatalf("update_settings: %v", err)
	}

	us, err := getUIState()
	if err != nil {
		t.Fatal(err)
	}
	if us.AppearanceMode != "dark" || us.PaletteDark != "rose-pine" || !us.UsageCompact {
		t.Fatalf("ui state after two patches = %+v", us)
	}
	if v, _ := json.Marshal(us.TerminalText["size"]); string(v) != "15" {
		t.Fatalf("terminal_text.size = %s, want 15", v)
	}

	_, out, err := getSettingsTool(ctx, nil, getSettingsIn{Section: "ui"})
	if err != nil {
		t.Fatalf("get_settings: %v", err)
	}
	ui, _ := out["ui"].(map[string]any)
	if ui["appearance_mode"] != "dark" {
		t.Fatalf("get_settings ui = %v", out)
	}
}

// The handler's validation is the tool's validation: a value the Settings tab
// would be refused is a tool error, and nothing is stored.
func TestUpdateSettingsRefusesInvalidUIValue(t *testing.T) {
	openTestDB(t)
	_, _, err := updateSettingsTool(context.Background(), nil, updateSettingsIn{UI: map[string]any{"appearance_mode": "plaid"}})
	if err == nil {
		t.Fatal("an unknown appearance_mode was accepted")
	}
	if us, _ := getUIState(); us.AppearanceMode == "plaid" {
		t.Fatal("refused value was stored anyway")
	}
}

func TestUpdateSettingsAutoTitle(t *testing.T) {
	openTestDB(t)
	off := false
	_, out, err := updateSettingsTool(context.Background(), nil, updateSettingsIn{AutoTitle: &off})
	if err != nil {
		t.Fatalf("update_settings: %v", err)
	}
	if autoTitleEnabled() {
		t.Fatal("auto-title still on")
	}
	agents, _ := out["agents"].(map[string]any)
	if agents["auto_title"] != false {
		t.Fatalf("answer's agents section = %v", out["agents"])
	}
}

func TestUpdateSettingsNeedsSomething(t *testing.T) {
	openTestDB(t)
	if _, _, err := updateSettingsTool(context.Background(), nil, updateSettingsIn{}); err == nil {
		t.Fatal("an empty update was accepted")
	}
}

func TestRemovePushDeviceUnknownID(t *testing.T) {
	openTestDB(t)
	if err := removePushDeviceByID("nope"); err == nil || !strings.Contains(err.Error(), "no notification device") {
		t.Fatalf("err = %v", err)
	}
}

// Settings live on lasso's machine: a credential contained to another host
// reads and writes none of them.
func TestSettingsToolsRefuseCallerWithoutLocalReach(t *testing.T) {
	openTestDB(t)
	req := callerReq("c1", "elsewhere", scopeSelf)
	if _, _, err := getSettingsTool(context.Background(), req, getSettingsIn{Section: "ui"}); err == nil {
		t.Fatal("get_settings answered a caller that does not reach local")
	}
	on := true
	if _, _, err := updateSettingsTool(context.Background(), req, updateSettingsIn{AutoTitle: &on}); err == nil {
		t.Fatal("update_settings accepted a caller that does not reach local")
	}
}

// Plugin approval, trust, install and update need a human in the Settings
// tab. Nothing in update_settings' input may reach them: pin that no field
// even names a plugin.
func TestUpdateSettingsCannotTouchPlugins(t *testing.T) {
	var walk func(reflect.Type)
	walk = func(rt reflect.Type) {
		for rt.Kind() == reflect.Pointer {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct {
			return
		}
		for i := range rt.NumField() {
			f := rt.Field(i)
			tag := strings.ToLower(f.Tag.Get("json") + f.Name)
			if strings.Contains(tag, "plugin") || strings.Contains(tag, "trust") {
				t.Errorf("update_settings field %s.%s reaches plugins; those need a human", rt.Name(), f.Name)
			}
			walk(f.Type)
		}
	}
	walk(reflect.TypeOf(updateSettingsIn{}))
}
