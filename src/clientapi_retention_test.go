package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Per-person file retention, deleting one's own files and the quota override
// (docs/design/client-api.md, Attachments).

// sentUpload uploads a picture and sends it, answering the stored row.
func (c *clientTest) sentUpload(token, cid string) (appAttachment, clientMessageView) {
	c.t.Helper()
	_, att := c.upload(token, cid, "p.png", pngBytes())
	resp, m := c.send(token, cid, `{"text":"a picture","attachments":["`+att.ID+`"]}`)
	if resp.StatusCode != 202 || len(m.Attachments) != 1 {
		c.t.Fatalf("send: %d %+v", resp.StatusCode, m.Attachments)
	}
	a, err := appAttachmentByID(att.ID)
	if err != nil {
		c.t.Fatal(err)
	}
	return a, m
}

// botSends makes the bot send the person a picture and answers its stored row.
func (c *clientTest) botSends(cid string) appAttachment {
	c.t.Helper()
	box := filepath.Join(c.root, clientOutboxRel)
	_ = os.MkdirAll(box, 0o700)
	_ = os.WriteFile(filepath.Join(box, "r.png"), pngBytes(), 0o600)
	bm, err := botClientReply("jess", cid, c.token(cid), "report", true, []string{filepath.Join(clientOutboxRel, "r.png")}, false, time.Now())
	if err != nil {
		c.t.Fatal(err)
	}
	a, err := appAttachmentByID(bm.Attachments[0].ID)
	if err != nil {
		c.t.Fatal(err)
	}
	return a
}

func ageFiles(d time.Duration) {
	_, _ = db.Exec(`UPDATE app_attachments SET created_at = ? WHERE expired_at = ''`, nowStamp(time.Now().Add(-d)))
}

func (c *clientTest) fileGone(a appAttachment) bool {
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(c.root, a.Path)); err != nil {
			return true
		}
	}
	return false
}

func (c *clientTest) setRetention(token, body string) (int, []byte) {
	c.t.Helper()
	resp, b := c.do("PUT", "/client/v1/me/settings", token, body)
	return resp.StatusCode, b
}

type meView struct {
	Settings struct {
		Days *int `json:"file_retention_days"`
	}
	Limits struct {
		Max *int `json:"max_file_retention_days"`
	}
	Storage struct {
		UsedBytes  int64 `json:"used_bytes"`
		UsedFiles  int   `json:"used_files"`
		QuotaBytes int64 `json:"quota_bytes"`
		QuotaFiles int   `json:"quota_files"`
	}
}

func (c *clientTest) me(token string) meView {
	c.t.Helper()
	var v meView
	c.get("/client/v1/me", token, &v)
	return v
}

func TestClientMeSettingsLimitsStorage(t *testing.T) {
	c := newClientTest(t)
	_, ann := c.person("Ann", false, "jess")
	cid := c.conv(ann, "jess")
	_, raw := c.do("GET", "/client/v1/me", ann, "")
	var generic map[string]json.RawMessage
	_ = json.Unmarshal(raw, &generic)
	if string(generic["settings"]) != `{"file_retention_days":null}` || string(generic["limits"]) != `{"max_file_retention_days":null}` {
		t.Fatalf("settings/limits = %s / %s", generic["settings"], generic["limits"])
	}
	me := c.me(ann)
	if me.Storage.UsedBytes != 0 || me.Storage.UsedFiles != 0 || me.Storage.QuotaBytes != 500<<20 || me.Storage.QuotaFiles != 3000 {
		t.Fatalf("storage = %+v", me.Storage)
	}
	a, _ := c.sentUpload(ann, cid)
	c.botSends(cid) // a bot-sent copy is not an upload: it is not counted against the quota
	me = c.me(ann)
	if me.Storage.UsedBytes != a.Size || me.Storage.UsedFiles != 1 {
		t.Fatalf("storage after an upload = %+v", me.Storage)
	}
}

func TestClientDefaultKeepsFiles(t *testing.T) {
	c := newClientTest(t)
	_, ann := c.person("Ann", false, "jess")
	cid := c.conv(ann, "jess")
	a, _ := c.sentUpload(ann, cid)
	out := c.botSends(cid)
	ageFiles(400 * 24 * time.Hour)
	clientMaintain(time.Now())
	time.Sleep(100 * time.Millisecond)
	for _, x := range []appAttachment{a, out} {
		if _, err := os.Stat(filepath.Join(c.root, x.Path)); err != nil {
			t.Fatalf("%s deleted with no retention chosen", x.Path)
		}
		if dl, _ := c.do("GET", "/client/v1/attachments/"+x.ID, ann, ""); dl.StatusCode != 200 {
			t.Fatalf("download after 400 days: %d", dl.StatusCode)
		}
	}
}

func TestClientChosenRetentionExpiresFiles(t *testing.T) {
	c := newClientTest(t)
	_, ann := c.person("Ann", false, "jess")
	cid := c.conv(ann, "jess")
	if code, b := c.setRetention(ann, `{"file_retention_days": 7}`); code != 200 || string(b) != "{\"settings\":{\"file_retention_days\":7}}\n" {
		t.Fatalf("set: %d %s", code, b)
	}
	if d := c.me(ann).Settings.Days; d == nil || *d != 7 {
		t.Fatalf("me.settings = %v", d)
	}
	young, _ := c.sentUpload(ann, cid)
	old, _ := c.sentUpload(ann, cid)
	botOld := c.botSends(cid)
	_, _ = db.Exec(`UPDATE app_attachments SET created_at = ? WHERE id IN (?, ?)`, nowStamp(time.Now().Add(-8*24*time.Hour)), old.ID, botOld.ID)
	_, _ = db.Exec(`UPDATE app_attachments SET created_at = ? WHERE id = ?`, nowStamp(time.Now().Add(-6*24*time.Hour)), young.ID)
	clientMaintain(time.Now())
	for _, x := range []appAttachment{old, botOld} {
		if !c.fileGone(x) {
			t.Fatalf("%s outlived 7 days", x.ID)
		}
		got, _ := appAttachmentByID(x.ID)
		if got.Removed != "retention" || got.ExpiredAt == "" {
			t.Fatalf("row = %+v", got)
		}
	}
	if _, err := os.Stat(filepath.Join(c.root, young.Path)); err != nil {
		t.Fatal("a file inside the window was deleted")
	}
}

func TestClientRetentionAppliesToExistingFiles(t *testing.T) {
	c := newClientTest(t)
	annID, ann := c.person("Ann", false, "jess")
	cid := c.conv(ann, "jess")
	older, _ := c.sentUpload(ann, cid)
	rescued, _ := c.sentUpload(ann, cid)
	ageFiles(20 * 24 * time.Hour)

	// A longer choice, or none, keeps a 20 day old file.
	for _, body := range []string{`{"file_retention_days": 60}`, `{"file_retention_days": null}`} {
		c.setRetention(ann, body)
		clientMaintain(time.Now())
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(c.root, older.Path)); err != nil {
		t.Fatal("deleted under a longer setting")
	}

	// Shortening it deletes what is now too old at the next sweep, and not before.
	c.setRetention(ann, `{"file_retention_days": 10}`)
	if a, _ := appAttachmentByID(older.ID); a.ExpiredAt != "" {
		t.Fatal("deleted before a sweep")
	}
	// Lengthening before the sweep rescues a file that was not deleted yet.
	c.setRetention(ann, `{"file_retention_days": 30}`)
	clientMaintain(time.Now())
	if a, _ := appAttachmentByID(rescued.ID); a.ExpiredAt != "" {
		t.Fatal("a rescued file was deleted")
	}
	c.setRetention(ann, `{"file_retention_days": 10}`)
	clientMaintain(time.Now())
	if !c.fileGone(older) || !c.fileGone(rescued) {
		t.Fatal("files older than the shortened setting survived the sweep")
	}
	// Deleted files do not come back.
	c.setRetention(ann, `{"file_retention_days": null}`)
	clientMaintain(time.Now())
	if a, _ := appAttachmentByID(older.ID); a.ExpiredAt == "" {
		t.Fatal("a deleted file came back")
	}
	_ = annID
}

func TestClientOwnerRetentionMaximum(t *testing.T) {
	c := newClientTest(t)
	_, ann := c.person("Ann", false, "jess")
	cid := c.conv(ann, "jess")
	t.Setenv(clientRetentionDaysEnv, "30")

	if me := c.me(ann); me.Limits.Max == nil || *me.Limits.Max != 30 || me.Settings.Days == nil || *me.Settings.Days != 30 {
		t.Fatalf("null under a maximum: %+v", me)
	}
	for body, want := range map[string]int{`{"file_retention_days": 365}`: 30, `{"file_retention_days": 7}`: 7, `{"file_retention_days": null}`: 30} {
		code, b := c.setRetention(ann, body)
		var out struct {
			Settings struct {
				Days *int `json:"file_retention_days"`
			}
		}
		_ = json.Unmarshal(b, &out)
		if code != 200 || out.Settings.Days == nil || *out.Settings.Days != want {
			t.Fatalf("%s -> %d %s, want %d", body, code, b, want)
		}
	}
	// "Keep" is treated as the maximum at the sweep.
	c.setRetention(ann, `{"file_retention_days": null}`)
	a, _ := c.sentUpload(ann, cid)
	young, _ := c.sentUpload(ann, cid)
	_, _ = db.Exec(`UPDATE app_attachments SET created_at = ? WHERE id = ?`, nowStamp(time.Now().Add(-31*24*time.Hour)), a.ID)
	clientMaintain(time.Now())
	if !c.fileGone(a) {
		t.Fatal("a file older than the owner's maximum was kept")
	}
	if _, err := os.Stat(filepath.Join(c.root, young.Path)); err != nil {
		t.Fatal("a file inside the maximum was deleted")
	}
	// Without a maximum the choice is the person's again.
	t.Setenv(clientRetentionDaysEnv, "")
	if me := c.me(ann); me.Limits.Max != nil || me.Settings.Days != nil {
		t.Fatalf("no maximum: %+v", me)
	}
}

func TestClientSettingsValidation(t *testing.T) {
	c := newClientTest(t)
	_, ann := c.person("Ann", false, "jess")
	for _, body := range []string{`{}`, `{"file_retention_days": 0}`, `{"file_retention_days": -1}`, `{"file_retention_days": 3651}`, `{"file_retention_days": 1.5}`,
		`{"file_retention_days": "7"}`, `not json`} {
		if code, b := c.setRetention(ann, body); code != 400 || errCode(b) != "invalid_request" {
			t.Fatalf("%s: %d %s", body, code, b)
		}
	}
	if code, _ := c.setRetention(ann, `{"file_retention_days": 3650}`); code != 200 {
		t.Fatal("3650 refused")
	}
	if resp, _ := c.do("GET", "/client/v1/me/settings", ann, ""); resp.StatusCode != 405 {
		t.Fatalf("GET settings: %d", resp.StatusCode)
	}
	if resp, _ := c.do("PUT", "/client/v1/me/settings", "", `{"file_retention_days": 7}`); resp.StatusCode != 401 {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
}

func TestClientDeleteOwnFile(t *testing.T) {
	c := newClientTest(t)
	_, ann := c.person("Ann", false, "jess")
	_, bob := c.person("Bob", false, "jess")
	cid, bobConv := c.conv(ann, "jess"), c.conv(bob, "jess")
	up, msg := c.sentUpload(ann, cid)
	botFile := c.botSends(cid)
	bobUp, _ := c.sentUpload(bob, bobConv)
	before := c.me(ann).Storage

	// Someone else's file and an unknown one answer the same 404.
	r1, b1 := c.do("DELETE", "/client/v1/attachments/"+bobUp.ID, ann, "")
	r2, b2 := c.do("DELETE", "/client/v1/attachments/a_nope", ann, "")
	if r1.StatusCode != 404 || r2.StatusCode != 404 || string(b1) != string(b2) {
		t.Fatalf("someone else's: %d %s / unknown: %d %s", r1.StatusCode, b1, r2.StatusCode, b2)
	}
	if a, _ := appAttachmentByID(bobUp.ID); a.ExpiredAt != "" {
		t.Fatal("Ann removed Bob's file")
	}
	if resp, _ := c.do("POST", "/client/v1/attachments/"+up.ID, ann, ""); resp.StatusCode != 405 {
		t.Fatalf("POST: %d", resp.StatusCode)
	}

	// Her own upload: bytes, both copies and the quota go; the marker stays.
	resp, body := c.do("DELETE", "/client/v1/attachments/"+up.ID, ann, "")
	if resp.StatusCode != 200 {
		t.Fatalf("delete: %d %s", resp.StatusCode, body)
	}
	var out struct{ Attachment map[string]any }
	_ = json.Unmarshal(body, &out)
	want := map[string]any{"id": up.ID, "name": "p.png", "type": "image/png", "size": float64(up.Size), "width": float64(1), "height": float64(1), "expired": true, "removed": "user"}
	if len(out.Attachment) != len(want) {
		t.Fatalf("marker = %v", out.Attachment)
	}
	for k, v := range want {
		if out.Attachment[k] != v {
			t.Fatalf("marker[%s] = %v, want %v (%v)", k, out.Attachment[k], v, out.Attachment)
		}
	}
	if !c.fileGone(up) {
		t.Fatal("the stored file is still in the bot's folder")
	}
	after := c.me(ann).Storage
	if after.UsedFiles != before.UsedFiles-1 || after.UsedBytes != before.UsedBytes-up.Size {
		t.Fatalf("quota not freed: %+v -> %+v", before, after)
	}
	// The message keeps the entry; the download says gone.
	for _, h := range c.rawMessagesView(ann, cid) {
		if h.ID == msg.ID && (!h.Attachments[0].Expired || h.Attachments[0].Removed != "user" || h.Attachments[0].URL != "") {
			t.Fatalf("history marker = %+v", h.Attachments[0])
		}
	}
	if dl, b := c.do("GET", "/client/v1/attachments/"+up.ID, ann, ""); dl.StatusCode != 410 || errCode(b) != "file_expired" {
		t.Fatalf("download: %d %s", dl.StatusCode, b)
	}
	// Idempotent.
	if resp, b2 := c.do("DELETE", "/client/v1/attachments/"+up.ID, ann, ""); resp.StatusCode != 200 || string(b2) != string(body) {
		t.Fatalf("second delete: %d %s", resp.StatusCode, b2)
	}

	// A file the bot sent can be removed too: the stored copy goes.
	if resp, _ := c.do("DELETE", "/client/v1/attachments/"+botFile.ID, ann, ""); resp.StatusCode != 200 || !c.fileGone(botFile) {
		t.Fatalf("bot-sent file: %d", resp.StatusCode)
	}
	if a, _ := appAttachmentByID(botFile.ID); a.Removed != "user" {
		t.Fatalf("row = %+v", a)
	}

	// An upload not yet sent can be removed, and cannot then be sent.
	_, pending := c.upload(ann, cid, "q.png", pngBytes())
	if resp, _ := c.do("DELETE", "/client/v1/attachments/"+pending.ID, ann, ""); resp.StatusCode != 200 {
		t.Fatalf("pending: %d", resp.StatusCode)
	}
	if resp, _ := c.send(ann, cid, `{"text":"x","attachments":["`+pending.ID+`"]}`); resp.StatusCode != 400 {
		t.Fatalf("sent a removed upload: %d", resp.StatusCode)
	}
}

func TestClientQuotaFreedByDeleting(t *testing.T) {
	c := newClientTest(t)
	_, ann := c.person("Ann", false, "jess")
	cid := c.conv(ann, "jess")
	t.Setenv(clientQuotaFilesEnv, "2")
	_, a := c.upload(ann, cid, "a.png", pngBytes())
	c.upload(ann, cid, "b.png", pngBytes())
	resp, b := c.doRaw("POST", "/client/v1/conversations/"+cid+"/attachments", ann, strings.NewReader(string(pngBytes())))
	if resp.StatusCode != 403 || errCode(b) != "quota_exceeded" {
		t.Fatalf("over: %d %s", resp.StatusCode, b)
	}
	if !strings.Contains(string(b), "Delete files") || !strings.Contains(string(b), "retention") {
		t.Fatalf("the error does not say what to do: %s", b)
	}
	c.do("DELETE", "/client/v1/attachments/"+a.ID, ann, "")
	if resp, _ := c.upload(ann, cid, "c.png", pngBytes()); resp.StatusCode != 201 {
		t.Fatalf("after deleting: %d", resp.StatusCode)
	}
}

func TestClientPurge(t *testing.T) {
	c := newClientTest(t)
	_, ann := c.person("Ann", false, "jess")
	_, bob := c.person("Bob", false, "jess")
	cid, bobConv := c.conv(ann, "jess"), c.conv(bob, "jess")
	old, _ := c.sentUpload(ann, cid)
	oldOut := c.botSends(cid)
	fresh, _ := c.sentUpload(ann, cid)
	bobs, _ := c.sentUpload(bob, bobConv)
	_, _ = db.Exec(`UPDATE app_attachments SET created_at = ? WHERE id IN (?, ?)`, nowStamp(time.Now().Add(-40*24*time.Hour)), old.ID, oldOut.ID)

	for _, body := range []string{`{}`, `{"older_than_days": -1}`, `{"older_than_days": "x"}`, `{"older_than_days": 1.5}`, `nope`} {
		if resp, b := c.do("POST", "/client/v1/me/files/purge", ann, body); resp.StatusCode != 400 || errCode(b) != "invalid_request" {
			t.Fatalf("%s: %d %s", body, resp.StatusCode, b)
		}
	}
	resp, b := c.do("POST", "/client/v1/me/files/purge", ann, `{"older_than_days": 30}`)
	var out struct {
		Files int   `json:"removed_files"`
		Bytes int64 `json:"freed_bytes"`
	}
	_ = json.Unmarshal(b, &out)
	if resp.StatusCode != 200 || out.Files != 2 || out.Bytes != old.Size+oldOut.Size {
		t.Fatalf("purge 30: %d %s", resp.StatusCode, b)
	}
	if !c.fileGone(old) || !c.fileGone(oldOut) {
		t.Fatal("files survived the purge")
	}
	if a, _ := appAttachmentByID(fresh.ID); a.ExpiredAt != "" {
		t.Fatal("a newer file was purged")
	}
	if a, _ := appAttachmentByID(old.ID); a.Removed != "user" {
		t.Fatalf("removed = %q", a.Removed)
	}
	// 0 removes everything of hers, and only hers.
	_, b = c.do("POST", "/client/v1/me/files/purge", ann, `{"older_than_days": 0}`)
	_ = json.Unmarshal(b, &out)
	if out.Files != 1 || out.Bytes != fresh.Size || !c.fileGone(fresh) {
		t.Fatalf("purge 0: %s", b)
	}
	if a, _ := appAttachmentByID(bobs.ID); a.ExpiredAt != "" {
		t.Fatal("Bob's file was purged")
	}
	if _, b = c.do("POST", "/client/v1/me/files/purge", ann, `{"older_than_days": 0}`); !strings.Contains(string(b), `"removed_files":0`) {
		t.Fatalf("nothing left: %s", b)
	}
	// Rate limited.
	var last int
	for i := 0; i < clientPurgesPerMin+1; i++ {
		resp, _ := c.do("POST", "/client/v1/me/files/purge", ann, `{"older_than_days": 0}`)
		last = resp.StatusCode
	}
	if last != 429 {
		t.Fatalf("purge not rate limited: %d", last)
	}
}

func TestClientAttachmentRemovedEvent(t *testing.T) {
	c := newClientTest(t)
	annID, ann := c.person("Ann", false, "jess")
	cid := c.conv(ann, "jess")
	t.Setenv(clientRetentionDaysEnv, "")
	a, msg := c.sentUpload(ann, cid)
	b, _ := c.sentUpload(ann, cid)
	d, _ := c.sentUpload(ann, cid)

	ch, stop, _ := c.openStream(ann, cid, "")
	nextEvent(t, ch, "hello")
	c.do("DELETE", "/client/v1/attachments/"+a.ID, ann, "")
	ev := nextEvent(t, ch, "attachment_removed")
	var data map[string]string
	_ = json.Unmarshal([]byte(ev.Data), &data)
	if ev.ID == "" || data["conversation_id"] != cid || data["message_id"] != msg.ID || data["attachment_id"] != a.ID || data["removed"] != "user" || len(data) != 4 {
		t.Fatalf("event = %+v", ev)
	}
	// A purge and a retention sweep announce too; a repeated delete does not.
	c.do("DELETE", "/client/v1/attachments/"+a.ID, ann, "")
	c.do("POST", "/client/v1/me/files/purge", ann, `{"older_than_days": 0}`)
	got := map[string]string{}
	for len(got) < 2 {
		e := nextEvent(t, ch, "attachment_removed")
		var x map[string]string
		_ = json.Unmarshal([]byte(e.Data), &x)
		got[x["attachment_id"]] = x["removed"]
	}
	if got[b.ID] != "user" || got[d.ID] != "user" || got[a.ID] != "" {
		t.Fatalf("purge events = %v", got)
	}
	stop()

	// A device that was away learns of a retention sweep when it resumes.
	r, _ := c.sentUpload(ann, cid)
	last := newestEventSeq(cid)
	thirty := 1
	_ = setFileRetention(annID, &thirty, time.Now())
	_, _ = db.Exec(`UPDATE app_attachments SET created_at = ? WHERE id = ?`, nowStamp(time.Now().Add(-48*time.Hour)), r.ID)
	clientMaintain(time.Now())
	ch2, stop2, _ := c.openStream(ann, cid, itoa(int(last)))
	defer stop2()
	nextEvent(t, ch2, "hello")
	e := nextEvent(t, ch2, "attachment_removed")
	if !strings.Contains(e.Data, r.ID) || !strings.Contains(e.Data, `"removed":"retention"`) || e.ID == "" {
		t.Fatalf("resumed event = %+v", e)
	}
}

func TestAppUserQuotaOverride(t *testing.T) {
	c := newClientTest(t)
	annID, ann := c.person("Ann", false, "jess")
	cid := c.conv(ann, "jess")
	put := func(body string) (int, testUserView2) {
		req := httptest.NewRequest("PUT", "/api/app-users/"+annID, strings.NewReader(body))
		w := httptest.NewRecorder()
		serveAppUsers(w, req)
		var out struct{ User testUserView2 }
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out.User
	}
	code, u := put(`{"quota_files": 1}`)
	if code != 200 || u.QuotaFiles == nil || *u.QuotaFiles != 1 || u.QuotaMB != nil {
		t.Fatalf("set: %d %+v", code, u)
	}
	if me := c.me(ann); me.Storage.QuotaFiles != 1 || me.Storage.QuotaBytes != 500<<20 {
		t.Fatalf("/me storage = %+v", me.Storage)
	}
	c.upload(ann, cid, "a.png", pngBytes())
	if resp, b := c.doRaw("POST", "/client/v1/conversations/"+cid+"/attachments", ann, strings.NewReader(string(pngBytes()))); resp.StatusCode != 403 || errCode(b) != "quota_exceeded" {
		t.Fatalf("override not applied: %d %s", resp.StatusCode, b)
	}
	// Raising it applies at once; MB too; the view carries storage and retention.
	if code, u = put(`{"quota_files": 5, "quota_mb": 2}`); code != 200 || *u.QuotaFiles != 5 || *u.QuotaMB != 2 {
		t.Fatalf("raise: %d %+v", code, u)
	}
	if u.Storage.UsedFiles != 1 || u.Storage.QuotaBytes != 2<<20 || u.Storage.QuotaFiles != 5 {
		t.Fatalf("view storage = %+v", u.Storage)
	}
	if resp, _ := c.upload(ann, cid, "b.png", pngBytes()); resp.StatusCode != 201 {
		t.Fatalf("raised quota: %d", resp.StatusCode)
	}
	c.setRetention(ann, `{"file_retention_days": 14}`)
	if _, u = put(`{"name":"Ann"}`); u.FileRetentionDays == nil || *u.FileRetentionDays != 14 || u.QuotaFiles == nil {
		t.Fatalf("an unrelated edit changed the override: %+v", u)
	}
	// null clears, absent leaves alone, nonsense is refused.
	if code, u = put(`{"quota_files": null}`); code != 200 || u.QuotaFiles != nil || u.QuotaMB == nil {
		t.Fatalf("clear: %d %+v", code, u)
	}
	if code, u = put(`{"quota_mb": null}`); code != 200 || u.QuotaMB != nil {
		t.Fatalf("clear mb: %d %+v", code, u)
	}
	if me := c.me(ann); me.Storage.QuotaFiles != 3000 || me.Storage.QuotaBytes != 500<<20 {
		t.Fatalf("defaults back: %+v", me.Storage)
	}
	for _, body := range []string{`{"quota_mb": 0}`, `{"quota_mb": -5}`, `{"quota_files": "many"}`, `{"quota_files": 1.5}`} {
		if code, _ := put(body); code != 400 {
			t.Fatalf("%s: %d", body, code)
		}
	}
}

type testUserView2 struct {
	QuotaMB           *int `json:"quota_mb"`
	QuotaFiles        *int `json:"quota_files"`
	FileRetentionDays *int `json:"file_retention_days"`
	Storage           struct {
		UsedFiles  int   `json:"used_files"`
		QuotaBytes int64 `json:"quota_bytes"`
		QuotaFiles int   `json:"quota_files"`
	}
}

// The update_app_user tool takes the same fields, null clearing an override.
func TestAppUserToolQuota(t *testing.T) {
	newClientTest(t)
	u, err := createAppUser("Ann", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	sch := updateAppUserSchema()
	resolved, err := sch.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{`{"id":"x","quota_mb":5}`, `{"id":"x","quota_mb":null,"quota_files":null}`} {
		var v map[string]any
		_ = json.Unmarshal([]byte(args), &v)
		if err := resolved.Validate(v); err != nil {
			t.Fatalf("%s: %v", args, err)
		}
	}
	for _, args := range []string{`{"id":"x","quota_mb":"5"}`, `{"id":"x","quota_files":0}`} {
		var v map[string]any
		_ = json.Unmarshal([]byte(args), &v)
		if err := resolved.Validate(v); err == nil {
			t.Fatalf("%s accepted", args)
		}
	}
	var in updateAppUserIn
	if err := json.Unmarshal([]byte(`{"id":"`+u.ID+`","quota_mb":9,"quota_files":null}`), &in); err != nil {
		t.Fatal(err)
	}
	if _, err := updateAppUser(in.ID, appUserPatch{QuotaMB: in.QuotaMB, QuotaFiles: in.QuotaFiles}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, _ := getAppUser(u.ID); got.QuotaMB != 9 || got.QuotaFiles != 0 {
		t.Fatalf("user = %+v", got)
	}
}
