package main

import "database/sql"

// dropSpeechRelay removes what a lasso 8.9.0 database holds for the phone
// apps' cloud voice relay: the app_tts_usage table, whose only content is
// per-month character counts nothing reads any more, and the client_tts
// settings row (a key reference and the choices around it). It runs at every
// start and is a no-op once both are gone, so an upgraded database ends up
// indistinguishable from one that never had them.
func dropSpeechRelay(h *sql.DB) error {
	tx, err := h.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DROP TABLE IF EXISTS app_tts_usage`,
		`DELETE FROM settings WHERE key = 'client_tts'`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return tx.Commit()
}
