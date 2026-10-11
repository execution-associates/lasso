package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// postSkillsRPC sends one JSON-RPC request straight at the handler, the way a
// client that supports the skills extension would.
func postSkillsRPC(t *testing.T, url, method string, params any) (json.RawMessage, *jsonrpc.Error) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": method, "params": params})
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s: HTTP %d: %s", method, resp.StatusCode, b)
	}
	var reply struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *jsonrpc.Error  `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		t.Fatalf("%s: decode: %v", method, err)
	}
	if string(reply.ID) != "7" {
		t.Fatalf("%s: reply id %s, want 7", method, reply.ID)
	}
	return reply.Result, reply.Error
}

type skillEntry struct {
	URI         string             `json:"uri"`
	Frontmatter map[string]any     `json:"frontmatter"`
	Resources   []mcpSkillResource `json:"resources"`
}

// The whole extension end to end on the real /mcp handler: the capability in
// initialize, skills/list and skills/get, and every manifest entry read back
// through resources/read with the size and digest the manifest promised.
func TestMCPServesLassoSkill(t *testing.T) {
	srv := httptest.NewServer(newMCPHandler())
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	client := mcp.NewClient(&mcp.Implementation{Name: "skills-test", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	caps := session.InitializeResult().Capabilities
	if _, ok := caps.Extensions[mcpSkillsExtension]; !ok || caps.Resources == nil {
		t.Fatalf("initialize capabilities lack the skills extension or resources: %+v", caps)
	}

	raw, rpcErr := postSkillsRPC(t, srv.URL, "skills/list", map[string]any{})
	if rpcErr != nil {
		t.Fatalf("skills/list: %v", rpcErr)
	}
	var list struct {
		ResultType string       `json:"resultType"`
		Skills     []skillEntry `json:"skills"`
		TTLMs      int          `json:"ttlMs"`
		CacheScope string       `json:"cacheScope"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	if list.ResultType != "complete" || list.TTLMs <= 0 || list.CacheScope == "" {
		t.Fatalf("skills/list result fields: %s", raw)
	}
	if len(list.Skills) != 1 || list.Skills[0].URI != "skill://lasso/SKILL.md" {
		t.Fatalf("skills/list = %s, want the one lasso skill", raw)
	}
	entry := list.Skills[0]
	if entry.Frontmatter["name"] != "lasso" || entry.Frontmatter["description"] == "" {
		t.Fatalf("frontmatter = %v", entry.Frontmatter)
	}

	// The spec's per-skill ceiling, which hosts must support.
	total := 0
	for _, r := range entry.Resources {
		total += r.Size
	}
	if len(entry.Resources) > 512 || total > 16<<20 {
		t.Fatalf("skill is %d files, %d bytes: over the spec's 512 files / 16 MiB", len(entry.Resources), total)
	}

	inManifest := map[string]bool{}
	for _, r := range entry.Resources {
		inManifest[r.URI] = true
		res, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: r.URI})
		if err != nil {
			t.Fatalf("resources/read %s: %v", r.URI, err)
		}
		c := res.Contents[0]
		b := []byte(c.Text)
		if c.Text == "" {
			b = c.Blob
		}
		sum := sha256.Sum256(b)
		if len(b) != r.Size || "sha256:"+hex.EncodeToString(sum[:]) != r.Digest {
			t.Errorf("%s: read %d bytes digest %x, manifest says %d %s", r.URI, len(b), sum, r.Size, r.Digest)
		}
		if strings.HasSuffix(r.URI, ".md") && c.MIMEType != "text/markdown" {
			t.Errorf("%s: mimeType %q", r.URI, c.MIMEType)
		}
	}
	if !inManifest[entry.URI] {
		t.Fatal("manifest does not list SKILL.md")
	}

	// A relative link in SKILL.md must land on a file the skill serves, or a
	// host following it is refused (reads are confined to the manifest).
	md, _ := skillFS.ReadFile("skill/lasso/SKILL.md")
	for _, m := range regexp.MustCompile(`\]\(([^)#\s]+)`).FindAllSubmatch(md, -1) {
		link := string(m[1])
		if strings.Contains(link, "://") || strings.HasPrefix(link, "mailto:") {
			continue
		}
		if uri := "skill://lasso/" + path.Clean(link); !inManifest[uri] {
			t.Errorf("SKILL.md links %s, which the skill does not serve", link)
		}
	}

	// SKILL.md is also an ordinary listed resource, for clients without the
	// extension.
	resources, err := session.ListResources(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, r := range resources.Resources {
		listed = listed || r.URI == entry.URI
	}
	if !listed {
		t.Error("resources/list does not list skill://lasso/SKILL.md")
	}

	raw, rpcErr = postSkillsRPC(t, srv.URL, "skills/get", map[string]any{"uri": entry.URI})
	if rpcErr != nil {
		t.Fatalf("skills/get: %v", rpcErr)
	}
	var got struct {
		ResultType string     `json:"resultType"`
		Skill      skillEntry `json:"skill"`
	}
	if err := json.Unmarshal(raw, &got); err != nil || got.ResultType != "complete" || got.Skill.URI != entry.URI || len(got.Skill.Resources) != len(entry.Resources) {
		t.Fatalf("skills/get = %s (%v)", raw, err)
	}

	if _, rpcErr := postSkillsRPC(t, srv.URL, "skills/get", map[string]any{"uri": "skill://nope/SKILL.md"}); rpcErr == nil || rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Errorf("skills/get of an unknown skill: %v, want -32602", rpcErr)
	}
	if _, rpcErr := postSkillsRPC(t, srv.URL, "skills/list", map[string]any{"cursor": "x"}); rpcErr == nil || rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Errorf("skills/list with a bogus cursor: %v, want -32602", rpcErr)
	}
	_, err = session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "skill://lasso/docs/nope.md"})
	var wire *jsonrpc.Error
	if !errors.As(err, &wire) || wire.Code != jsonrpc.CodeInvalidParams {
		t.Errorf("resources/read of an unknown skill file: %v, want -32602", err)
	}
}

// The staged copy is what the binary serves; a stale one would hand agents
// yesterday's instructions. mise run test restages first, so this only fires
// when go test runs straight after an edit to SKILL.md.
func TestStagedSkillMatchesRepo(t *testing.T) {
	repo, err := os.ReadFile("../SKILL.md")
	if err != nil {
		t.Skip("no repo SKILL.md beside src/")
	}
	staged, _ := skillFS.ReadFile("skill/lasso/SKILL.md")
	if !bytes.Equal(repo, staged) {
		t.Fatal("src/skill/lasso/SKILL.md differs from ../SKILL.md: run scripts/stage-skill.sh (mise run build/test do)")
	}
}

func TestLoadMCPSkillsRefusesBadSkills(t *testing.T) {
	for name, md := range map[string]string{
		"no frontmatter":   "# hi\n",
		"unclosed":         "---\nname: x\n",
		"name mismatch":    "---\nname: other\ndescription: d\n---\n",
		"no description":   "---\nname: x\n---\n",
		"invalid yaml":     "---\nname: [x\n---\n",
		"description list": "---\nname: x\ndescription: [a]\n---\n",
	} {
		fsys := fstest.MapFS{"skill/x/SKILL.md": {Data: []byte(md)}}
		if _, err := loadMCPSkills(fsys, "skill"); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	fsys := fstest.MapFS{
		"skill/x/SKILL.md":       {Data: []byte("---\nname: x\ndescription: d\nmetadata:\n  k: v\n---\nbody\n")},
		"skill/x/refs/a.md":      {Data: []byte("a\n")},
		"skill/.hidden/SKILL.md": {Data: []byte("junk")},
	}
	skills, err := loadMCPSkills(fsys, "skill")
	if err != nil || len(skills) != 1 {
		t.Fatalf("loadMCPSkills = %v, %v", skills, err)
	}
	if m, _ := skills[0].Frontmatter["metadata"].(map[string]any); m["k"] != "v" {
		t.Errorf("extra frontmatter not kept unchanged: %v", skills[0].Frontmatter)
	}
	if len(skills[0].Resources) != 2 || skills[0].Resources[1].URI != "skill://x/refs/a.md" {
		t.Errorf("manifest = %+v", skills[0].Resources)
	}
}

// Every request that is not skills/list or skills/get reaches the SDK with its
// body intact, including a tool call whose arguments happen to say "skills/".
func TestWithMCPSkillsPassesOtherRequestsThrough(t *testing.T) {
	skills := []*mcpSkill{{URI: "skill://x/SKILL.md"}}
	var seen []byte
	h := withMCPSkills(skills, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = io.ReadAll(r.Body)
		w.WriteHeader(299)
	}))
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"open_file","arguments":{"path":"\"skills/x"}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"arguments":{"method":"skills/list"}}}`,
		`[{"jsonrpc":"2.0","id":1,"method":"skills/list"}]`,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body)))
		if rec.Code != 299 || string(seen) != body {
			t.Errorf("%s: code %d, next saw %q", body, rec.Code, seen)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"skills/list"}`)))
	if rec.Code != http.StatusAccepted {
		t.Errorf("skills/list notification: %d, want 202", rec.Code)
	}
}
