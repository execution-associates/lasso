package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestChatTextUIState(t *testing.T) {
	openTestDB(t)
	us, _ := getUIState()
	if us.ChatText == nil || len(us.ChatText) != 0 {
		t.Fatalf("default chat_text = %#v; want {}", us.ChatText)
	}
	postUIState(t, `{"chat_text":{"size":16}}`)
	// A second device editing a different field must not drop the first.
	got := postUIState(t, `{"chat_text":{"weight":450.4,"preset":"plugin:harbor:roomy"}}`)
	if got.ChatText["size"] != 16.0 || got.ChatText["weight"] != 450.0 || got.ChatText["preset"] != "plugin:harbor:roomy" {
		t.Fatalf("per-field merge = %v (weight is rounded to an integer)", got.ChatText)
	}
	got = postUIState(t, `{"usage_compact":true}`)
	if len(got.ChatText) != 3 {
		t.Fatalf("an unrelated patch touched chat_text: %v", got.ChatText)
	}
	// null clears a number, "" clears the preset; both return to the default.
	got = postUIState(t, `{"chat_text":{"size":null,"preset":""}}`)
	if _, ok := got.ChatText["size"]; ok {
		t.Fatalf("null did not clear size: %v", got.ChatText)
	}
	if _, ok := got.ChatText["preset"]; ok || got.ChatText["weight"] != 450.0 {
		t.Fatalf("clearing the preset = %v", got.ChatText)
	}

	for name, body := range map[string]string{
		"unknown field":      `{"chat_text":{"color":"red"}}`,
		"unknown null field": `{"chat_text":{"color":null}}`,
		"size too small":     `{"chat_text":{"size":4}}`,
		"size too large":     `{"chat_text":{"size":400}}`,
		"leading too large":  `{"chat_text":{"line_height":9}}`,
		"backing above one":  `{"chat_text":{"backing":1.5}}`,
		"number as string":   `{"chat_text":{"size":"16px"}}`,
		"preset not an id":   `{"chat_text":{"preset":"roomy"}}`,
		"preset injection":   `{"chat_text":{"preset":"plugin:a:b\"; } x {"}}`,
		"preset number":      `{"chat_text":{"preset":3}}`,
		"not an object":      `{"chat_text":"big"}`,
	} {
		if w := postUIStateRaw(t, body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s; want 400", name, w.Code, w.Body.String())
		}
	}
	us, _ = getUIState()
	if len(us.ChatText) != 1 || us.ChatText["weight"] != 450.0 {
		t.Errorf("a refused patch changed what is stored: %v", us.ChatText)
	}
}

func TestChatTextSanitizesHandEditedBlob(t *testing.T) {
	got := sanitizeChatText(map[string]any{
		"size": 15.0, "weight": 2000.0, "preset": "nope", "color": "red", "backing": 0.4,
	})
	if len(got) != 2 || got["size"] != 15.0 || got["backing"] != 0.4 {
		t.Errorf("sanitize kept %v; want only size and backing", got)
	}
}

func TestPluginChatStyles(t *testing.T) {
	style := func(m map[string]any) map[string]any { return m["chat_styles"].([]any)[0].(map[string]any) }
	withStyle := func() map[string]any {
		m := appearanceManifest("harbor", "harbor-night")
		m["chat_styles"] = []any{map[string]any{
			"id": "roomy", "label": "Roomy", "font": "mono",
			"size": 16, "weight": 450, "line_height": 1.75, "letter_spacing": 0.01, "width": 48, "backing": 0.6,
		}}
		return m
	}
	cases := []struct {
		name    string
		mut     func(m map[string]any)
		wantErr string
	}{
		{"ok", func(map[string]any) {}, ""},
		{"only a size", func(m map[string]any) { m["chat_styles"] = []any{map[string]any{"id": "big", "size": 18}} }, ""},
		{"bad id", func(m map[string]any) { style(m)["id"] = "Roomy!" }, "id"},
		{"dup id", func(m map[string]any) { m["chat_styles"] = append(m["chat_styles"].([]any), style(m)) }, "duplicate id"},
		{"foreign font", func(m map[string]any) { style(m)["font"] = "inter" }, "not one of this plugin's fonts"},
		{"size out of range", func(m map[string]any) { style(m)["size"] = 64 }, "size must be between"},
		{"backing out of range", func(m map[string]any) { style(m)["backing"] = -1 }, "backing must be between"},
		{"long label", func(m map[string]any) { style(m)["label"] = strings.Repeat("x", 65) }, "label"},
		{"too many", func(m map[string]any) {
			var all []any
			for i := range 17 {
				all = append(all, map[string]any{"id": "s" + strings.Repeat("x", i)})
			}
			m["chat_styles"] = all
		}, "at most 16 chat styles"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := withStyle()
			c.mut(m)
			pd := writePlugin(t, t.TempDir(), "harbor", m, appearanceFiles())
			_, err := loadPluginManifest(pd, "harbor")
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, c.wantErr)
			}
		})
	}

	// A chat style is not a permission: changing its numbers keeps the
	// approval, the way editing a palette's colours does.
	load := func(m map[string]any) *pluginManifest {
		man, err := loadPluginManifest(writePlugin(t, t.TempDir(), "harbor", m, appearanceFiles()), "harbor")
		if err != nil {
			t.Fatal(err)
		}
		return man
	}
	a, b := withStyle(), withStyle()
	style(b)["size"] = 20
	if load(a).fingerprint() != load(b).fingerprint() {
		t.Error("editing a chat style's numbers changed the fingerprint")
	}

	m, dir := testPluginManager(t)
	writePlugin(t, dir, "harbor", withStyle(), appearanceFiles())
	m.rescan()
	if err := m.enable("harbor", ""); err != nil {
		t.Fatal(err)
	}
	p := pluginByName(t, m.listing(), "harbor")
	if len(p.ChatStyles) != 1 {
		t.Fatalf("chat_styles = %+v", p.ChatStyles)
	}
	s := p.ChatStyles[0]
	if s.GlobalID != "plugin:harbor:roomy" || s.Label != "Roomy" || s.Font != "plugin:harbor:mono" || s.Size == nil || *s.Size != 16 || s.Backing == nil || *s.Backing != 0.6 {
		t.Errorf("listed style = %+v", s)
	}
}
