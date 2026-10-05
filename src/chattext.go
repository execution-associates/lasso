package main

import (
	"fmt"
	"maps"
	"math"
	"regexp"
	"sort"
	"strings"
)

// Chat text: how the chat view's prose is set — size, weight, leading,
// tracking, column width, and the reading panel behind it. Stored in
// ui_state.chat_text, and offered by plugins as named chat styles
// (pluginappearance.go), both validated against the ONE range table below so a
// value a plugin may ship is exactly a value Settings may store.
//
// Every field is a plain number the frontend writes into a CSS custom property
// itself, which is why ranges are enforced here and again in the browser: a
// plugin contributes values, never CSS.

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

// chatTextPresetKey names the plugin chat style the stored values sit on top
// of: an explicit field wins over the style's, and the style's over lasso's
// default.
const chatTextPresetKey = "preset"

// chatStyleGlobalIDRE is a plugin chat style's global id, the shape a font's
// has.
var chatStyleGlobalIDRE = regexp.MustCompile(`^plugin:[a-z][a-z0-9-]{0,31}:[a-z][a-z0-9-]{0,31}$`)

func chatTextKeys() string {
	keys := []string{chatTextPresetKey}
	for k := range chatTextRanges {
		keys = append(keys, k)
	}
	sort.Strings(keys[1:])
	return strings.Join(keys, ", ")
}

// checkChatTextNumber validates one numeric field and returns it normalized.
func checkChatTextNumber(key string, v float64) (float64, error) {
	r, ok := chatTextRanges[key]
	if !ok {
		return 0, fmt.Errorf("unknown field %q (one of %s)", key, chatTextKeys())
	}
	if math.IsNaN(v) || math.IsInf(v, 0) || v < r.Min || v > r.Max {
		return 0, fmt.Errorf("%s must be between %g and %g", key, r.Min, r.Max)
	}
	if r.Integer {
		v = math.Round(v)
	}
	return v, nil
}

// normalizeChatTextEntry validates one stored or patched key. ok=false with a
// nil error means the entry clears the field ("" preset).
func normalizeChatTextEntry(key string, v any) (any, bool, error) {
	if key == chatTextPresetKey {
		s, isStr := v.(string)
		if !isStr {
			return nil, false, fmt.Errorf("chat_text.preset must be a string")
		}
		if s == "" {
			return nil, false, nil
		}
		if !chatStyleGlobalIDRE.MatchString(s) {
			return nil, false, fmt.Errorf("chat_text.preset must be empty or a style id like plugin:<name>:<style>")
		}
		return s, true, nil
	}
	f, isNum := v.(float64)
	if !isNum {
		if _, known := chatTextRanges[key]; !known {
			return nil, false, fmt.Errorf("chat_text: unknown field %q (one of %s)", key, chatTextKeys())
		}
		return nil, false, fmt.Errorf("chat_text.%s must be a number", key)
	}
	n, err := checkChatTextNumber(key, f)
	if err != nil {
		return nil, false, fmt.Errorf("chat_text.%s", err)
	}
	return n, true, nil
}

// mergeChatText folds a patch onto the stored fields, PER FIELD like
// typography: a named field is set, null (or "" for the preset) deletes it —
// the default is the absence of a choice — and a field the patch does not name
// is left as it was. Any invalid entry refuses the whole patch.
func mergeChatText(stored, patch map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(stored)+len(patch))
	maps.Copy(out, stored)
	for k, v := range patch {
		if v == nil {
			if k != chatTextPresetKey {
				if _, known := chatTextRanges[k]; !known {
					return nil, fmt.Errorf("chat_text: unknown field %q (one of %s)", k, chatTextKeys())
				}
			}
			delete(out, k)
			continue
		}
		n, keep, err := normalizeChatTextEntry(k, v)
		if err != nil {
			return nil, err
		}
		if keep {
			out[k] = n
		} else {
			delete(out, k)
		}
	}
	return out, nil
}

// sanitizeChatText drops the entries of a hand-edited blob that could not have
// been written through the API, keeping the rest.
func sanitizeChatText(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		if n, keep, err := normalizeChatTextEntry(k, v); err == nil && keep {
			out[k] = n
		}
	}
	return out
}
