package main

import (
	"net/http"
	"testing"
)

// The chrome's texture rides the ui_state patch like the browser mode: subtle
// on every install that predates it, kept across a neighbour's write, and a
// value nobody could have picked refused rather than coerced.
func TestTexturePatch(t *testing.T) {
	openTestDB(t)

	got := postUIState(t, `{"client_id":"A","user_intent":false}`)
	if got.Texture != textureSubtle {
		t.Fatalf("fresh install is not subtle: %+v", got.uiState)
	}
	postUIState(t, `{"texture":"off","client_id":"A","user_intent":true}`)
	got = postUIState(t, `{"files_click_navigates":false,"client_id":"B","user_intent":true}`)
	if got.Texture != textureOff {
		t.Fatalf("texture lost by an unrelated patch: %+v", got.uiState)
	}
	for _, bad := range []string{`""`, `"loud"`} {
		w := postUIStateRaw(t, `{"texture":`+bad+`,"client_id":"A","user_intent":true}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("texture %s accepted: %d", bad, w.Code)
		}
	}
	if normalizeTexture("loud") != textureSubtle {
		t.Fatal("unknown stored texture not normalized to subtle")
	}
}
