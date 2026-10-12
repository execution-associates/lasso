package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Metadata a client attaches to a message it sends an omp bot — that it was
// dictated, and by which engine and model — kept beside the conversation,
// since omp's session file (the chat's source of truth) has nowhere to hold
// it. A send stores (bot, sha256 of the text, when, meta); reading the chat
// matches each row back onto the user message it describes.

const botMessageMetaSchema = `
CREATE TABLE IF NOT EXISTS bot_message_meta (
  id       INTEGER PRIMARY KEY,
  bot      TEXT    NOT NULL,
  text_sha TEXT    NOT NULL,
  sent_at  INTEGER NOT NULL,   -- unix ms, taken just before the send
  meta     TEXT    NOT NULL    -- JSON object of string to string
);
CREATE INDEX IF NOT EXISTS bot_message_meta_bot ON bot_message_meta(bot, sent_at);
`

const (
	// botMetaKeep is how many rows a bot keeps; the oldest go first.
	botMetaKeep     = 500
	botMetaMaxBytes = 1024
	botMetaMaxKeys  = 16
	// botMetaSkew is how far before the send a matching row's timestamp may
	// be: omp stamps the record with its own clock, and lasso takes sent_at
	// before the RPC, so the two only disagree by skew.
	botMetaSkew = 5 * time.Second
)

var botMetaKeyRE = regexp.MustCompile(`^[a-z_]{1,32}$`)

// decodeBotMeta validates a send's `meta`: absent, null or {} is none;
// otherwise an object of string values, at most botMetaMaxKeys keys matching
// botMetaKeyRE, at most botMetaMaxBytes marshalled.
func decodeBotMeta(raw json.RawMessage) (map[string]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, errors.New("meta must be an object of string values")
	}
	if len(m) == 0 {
		return nil, nil
	}
	if len(m) > botMetaMaxKeys {
		return nil, fmt.Errorf("meta has more than %d keys", botMetaMaxKeys)
	}
	for k := range m {
		if !botMetaKeyRE.MatchString(k) {
			return nil, fmt.Errorf("meta key %q: lowercase letters and underscores, at most 32", k)
		}
	}
	if b, _ := json.Marshal(m); len(b) > botMetaMaxBytes {
		return nil, fmt.Errorf("meta is larger than %d bytes", botMetaMaxBytes)
	}
	return m, nil
}

// botTextSHA is the hash a message is matched by. Trimmed, because the chat
// parser trims a user row's text.
func botTextSHA(text string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(text)))
	return hex.EncodeToString(sum[:])
}

// storeBotMeta records a confirmed send's meta and prunes the bot's rows to
// the newest botMetaKeep.
func storeBotMeta(bot, text string, sentAt time.Time, meta map[string]string) error {
	b, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if _, err := db.Exec(`INSERT INTO bot_message_meta(bot, text_sha, sent_at, meta) VALUES(?, ?, ?, ?)`,
		bot, botTextSHA(text), sentAt.UnixMilli(), string(b)); err != nil {
		return err
	}
	_, err = db.Exec(`DELETE FROM bot_message_meta WHERE bot = ? AND id NOT IN (
		SELECT id FROM bot_message_meta WHERE bot = ? ORDER BY sent_at DESC, id DESC LIMIT ?)`,
		bot, bot, botMetaKeep)
	return err
}

// annotateBotMeta puts each stored row's meta on the user row it describes:
// oldest row first, the EARLIEST unannotated user row whose text hashes the
// same and whose timestamp is no more than botMetaSkew before the send. Each
// row and each item is used once, so the same words sent twice annotate their
// own messages. A row whose message is not on this page may land on a later
// copy of the same words that is.
func annotateBotMeta(bot string, items []chatItem) {
	type userRow struct {
		i   int
		sha string
		at  int64
	}
	var users []userRow
	for i, it := range items {
		if it.Kind != "user" || it.Text == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, it.At)
		if err != nil {
			continue
		}
		users = append(users, userRow{i: i, sha: botTextSHA(it.Text), at: at.UnixMilli()})
	}
	if len(users) == 0 || db == nil {
		return
	}
	rows, err := db.Query(`SELECT text_sha, sent_at, meta FROM bot_message_meta WHERE bot = ? ORDER BY sent_at, id`, bot)
	if err != nil {
		return
	}
	defer rows.Close()
	used := make([]bool, len(users))
	for rows.Next() {
		var sha, raw string
		var sentAt int64
		if rows.Scan(&sha, &sentAt, &raw) != nil {
			continue
		}
		for j, u := range users {
			if used[j] || u.sha != sha || u.at < sentAt-botMetaSkew.Milliseconds() {
				continue
			}
			var m map[string]string
			if json.Unmarshal([]byte(raw), &m) == nil && len(m) > 0 {
				items[u.i].Meta = m
				used[j] = true
			}
			break
		}
	}
}
