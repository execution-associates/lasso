package main

import (
	"fmt"
	"maps"
	"math"
	"regexp"
	"sort"
	"strings"
)

// Text settings: how the chat view's prose and the terminals are set.
//
// Chat text — size, weight, leading, tracking, column width, and the reading
// panel behind it — is stored in ui_state.chat_text and offered by plugins as
// named chat styles (pluginappearance.go), both validated against the ONE
// range table below, so a value a plugin may ship is exactly a value Settings
// may store. Terminal text is ui_state.terminal_text: xterm's font options,
// with its own table and no plugin styles.
//
// Every field is a plain number the frontend writes into a CSS custom property
// or an xterm option itself, which is why ranges are enforced here and again in
// the browser: a plugin contributes values, never CSS.

// chatTextRange is the closed interval one field may take. Integer fields are
// rounded on the way in rather than refused, since a slider may send 450.0001.
type chatTextRange struct {
	Min, Max float64
	Integer  bool
}

// chatTextRanges is the whole table. Sizes are px, width is rem (the column
// cap), letter spacing is em, backing is the reading panel's opacity (0 =
// none, 1 = the theme background, opaque).
var chatTextRanges = map[string]chatTextRange{
	"size":           {Min: 11, Max: 24},
	"weight":         {Min: 100, Max: 900, Integer: true},
	"line_height":    {Min: 1.2, Max: 2.2},
	"letter_spacing": {Min: -0.05, Max: 0.15},
	"width":          {Min: 30, Max: 100},
	"backing":        {Min: 0, Max: 1},
}

// terminalTextRanges is the terminal's table (ui_state.terminal_text): xterm's
// own options, applied by the browser to every terminal. Size and letter
// spacing are px (xterm's letterSpacing is whole pixels added per cell), line
// height is xterm's multiplier, which cannot go below 1.
var terminalTextRanges = map[string]chatTextRange{
	"size":           {Min: 8, Max: 32},
	"weight":         {Min: 100, Max: 900, Integer: true},
	"line_height":    {Min: 1, Max: 2},
	"letter_spacing": {Min: -2, Max: 8, Integer: true},
}

// textPrefKind is one stored set of text settings: its name in errors, its
// range table, and whether it takes a plugin style (only the chat's does).
type textPrefKind struct {
	name      string
	ranges    map[string]chatTextRange
	hasPreset bool
}

var (
	chatTextKind     = textPrefKind{name: "chat_text", ranges: chatTextRanges, hasPreset: true}
	terminalTextKind = textPrefKind{name: "terminal_text", ranges: terminalTextRanges}
)

// chatTextPresetKey names the plugin chat style the stored values sit on top
// of: an explicit field wins over the style's, and the style's over lasso's
// default.
const chatTextPresetKey = "preset"

// chatStyleGlobalIDRE is a plugin chat style's global id, the shape a font's
// has.
var chatStyleGlobalIDRE = regexp.MustCompile(`^plugin:[a-z][a-z0-9-]{0,31}:[a-z][a-z0-9-]{0,31}$`)

func (k textPrefKind) keys() string {
	var keys []string
	for f := range k.ranges {
		keys = append(keys, f)
	}
	sort.Strings(keys)
	if k.hasPreset {
		keys = append([]string{chatTextPresetKey}, keys...)
	}
	return strings.Join(keys, ", ")
}

// checkNumber validates one numeric field and returns it normalized.
func (k textPrefKind) checkNumber(key string, v float64) (float64, error) {
	r, ok := k.ranges[key]
	if !ok {
		return 0, fmt.Errorf("unknown field %q (one of %s)", key, k.keys())
	}
	if math.IsNaN(v) || math.IsInf(v, 0) || v < r.Min || v > r.Max {
		return 0, fmt.Errorf("%s must be between %g and %g", key, r.Min, r.Max)
	}
	if r.Integer {
		v = math.Round(v)
	}
	return v, nil
}

// checkChatTextNumber is checkNumber against the chat's table, which is also
// what a plugin chat style is held to.
func checkChatTextNumber(key string, v float64) (float64, error) {
	return chatTextKind.checkNumber(key, v)
}

// normalize validates one stored or patched key. ok=false with a nil error
// means the entry clears the field ("" preset).
func (k textPrefKind) normalize(key string, v any) (any, bool, error) {
	if k.hasPreset && key == chatTextPresetKey {
		s, isStr := v.(string)
		if !isStr {
			return nil, false, fmt.Errorf("%s.preset must be a string", k.name)
		}
		if s == "" {
			return nil, false, nil
		}
		if !chatStyleGlobalIDRE.MatchString(s) {
			return nil, false, fmt.Errorf("%s.preset must be empty or a style id like plugin:<name>:<style>", k.name)
		}
		return s, true, nil
	}
	if _, known := k.ranges[key]; !known {
		return nil, false, fmt.Errorf("%s: unknown field %q (one of %s)", k.name, key, k.keys())
	}
	f, isNum := v.(float64)
	if !isNum {
		return nil, false, fmt.Errorf("%s.%s must be a number", k.name, key)
	}
	n, err := k.checkNumber(key, f)
	if err != nil {
		return nil, false, fmt.Errorf("%s.%s", k.name, err)
	}
	return n, true, nil
}

// merge folds a patch onto the stored fields, PER FIELD like typography: a
// named field is set, null (or "" for the preset) deletes it — the default is
// the absence of a choice — and a field the patch does not name is left as it
// was. Any invalid entry refuses the whole patch.
func (k textPrefKind) merge(stored, patch map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(stored)+len(patch))
	maps.Copy(out, stored)
	for key, v := range patch {
		if v == nil {
			if _, known := k.ranges[key]; !known && !(k.hasPreset && key == chatTextPresetKey) {
				return nil, fmt.Errorf("%s: unknown field %q (one of %s)", k.name, key, k.keys())
			}
			delete(out, key)
			continue
		}
		n, keep, err := k.normalize(key, v)
		if err != nil {
			return nil, err
		}
		if keep {
			out[key] = n
		} else {
			delete(out, key)
		}
	}
	return out, nil
}

// sanitize drops the entries of a hand-edited blob that could not have been
// written through the API, keeping the rest.
func (k textPrefKind) sanitize(in map[string]any) map[string]any {
	out := map[string]any{}
	for key, v := range in {
		if n, keep, err := k.normalize(key, v); err == nil && keep {
			out[key] = n
		}
	}
	return out
}

func mergeChatText(stored, patch map[string]any) (map[string]any, error) {
	return chatTextKind.merge(stored, patch)
}

func sanitizeChatText(in map[string]any) map[string]any { return chatTextKind.sanitize(in) }
