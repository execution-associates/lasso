package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"
)

// /mcp serves lasso's own agent skill (the repo's SKILL.md and the docs it
// links into) over the MCP Skills extension, io.modelcontextprotocol/skills
// (https://modelcontextprotocol.io/extensions/skills/overview): a client that
// supports it discovers the skill with skills/list or skills/get and reads its
// files with resources/read at skill://lasso/<path>. A client that does not
// still sees SKILL.md in resources/list.
//
// The files are staged into src/skill/<name>/ by scripts/stage-skill.sh
// (mise run build/test, CI, release), since go:embed cannot reach above src/.
// A build without them fails to compile, the same as one without web/dist.
//
// go-sdk 1.6 rejects any method outside its own table before middleware runs,
// in the HTTP transport itself, so skills/list and skills/get are answered by
// withMCPSkills in front of the SDK. resources/read is the SDK's.

//go:embed all:skill
var skillFS embed.FS

const (
	mcpSkillsExtension = "io.modelcontextprotocol/skills"
	// The skill changes only with a lasso release, which restarts the process.
	mcpSkillTTLMs = 3_600_000
)

type mcpSkillResource struct {
	URI    string `json:"uri"`
	Digest string `json:"digest"`
	Size   int    `json:"size"`
}

// mcpSkill is one entry as skills/list and skills/get return it, plus the
// bytes resources/read serves, keyed by URI. The manifest is computed from
// those same bytes, as the spec requires.
type mcpSkill struct {
	URI         string             `json:"uri"`
	Frontmatter map[string]any     `json:"frontmatter"`
	Resources   []mcpSkillResource `json:"resources"`
	files       map[string][]byte
}

var mcpSkills = sync.OnceValue(func() []*mcpSkill {
	skills, err := loadMCPSkills(skillFS, "skill")
	if err != nil {
		log.Printf("mcp skills: %v (serving none)", err)
		return nil
	}
	return skills
})

// loadMCPSkills reads every <root>/<name>/SKILL.md and the files beside it.
func loadMCPSkills(fsys fs.FS, root string) ([]*mcpSkill, error) {
	dirs, err := fs.ReadDir(fsys, root)
	if err != nil {
		return nil, err
	}
	var skills []*mcpSkill
	for _, d := range dirs {
		if !d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			continue
		}
		s, err := loadMCPSkill(fsys, root, d.Name())
		if err != nil {
			return nil, fmt.Errorf("skill %s: %w", d.Name(), err)
		}
		skills = append(skills, s)
	}
	return skills, nil
}

func loadMCPSkill(fsys fs.FS, root, name string) (*mcpSkill, error) {
	dir := path.Join(root, name)
	md, err := fs.ReadFile(fsys, path.Join(dir, "SKILL.md"))
	if err != nil {
		return nil, err
	}
	fm, err := parseSkillFrontmatter(md)
	if err != nil {
		return nil, err
	}
	if fm["name"] != name {
		return nil, fmt.Errorf("frontmatter name %v does not match its directory", fm["name"])
	}
	if d, _ := fm["description"].(string); d == "" {
		return nil, errors.New("frontmatter has no description")
	}
	s := &mcpSkill{
		URI:         "skill://" + name + "/SKILL.md",
		Frontmatter: fm,
		files:       map[string][]byte{},
	}
	err = fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		uri := "skill://" + name + "/" + strings.TrimPrefix(p, dir+"/")
		sum := sha256.Sum256(b)
		s.files[uri] = b
		s.Resources = append(s.Resources, mcpSkillResource{URI: uri, Digest: "sha256:" + hex.EncodeToString(sum[:]), Size: len(b)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(s.Resources, func(i, j int) bool { return s.Resources[i].URI < s.Resources[j].URI })
	return s, nil
}

// parseSkillFrontmatter returns SKILL.md's YAML frontmatter, every field kept
// as written: skills/list must hand them over unchanged.
func parseSkillFrontmatter(md []byte) (map[string]any, error) {
	md = bytes.ReplaceAll(md, []byte("\r\n"), []byte("\n"))
	rest, ok := bytes.CutPrefix(md, []byte("---\n"))
	if !ok {
		return nil, errors.New("SKILL.md does not begin with YAML frontmatter")
	}
	head, _, ok := bytes.Cut(rest, []byte("\n---\n"))
	if !ok {
		return nil, errors.New("SKILL.md frontmatter is not closed")
	}
	fm := map[string]any{}
	if err := yaml.Unmarshal(head, &fm); err != nil {
		return nil, fmt.Errorf("SKILL.md frontmatter: %w", err)
	}
	return fm, nil
}

func findMCPSkill(skills []*mcpSkill, uri string) *mcpSkill {
	for _, s := range skills {
		if s.URI == uri {
			return s
		}
	}
	return nil
}

// registerMCPSkills declares the extension and serves the skills' files. Each
// SKILL.md is a listed resource; the supporting files are reached through one
// template so resources/list stays short. An unknown skill:// URI is -32602,
// the spec's code, not the SDK's resource-not-found.
func registerMCPSkills(srv *mcp.Server, skills []*mcpSkill) {
	read := func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		uri := req.Params.URI
		for _, s := range skills {
			if b, ok := s.files[uri]; ok {
				c := &mcp.ResourceContents{URI: uri, MIMEType: skillFileMIME(uri)}
				if utf8.Valid(b) {
					c.Text = string(b)
				} else {
					c.Blob = b
				}
				return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{c}}, nil
			}
		}
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "unknown skill file: " + uri}
	}
	for _, s := range skills {
		name, _ := s.Frontmatter["name"].(string)
		desc, _ := s.Frontmatter["description"].(string)
		srv.AddResource(&mcp.Resource{URI: s.URI, Name: name, Title: name + " skill", Description: desc, MIMEType: "text/markdown"}, read)
	}
	srv.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "skill://{skill}/{+path}",
		Name:        "skill-files",
		Title:       "Skill files",
		Description: "The files of a skill this server serves; skills/list gives each skill's complete manifest.",
	}, read)
}

func skillFileMIME(uri string) string {
	switch ext := path.Ext(uri); ext {
	case ".md":
		return "text/markdown"
	default:
		if t := mime.TypeByExtension(ext); t != "" {
			return t
		}
		return "text/plain"
	}
}

// withMCPSkills answers skills/list and skills/get, which the SDK would refuse
// as unknown methods, and hands every other request to next untouched. Neither
// needs session state: the skills are the same for every caller who got past
// /mcp's auth gate, which sits outside this handler.
func withMCPSkills(skills []*mcpSkill, next http.Handler) http.Handler {
	if len(skills) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			http.Error(w, "reading request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		// The cheap check first: almost every POST is a tool call.
		if !bytes.Contains(body, []byte(`"skills/`)) {
			next.ServeHTTP(w, r)
			return
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(body, &msg) != nil || (msg.Method != "skills/list" && msg.Method != "skills/get") {
			next.ServeHTTP(w, r)
			return
		}
		if len(msg.ID) == 0 || string(msg.ID) == "null" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		result, rpcErr := answerMCPSkills(skills, msg.Method, msg.Params)
		reply := map[string]any{"jsonrpc": "2.0", "id": msg.ID}
		if rpcErr != nil {
			reply["error"] = rpcErr
		} else {
			reply["result"] = result
		}
		if sid := r.Header.Get("Mcp-Session-Id"); sid != "" {
			w.Header().Set("Mcp-Session-Id", sid)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(reply)
	})
}

func answerMCPSkills(skills []*mcpSkill, method string, raw json.RawMessage) (map[string]any, *jsonrpc.Error) {
	var params struct {
		Cursor string `json:"cursor"`
		URI    string `json:"uri"`
	}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid params: " + err.Error()}
		}
	}
	result := map[string]any{"resultType": "complete", "ttlMs": mcpSkillTTLMs, "cacheScope": "public"}
	switch method {
	case "skills/list":
		// One page holds them all, so no cursor this server issued exists.
		if params.Cursor != "" {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid cursor"}
		}
		result["skills"] = skills
	default:
		s := findMCPSkill(skills, params.URI)
		if s == nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "unknown skill: " + params.URI}
		}
		result["skill"] = s
	}
	return result, nil
}
