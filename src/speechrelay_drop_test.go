package main

import (
	"database/sql"
	"testing"
)

// TestDropSpeechRelayUpgradesAn890Database starts lasso on a database shaped
// like 8.9.0's — the cloud voice relay's usage table with a month of counts in
// it, and its settings row beside an ordinary setting — and checks the table
// and the row are gone while everything else is untouched.
func TestDropSpeechRelayUpgradesAn890Database(t *testing.T) {
	t.Setenv("LASSO_DIR", t.TempDir())
	raw, err := sql.Open("sqlite", lassoDBPath())
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	for _, q := range []string{
		`CREATE TABLE settings (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`INSERT INTO settings (key, value) VALUES
			('client_tts', '{"inworld":{"key_ref":"INWORLD_KEY","default_voice":"Ashley"}}'),
			('branch_prefix', 'feat/')`,
		`CREATE TABLE app_tts_usage (
			user_id    TEXT NOT NULL,
			month      TEXT NOT NULL,
			engine     TEXT NOT NULL,
			characters INTEGER NOT NULL DEFAULT 0,
			audio_ms   INTEGER NOT NULL DEFAULT 0,
			sentences  INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (user_id, month, engine)
		)`,
		`INSERT INTO app_tts_usage (user_id, month, engine, characters, audio_ms, sentences)
			VALUES ('u_1', '2026-11', 'inworld', 4200, 61000, 12)`,
	} {
		if _, err := raw.Exec(q); err != nil {
			raw.Close()
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	if err := openDB(); err != nil {
		t.Fatalf("openDB: %v", err)
	}
	t.Cleanup(closeTestDB)

	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'app_tts_usage'`).Scan(&tables); err != nil {
		t.Fatalf("count app_tts_usage: %v", err)
	}
	if tables != 0 {
		t.Fatalf("app_tts_usage still exists")
	}
	if v, err := getSetting("client_tts"); err != nil || v != "" {
		t.Fatalf("client_tts setting = %q, %v; want it gone", v, err)
	}
	if v, err := getSetting("branch_prefix"); err != nil || v != "feat/" {
		t.Fatalf("branch_prefix = %q, %v; want feat/ kept", v, err)
	}
	// Every start runs it, so a second pass must change nothing and must not
	// fail on the table it already dropped.
	if err := dropSpeechRelay(db); err != nil {
		t.Fatalf("second dropSpeechRelay: %v", err)
	}
}
