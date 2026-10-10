package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Plugins — third-party additions to lasso: sidebar tabs (a plugin's own web
// UI) and MCP tools mirrored onto lasso's /mcp. A plugin is a directory under
// <lassoDir()>/plugins/<name>/ holding plugin.json; docs/plugins/authoring.md is the
// authoring guide, and this file is the part that decides what a plugin may do.
//
// The trust model is the whole design. A manifest is SELF-ASSERTED — whoever
// wrote the directory wrote it — so nothing in it can grant anything:
//
//   - A discovered plugin is disabled until the OPERATOR enables it, and
//     enabling approves exactly the permissions the listing showed, stored as a
//     fingerprint (pluginFingerprint) in lasso's own db. A manifest edit that
//     changes any of them (a tab's entry/url, the image, the command, the
//     network allowlist, the env keys, the secrets and where each may be sent)
//     reads as needs_approval, and its tabs and MCP server stop loading until a
//     human approves the new set. Cosmetic fields (version, description, a
//     tab's label or icon) are deliberately outside the fingerprint: making a
//     human re-approve a typo fix trains them to click through approvals.
//   - Its MCP server runs in an isb sandbox (pluginsandbox.go) — a container,
//     or a VM when the operator asks — unless the operator marks it TRUSTED.
//     Both flags live in the db next to the approval, never in the manifest,
//     because "run me on the host" is exactly the request a malicious plugin
//     would make.
//   - Its tabs are served from /plugins/<name>/ under a CSP sandbox, which
//     makes the document an opaque origin even when it is opened top-level.
//     Without that, plugin JavaScript would be same-origin with lasso and could
//     call /api/file with the human's cookies — the file endpoints read and
//     write arbitrary paths, so that is the machine, not just the UI.

const (
	pluginManifestFile = "plugin.json"
	// pluginManifestMax bounds the manifest read. A manifest is a few hundred
	// bytes; anything near this is not a manifest.
	pluginManifestMax = 256 << 10
	// pluginToolSep joins a plugin's name to each of its tools' names on /mcp.
	// Double underscore because a plugin name may contain '-' and a tool name
	// '_', so neither alone separates them unambiguously to a reader.
	pluginToolSep = "__"
	// pluginRescanEvery is how often the plugins directory is re-read with
	// nobody asking. It is what turns a manifest edited in place into
	// needs_approval (and stops its server) without a human pressing Reload.
	pluginRescanEvery = 10 * time.Second
)

var (
	pluginNameRE  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	pluginEnvKeRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	// mcpToolNameRE is what Anthropic's API accepts as a tool name. A mirrored
	// name that does not fit is skipped: a tool no client can call is worse
	// than one that is not listed.
	mcpToolNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	// pluginHostRE is a DNS name, optionally a *. suffix wildcard. isb's
	// egress proxy allows names only, so checkPluginHost also refuses an IP
	// literal and a wildcard over a single label (*.com).
	pluginHostRE = regexp.MustCompile(`^(\*\.)?[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)
)

// pluginsDir is where plugins live, created on first use so the path the
// Settings pane shows is one that exists.
func pluginsDir() string {
	d := filepath.Join(lassoDir(), "plugins")
	_ = os.MkdirAll(d, 0o755)
	return d
}

// ---------------------------------------------------------------------------
// the manifest
// ---------------------------------------------------------------------------

type pluginManifest struct {
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Description string          `json:"description"`
	Tabs        []pluginTabSpec `json:"tabs"`
	// Views are pages for the MAIN window, beside the terminal, chat and grid
	// in the footer's view menu. Same shape and same serving as a tab; only
	// where lasso frames it differs.
	Views []pluginTabSpec `json:"views"`
	MCP   *pluginMCPSpec  `json:"mcp"`
	// Themes, Fonts and ChatStyles are appearance contributions: data, never
	// code or CSS (pluginappearance.go, chattext.go).
	Themes     []pluginThemeSpec     `json:"themes"`
	Fonts      []pluginFontSpec      `json:"fonts"`
	ChatStyles []pluginChatStyleSpec `json:"chat_styles"`
	// MinLassoVersion and Platforms say where the plugin can run at all. A
	// plugin that cannot is listed invalid with the reason; neither is part of
	// the fingerprint (they grant nothing).
	MinLassoVersion string   `json:"min_lasso_version"`
	Platforms       []string `json:"platforms"`

	// themeSig digests the files the themes are read from, so an in-place
	// palette edit rebuilds the registry (pluginManager.syncThemes). Set by
	// loadPluginManifest; never part of the fingerprint.
	themeSig string
}

type pluginTabSpec struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Icon  string `json:"icon"`
	Entry string `json:"entry"`
	URL   string `json:"url"`
}

type pluginMCPSpec struct {
	Image string `json:"image"`
	// VMImage is the VM image to boot when the operator runs this plugin in a
	// VM (an OCI image cannot boot as one). "" = lasso's default.
	VMImage string             `json:"vm_image"`
	Command []string           `json:"command"`
	Network []string           `json:"network"`
	Env     map[string]string  `json:"env"`
	Secrets []pluginSecretSpec `json:"secrets"`
}

type pluginSecretSpec struct {
	Name  string   `json:"name"`
	Hosts []string `json:"hosts"`
}

// pluginNetEntry is one parsed `network` entry.
type pluginNetEntry struct {
	Host string
	Port int
}

// parsePluginNet reads "host[:port]", defaulting to 443 — the one port almost
// every API a plugin talks to is on.
func parsePluginNet(s string) (pluginNetEntry, error) {
	s = strings.TrimSpace(s)
	host, port := s, 443
	if h, p, ok := strings.Cut(s, ":"); ok {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return pluginNetEntry{}, fmt.Errorf("network entry %q: port must be 1-65535", s)
		}
		host, port = h, n
	}
	if err := checkPluginHost(host); err != nil {
		return pluginNetEntry{}, fmt.Errorf("network entry %q: %v", s, err)
	}
	return pluginNetEntry{Host: strings.ToLower(host), Port: port}, nil
}

// checkPluginHost is the rule for every host a manifest names (network
// entries and secrets' hosts): what isb's egress allowlist accepts.
func checkPluginHost(host string) error {
	if net.ParseIP(strings.Trim(host, "[]")) != nil {
		return fmt.Errorf("%q is an IP address; the sandbox's egress allows host names only", host)
	}
	if !pluginHostRE.MatchString(host) {
		return fmt.Errorf("%q is not a host name", host)
	}
	if rest, ok := strings.CutPrefix(host, "*."); ok && !strings.Contains(rest, ".") {
		return fmt.Errorf("%q is a wildcard over a single label; name at least two (*.example.com)", host)
	}
	return nil
}

// loadPluginManifest reads and validates <dir>/plugin.json. dirName is the
// directory's own name, which the manifest's name must equal: the directory is
// what the operator sees and the name is what everything else is keyed by, and
// letting them differ would let a plugin present itself as another.
func loadPluginManifest(dir, dirName string) (*pluginManifest, error) {
	f, err := os.Open(filepath.Join(dir, pluginManifestFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no %s", pluginManifestFile)
		}
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, pluginManifestMax+1))
	if err != nil {
		return nil, err
	}
	if len(b) > pluginManifestMax {
		return nil, fmt.Errorf("%s is larger than %d bytes", pluginManifestFile, pluginManifestMax)
	}
	var m pluginManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %v", pluginManifestFile, err)
	}
	if err := m.validate(dirName); err != nil {
		return nil, err
	}
	sig, err := m.validateAppearanceAssets(dir)
	if err != nil {
		return nil, err
	}
	m.themeSig = sig
	return &m, nil
}

// validate is all-or-nothing: an invalid manifest loads nothing at all, since a
// half-loaded plugin is one whose approval covered something else.
func (m *pluginManifest) validate(dirName string) error {
	if !pluginNameRE.MatchString(m.Name) {
		return fmt.Errorf("name %q must match %s", m.Name, pluginNameRE)
	}
	// An empty dirName is a manifest read before it has a directory of its own
	// name (a staged install, a linked checkout): its name is what it will be
	// keyed by, so there is nothing to compare it with yet.
	if dirName != "" && m.Name != dirName {
		return fmt.Errorf("name %q does not match its directory %q", m.Name, dirName)
	}
	if err := m.validateRuntime(); err != nil {
		return err
	}
	if err := validatePluginPages("tabs", m.Tabs, 16); err != nil {
		return err
	}
	if err := validatePluginPages("views", m.Views, 8); err != nil {
		return err
	}
	if err := m.validateAppearance(); err != nil {
		return err
	}
	if m.MCP == nil {
		return nil
	}
	c := m.MCP
	if strings.TrimSpace(c.Image) == "" {
		return errors.New("mcp.image is required (the OCI image its sandbox boots)")
	}
	if strings.ContainsAny(c.Image, " \t\n") || strings.HasPrefix(c.Image, "-") {
		return fmt.Errorf("mcp.image %q is not an image reference", c.Image)
	}
	if c.VMImage != "" && (strings.ContainsAny(c.VMImage, " \t\n") || strings.HasPrefix(c.VMImage, "-")) {
		return fmt.Errorf("mcp.vm_image %q is not an image reference", c.VMImage)
	}
	if len(c.Command) == 0 || strings.TrimSpace(c.Command[0]) == "" {
		return errors.New("mcp.command is required (the stdio MCP server's argv)")
	}
	for _, n := range c.Network {
		if _, err := parsePluginNet(n); err != nil {
			return fmt.Errorf("mcp.%v", err)
		}
	}
	for k := range c.Env {
		if !pluginEnvKeRE.MatchString(k) {
			return fmt.Errorf("mcp.env: %q is not an environment variable name", k)
		}
	}
	secretSeen := map[string]bool{}
	for i, s := range c.Secrets {
		if !pluginEnvKeRE.MatchString(s.Name) {
			return fmt.Errorf("mcp.secrets[%d]: %q is not an environment variable name", i, s.Name)
		}
		// Case-insensitively: a secret's isb store name is its name lowercased.
		if secretSeen[strings.ToLower(s.Name)] {
			return fmt.Errorf("mcp.secrets[%d]: duplicate secret %q", i, s.Name)
		}
		secretSeen[strings.ToLower(s.Name)] = true
		if _, clash := c.Env[s.Name]; clash {
			return fmt.Errorf("mcp.secrets[%d]: %q is also in mcp.env", i, s.Name)
		}
		// A secret with nowhere to go would be substituted nowhere: the sandbox
		// sends its placeholder only to the hosts named here.
		if len(s.Hosts) == 0 {
			return fmt.Errorf("mcp.secrets[%d] (%s): hosts is required (where the secret may be sent)", i, s.Name)
		}
		for _, h := range s.Hosts {
			if err := checkPluginHost(h); err != nil {
				return fmt.Errorf("mcp.secrets[%d] (%s): %v", i, s.Name, err)
			}
		}
	}
	return nil
}

// cleanPluginPath turns a plugin-relative path into the slash form os.Root
// expects, refusing anything that is not plainly inside the plugin: absolute
// paths, `..` segments (refused outright rather than cleaned away — a manifest
// or URL spelling one is not an honest path), dot-segments naming hidden files
// (a plugin directory may well hold a .env), backslashes and NULs.
func cleanPluginPath(p string) (string, error) {
	if p == "" {
		return "", errors.New("empty path")
	}
	if strings.ContainsAny(p, "\\\x00") {
		return "", errors.New("path contains a backslash or NUL")
	}
	if strings.HasPrefix(p, "/") {
		return "", errors.New("path must be relative to the plugin directory")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", errors.New("path must not contain ..")
		}
		if strings.HasPrefix(seg, ".") && seg != "." {
			return "", errors.New("path must not name a hidden file")
		}
	}
	c := path.Clean(p)
	if c == "." || c == "" {
		return "", errors.New("empty path")
	}
	return c, nil
}

// validatePluginPages checks a manifest's tabs or views: both are a page lasso
// frames, an entry it serves or a url it frames as-is.
func validatePluginPages(kind string, pages []pluginTabSpec, limit int) error {
	if len(pages) > limit {
		return fmt.Errorf("at most %d %s (has %d)", limit, kind, len(pages))
	}
	seen := map[string]bool{}
	for i, t := range pages {
		if !pluginNameRE.MatchString(t.ID) {
			return fmt.Errorf("%s[%d]: id %q must match %s", kind, i, t.ID, pluginNameRE)
		}
		if seen[t.ID] {
			return fmt.Errorf("%s[%d]: duplicate id %q", kind, i, t.ID)
		}
		seen[t.ID] = true
		if strings.TrimSpace(t.Label) == "" || len(t.Label) > 64 {
			return fmt.Errorf("%s[%d] (%s): label must be 1-64 characters", kind, i, t.ID)
		}
		switch {
		case t.Entry != "" && t.URL != "":
			return fmt.Errorf("%s[%d] (%s): has both entry and url; a page is one or the other", kind, i, t.ID)
		case t.Entry != "":
			if _, err := cleanPluginPath(t.Entry); err != nil {
				return fmt.Errorf("%s[%d] (%s): entry: %v", kind, i, t.ID, err)
			}
		case t.URL != "":
			u, err := url.Parse(t.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return fmt.Errorf("%s[%d] (%s): url must be an absolute http(s) URL", kind, i, t.ID)
			}
		default:
			return fmt.Errorf("%s[%d] (%s): needs an entry (a file in the plugin) or a url", kind, i, t.ID)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// permissions and their fingerprint
// ---------------------------------------------------------------------------

// pluginPerms is what enabling a plugin approves, as the listing shows it.
type pluginPerms struct {
	Tabs  []pluginTabPerm `json:"tabs"`
	Views []pluginTabPerm `json:"views"`
	MCP   *pluginMCPPerms `json:"mcp,omitempty"`
	// Themes and Fonts are low-risk (data lasso renders itself), but a human
	// should see everything a plugin adds before approving it.
	Themes []string         `json:"themes"`
	Fonts  []pluginFontPerm `json:"fonts"`
}

type pluginTabPerm struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Entry string `json:"entry,omitempty"`
	URL   string `json:"url,omitempty"`
}

type pluginMCPPerms struct {
	Image string `json:"image"`
	// VMImage is omitempty so a plugin without one keeps the fingerprint it
	// was approved under.
	VMImage string             `json:"vm_image,omitempty"`
	Command []string           `json:"command"`
	Network []string           `json:"network"`
	EnvKeys []string           `json:"env_keys"`
	Secrets []pluginSecretSpec `json:"secrets"`
}

func (m *pluginManifest) perms() pluginPerms {
	p := emptyPluginPerms()
	for _, t := range m.Tabs {
		p.Tabs = append(p.Tabs, pluginTabPerm{ID: t.ID, Label: t.Label, Entry: t.Entry, URL: t.URL})
	}
	for _, t := range m.Views {
		p.Views = append(p.Views, pluginTabPerm{ID: t.ID, Label: t.Label, Entry: t.Entry, URL: t.URL})
	}
	for _, t := range m.Themes {
		p.Themes = append(p.Themes, t.ID)
	}
	for _, f := range m.Fonts {
		p.Fonts = append(p.Fonts, pluginFontPerm{ID: f.ID, Family: f.Family, Category: f.Category})
	}
	if c := m.MCP; c != nil {
		mp := &pluginMCPPerms{
			Image:   c.Image,
			VMImage: c.VMImage,
			Command: append([]string{}, c.Command...),
			Network: []string{},
			EnvKeys: []string{},
			Secrets: []pluginSecretSpec{},
		}
		for _, n := range c.Network {
			e, _ := parsePluginNet(n) // validated
			mp.Network = append(mp.Network, e.Host+":"+strconv.Itoa(e.Port))
		}
		for k := range c.Env {
			mp.EnvKeys = append(mp.EnvKeys, k)
		}
		sort.Strings(mp.EnvKeys)
		for _, s := range c.Secrets {
			hosts := make([]string, 0, len(s.Hosts))
			for _, h := range s.Hosts {
				hosts = append(hosts, strings.ToLower(h))
			}
			mp.Secrets = append(mp.Secrets, pluginSecretSpec{Name: s.Name, Hosts: hosts})
		}
		p.MCP = mp
	}
	return p
}

// emptyPluginPerms is the zero listing, with every list present so a client
// never needs a nil check.
func emptyPluginPerms() pluginPerms {
	return pluginPerms{Tabs: []pluginTabPerm{}, Views: []pluginTabPerm{}, Themes: []string{}, Fonts: []pluginFontPerm{}}
}

// pluginFingerprint is the sha256 of a canonical JSON of the approved
// permissions. Canonical means order-insensitive wherever order carries no
// meaning (tabs, network, env keys, secrets and their hosts) so reordering a
// manifest does not demand a re-approval, while the command stays in order
// because argv order IS meaning. Labels, icons and tab ids are left out: they
// change what a tab is called, not what it can reach.
//
// Theme ids and fonts' id/family/category ARE in it — a theme id is a key
// written into herdr's config and synced to the fleet, and a family is what
// the typography picker offers — but not the files behind them: editing a
// palette's colours is not a permission change. Both are omitempty, so a
// plugin with neither keeps the fingerprint it was approved under. Views are
// canonicalized like tabs and omitempty for the same reason.
func (m *pluginManifest) fingerprint() string {
	p := m.perms()
	type tab struct {
		Entry string `json:"entry,omitempty"`
		URL   string `json:"url,omitempty"`
	}
	type canon struct {
		Tabs   []tab            `json:"tabs"`
		Views  []tab            `json:"views,omitempty"`
		MCP    *pluginMCPPerms  `json:"mcp,omitempty"`
		Themes []string         `json:"themes,omitempty"`
		Fonts  []pluginFontPerm `json:"fonts,omitempty"`
	}
	pages := func(in []pluginTabPerm) []tab {
		out := []tab{}
		for _, t := range in {
			entry := t.Entry
			if entry != "" {
				entry, _ = cleanPluginPath(entry) // validated; "./ui/x" and "ui/x" are one file
			}
			out = append(out, tab{Entry: entry, URL: t.URL})
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Entry != out[j].Entry {
				return out[i].Entry < out[j].Entry
			}
			return out[i].URL < out[j].URL
		})
		return out
	}
	c := canon{Tabs: pages(p.Tabs)}
	if len(p.Views) > 0 {
		c.Views = pages(p.Views)
	}
	if p.MCP != nil {
		mp := *p.MCP
		mp.Network = slices.Clone(mp.Network)
		sort.Strings(mp.Network)
		mp.Network = slices.Compact(mp.Network)
		mp.Secrets = nil
		for _, s := range p.MCP.Secrets {
			hs := slices.Clone(s.Hosts)
			sort.Strings(hs)
			mp.Secrets = append(mp.Secrets, pluginSecretSpec{Name: s.Name, Hosts: slices.Compact(hs)})
		}
		sort.Slice(mp.Secrets, func(i, j int) bool { return mp.Secrets[i].Name < mp.Secrets[j].Name })
		c.MCP = &mp
	}
	if len(p.Themes) > 0 {
		c.Themes = slices.Sorted(slices.Values(p.Themes))
	}
	if len(p.Fonts) > 0 {
		c.Fonts = slices.Clone(p.Fonts)
		sort.Slice(c.Fonts, func(i, j int) bool { return c.Fonts[i].ID < c.Fonts[j].ID })
	}
	b, _ := json.Marshal(c)
	sum := sha256.Sum256(append([]byte("lasso-plugin-perms/1\n"), b...))
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// the operator's grants (lasso's db, never the plugin's directory)
// ---------------------------------------------------------------------------

// pluginGrant is what the operator decided about one plugin. Approved is the
// fingerprint they approved ("" = disabled); Trusted lets its MCP server run on
// the host instead of in a sandbox; VM puts the sandbox in a VM (its own
// kernel) instead of a container. Trusted wins over VM. All live in lasso's
// settings table, which a plugin cannot write: its sandbox mounts only its own
// directory read-only, and its data directory.
type pluginGrant struct {
	Approved string `json:"approved,omitempty"`
	Trusted  bool   `json:"trusted,omitempty"`
	VM       bool   `json:"vm,omitempty"`
}

const pluginGrantsKey = "plugins"

var pluginGrantsMu sync.Mutex

func loadPluginGrants() map[string]pluginGrant {
	out := map[string]pluginGrant{}
	if db == nil {
		return out
	}
	v, err := getSetting(pluginGrantsKey)
	if err != nil || v == "" {
		return out
	}
	_ = json.Unmarshal([]byte(v), &out)
	return out
}

// updatePluginGrant applies fn to one plugin's grant, read-modify-write under a
// lock so two Settings clicks cannot drop each other's change.
func updatePluginGrant(name string, fn func(*pluginGrant)) error {
	if db == nil {
		return errors.New("lasso's state db is not open")
	}
	pluginGrantsMu.Lock()
	defer pluginGrantsMu.Unlock()
	all := loadPluginGrants()
	g := all[name]
	fn(&g)
	if g == (pluginGrant{}) {
		delete(all, name)
	} else {
		all[name] = g
	}
	b, err := json.Marshal(all)
	if err != nil {
		return err
	}
	return setSetting(pluginGrantsKey, string(b))
}

// ---------------------------------------------------------------------------
// discovery and state
// ---------------------------------------------------------------------------

const (
	pluginStateDisabled      = "disabled"
	pluginStateEnabled       = "enabled"
	pluginStateNeedsApproval = "needs_approval"
	pluginStateInvalid       = "invalid"
)

// pluginEntry is one directory as the last scan found it.
type pluginEntry struct {
	Name string
	Dir  string
	Man  *pluginManifest // nil when invalid
	Err  string
	FP   string
	// Src is how it got here: a GitHub install, a linked checkout, or (no
	// record) a hand-placed directory. plugininstall.go.
	Src pluginSource
}

func (e *pluginEntry) state(g pluginGrant) string {
	switch {
	case e.Man == nil:
		return pluginStateInvalid
	case g.Approved == "":
		return pluginStateDisabled
	case g.Approved != e.FP:
		return pluginStateNeedsApproval
	default:
		return pluginStateEnabled
	}
}

// scanPlugins reads every plugin directory. A directory that is not a valid
// plugin is still listed — as invalid, with the reason — because a plugin that
// silently fails to appear is the one support question nobody can answer.
func scanPlugins(dir string) map[string]*pluginEntry {
	out := map[string]*pluginEntry{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, de := range ents {
		name := de.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(dir, name)
		if st, err := os.Stat(full); err != nil || !st.IsDir() { // follows a symlinked plugin
			continue
		}
		e := &pluginEntry{Name: name, Dir: full}
		if !pluginNameRE.MatchString(name) {
			e.Err = fmt.Sprintf("directory name %q is not a valid plugin name (%s)", name, pluginNameRE)
		} else if m, err := loadPluginManifest(full, name); err != nil {
			e.Err = err.Error()
		} else {
			e.Man, e.FP = m, m.fingerprint()
		}
		out[name] = e
	}
	return out
}

// ---------------------------------------------------------------------------
// the manager
// ---------------------------------------------------------------------------

// pluginManager owns the scan, the per-plugin MCP servers, and the HTTP API.
type pluginManager struct {
	dir    string
	server func() *mcp.Server // lasso's shared /mcp server, where tools are mirrored
	// runner picks how a plugin's MCP server runs for an isolation level
	// (pluginIsolation): an isb container or VM, or (trusted) the host. A
	// seam for tests; production never replaces it.
	runner func(isolation string) pluginRunner
	// onChange is called after every state change, so every tab refetches
	// /api/plugins (main wires it to hub.bumpPluginsRev).
	onChange func()

	ctx context.Context

	reconcileMu sync.Mutex // serializes reconcile: one start/stop pass at a time

	mu      sync.Mutex
	entries map[string]*pluginEntry
	servers map[string]*pluginServer
	sig     string // signature of the last listing, to tell a change from a rescan

	themeMu  sync.Mutex // serializes syncThemes
	themeSig string     // digest of the contributed themes the registry was last built from

	// dataRoot holds each plugin's writable data directory (<lassoDir>/plugin-data).
	dataRoot string
	// held names plugins whose directory is being swapped or removed
	// (update/uninstall): reconcile treats them as not wanted, so a rescan
	// landing mid-swap cannot restart a server on a half-moved directory.
	held map[string]bool
	// logs is each plugin's ring of recent host-child stderr lines, kept across
	// restarts of its server (GET /api/plugins/<name>/log for a trusted one).
	logs map[string]*lineRing
	// staged are install/update previews waiting for a confirm (plugininstall.go).
	stageMu sync.Mutex
	staged  map[string]*pluginStage
	// installMu serializes the operations that move directories and records:
	// confirm, link, unlink, uninstall, update.
	installMu sync.Mutex
	// runLock is held while this process owns the plugin servers for this
	// directory (pluginlock.go); runnerElsewhere says who does when it doesn't.
	runLock         *os.File
	runnerElsewhere string
}

// plugins is the process-wide manager main wires up; nil reads as "no plugins".
var plugins *pluginManager

func newPluginManager(dir string, server func() *mcp.Server) *pluginManager {
	return &pluginManager{
		dir:      dir,
		dataRoot: filepath.Join(filepath.Dir(dir), "plugin-data"),
		server:   server,
		runner:   defaultPluginRunner,
		ctx:      context.Background(),
		entries:  map[string]*pluginEntry{},
		servers:  map[string]*pluginServer{},
		held:     map[string]bool{},
		logs:     map[string]*lineRing{},
		staged:   map[string]*pluginStage{},
	}
}

func (m *pluginManager) changed() {
	if m.onChange != nil {
		m.onChange()
	}
}

// run scans, starts what is enabled, and keeps rescanning until ctx ends. The
// servers it starts hang off ctx too; shutdown stops them synchronously
// (stopAll) rather than trusting a cancelled context to have finished.
func (m *pluginManager) run(ctx context.Context) {
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()
	// A staging directory left by a previous process belongs to no preview
	// anyone can confirm any more.
	m.sweepStaging(true)
	// A lasso before 5.0 ran plugin servers in microsandbox microVMs; any it
	// left running would otherwise run forever.
	go sweepLegacyMSBSandboxes()
	m.rescan()
	t := time.NewTicker(pluginRescanEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.sweepStaging(false)
			m.rescan()
		}
	}
}

// rescan re-reads the directory and brings the running servers in line with
// it, reporting (and announcing) whether the listing changed.
func (m *pluginManager) rescan() bool {
	entries := m.scan()
	m.mu.Lock()
	m.entries = entries
	m.mu.Unlock()
	m.reconcile()
	return m.noteIfChanged()
}

// noteIfChanged bumps the revision when what /api/plugins would say moved.
//
// A rebuilt theme registry forces the bump even when the listing reads the
// same (a palette edited in place changes no field of it): plugins_rev is what
// makes every browser drop its cached theme catalog.
func (m *pluginManager) noteIfChanged() bool {
	themesMoved := m.syncThemes()
	b, _ := json.Marshal(m.listing().Plugins)
	m.mu.Lock()
	changed := string(b) != m.sig
	m.sig = string(b)
	m.mu.Unlock()
	if changed || themesMoved {
		m.changed()
	}
	return changed || themesMoved
}

// reconcile starts a server for every enabled plugin with an mcp section and
// stops every server that should no longer run — disabled, unapproved, gone,
// or running under a fingerprint or isolation that has since changed (a trust
// or VM flip is a restart: the same server cannot move between the host, a
// container and a VM in place).
func (m *pluginManager) reconcile() {
	m.reconcileMu.Lock()
	defer m.reconcileMu.Unlock()
	grants := loadPluginGrants()
	owner := m.ownsRunner()

	m.mu.Lock()
	var stop []*pluginServer
	var start []*pluginServer
	want := map[string]bool{}
	for name, e := range m.entries {
		g := grants[name]
		if !owner || e.state(g) != pluginStateEnabled || e.Man.MCP == nil || m.held[name] {
			continue
		}
		want[name] = true
		iso := pluginIsolation(g)
		if s := m.servers[name]; s != nil {
			if s.fp == e.FP && s.isolation == iso {
				continue
			}
			stop = append(stop, s)
		}
		s := newPluginServer(e, iso, m.server, m.runner(iso), m.changed)
		s.dataDir = m.dataDir(name)
		s.ring = m.logRingLocked(name)
		m.servers[name] = s
		start = append(start, s)
	}
	for name, s := range m.servers {
		if !want[name] {
			stop = append(stop, s)
			delete(m.servers, name)
		}
	}
	ctx := m.ctx
	m.mu.Unlock()

	// Outside the lock: stopping a sandboxed server waits on isb removing the
	// sandbox, which takes seconds, and the listing must stay answerable.
	for _, s := range stop {
		s.stop()
	}
	for _, s := range start {
		s.start(ctx)
	}
}

// restart stops and starts one plugin's server — also how an `unavailable`
// server (no usable isb, isb serve down, a secret that would not resolve) is
// retried after the operator fixed the cause.
func (m *pluginManager) restart(name string) error {
	m.mu.Lock()
	s := m.servers[name]
	delete(m.servers, name)
	m.mu.Unlock()
	if s != nil {
		s.stop()
	}
	m.rescan()
	m.mu.Lock()
	_, running := m.servers[name]
	m.mu.Unlock()
	if !running {
		return fmt.Errorf("plugin %q has no MCP server to restart (not enabled, or no mcp section)", name)
	}
	m.changed()
	return nil
}

// stopAll stops every server and waits for them. Called on shutdown, so lasso
// never exits ahead of a child (or leaves a sandbox running).
func (m *pluginManager) stopAll() {
	if m == nil {
		return
	}
	m.reconcileMu.Lock()
	defer m.reconcileMu.Unlock()
	m.mu.Lock()
	all := make([]*pluginServer, 0, len(m.servers))
	for _, s := range m.servers {
		all = append(all, s)
	}
	m.servers = map[string]*pluginServer{}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range all {
		wg.Add(1)
		go func() { defer wg.Done(); s.stop() }()
	}
	wg.Wait()
	m.releaseRunner()
}

func (m *pluginManager) entry(name string) *pluginEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.entries[name]
}

func (m *pluginManager) serverFor(name string) *pluginServer {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.servers[name]
}

// enable approves the plugin's CURRENT permissions. want, when given, is the
// fingerprint the human was shown; a manifest that changed between the
// listing and the click is refused rather than approved sight unseen.
func (m *pluginManager) enable(name, want string) error {
	m.rescan()
	e := m.entry(name)
	if e == nil {
		return errPluginNotFound
	}
	if e.Man == nil {
		return fmt.Errorf("plugin %q is invalid: %s", name, e.Err)
	}
	if want != "" && want != e.FP {
		return errPluginChanged
	}
	if err := updatePluginGrant(name, func(g *pluginGrant) { g.Approved = e.FP }); err != nil {
		return err
	}
	log.Printf("plugins:  %s enabled (permissions %s…)", name, e.FP[:12])
	m.reconcile()
	m.noteIfChanged()
	return nil
}

func (m *pluginManager) disable(name string) error {
	if m.entry(name) == nil {
		m.rescan()
		if m.entry(name) == nil {
			return errPluginNotFound
		}
	}
	if err := updatePluginGrant(name, func(g *pluginGrant) { g.Approved = "" }); err != nil {
		return err
	}
	log.Printf("plugins:  %s disabled", name)
	m.reconcile()
	m.noteIfChanged()
	return nil
}

func (m *pluginManager) setTrusted(name string, trusted bool) error {
	if m.entry(name) == nil {
		m.rescan()
		if m.entry(name) == nil {
			return errPluginNotFound
		}
	}
	if err := updatePluginGrant(name, func(g *pluginGrant) { g.Trusted = trusted }); err != nil {
		return err
	}
	if trusted {
		log.Printf("plugins:  %s marked TRUSTED — its MCP server runs on the host, outside the sandbox", name)
	} else {
		log.Printf("plugins:  %s no longer trusted — its MCP server runs in an isb sandbox", name)
	}
	m.reconcile()
	m.noteIfChanged()
	return nil
}

// setVM is the operator's isolation choice for a sandboxed plugin: a VM (its
// own kernel; slower start) or a container. A flip restarts the server; it is
// stored even while the plugin is trusted (trusted wins until untrusted).
func (m *pluginManager) setVM(name string, vm bool) error {
	if m.entry(name) == nil {
		m.rescan()
		if m.entry(name) == nil {
			return errPluginNotFound
		}
	}
	if err := updatePluginGrant(name, func(g *pluginGrant) { g.VM = vm }); err != nil {
		return err
	}
	log.Printf("plugins:  %s isolation set to %s", name, map[bool]string{true: "a VM", false: "a container"}[vm])
	m.reconcile()
	m.noteIfChanged()
	return nil
}

var (
	errPluginNotFound = errors.New("no such plugin")
	errPluginChanged  = errors.New("the plugin's permissions changed since they were shown; review them again")
)

// ---------------------------------------------------------------------------
// the listing (GET /api/plugins)
// ---------------------------------------------------------------------------

type pluginsPayload struct {
	Dir     string          `json:"dir"`
	Sandbox sandboxStatus   `json:"sandbox"`
	Plugins []pluginPayload `json:"plugins"`
}

type pluginPayload struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Dir         string `json:"dir"`
	State       string `json:"state"`
	Error       string `json:"error,omitempty"`
	Trusted     bool   `json:"trusted"`
	// VM is the operator's isolation choice; Isolation is the effect (host
	// when trusted, else container or vm).
	VM          bool                 `json:"vm"`
	Isolation   string               `json:"isolation"`
	Fingerprint string               `json:"fingerprint,omitempty"`
	Permissions pluginPerms          `json:"permissions"`
	Tabs        []pluginTabOut       `json:"tabs"`
	Views       []pluginTabOut       `json:"views"`
	MCP         *pluginMCPPayload    `json:"mcp,omitempty"`
	Themes      []pluginThemeOut     `json:"themes"`
	Fonts       []pluginFontOut      `json:"fonts"`
	ChatStyles  []pluginChatStyleOut `json:"chat_styles"`
	// Warnings are problems that do not invalidate the plugin — a theme whose
	// key is taken, and so skipped — shown in Settings beside it.
	Warnings []string `json:"warnings"`
	// Source is how the plugin was installed (github / linked / local), and
	// DataDir its one writable directory (created on its server's first start).
	Source  pluginSourceOut `json:"source"`
	DataDir string          `json:"data_dir"`
}

type pluginTabOut struct {
	ID       string `json:"id"`
	GlobalID string `json:"global_id"`
	Label    string `json:"label"`
	Icon     string `json:"icon"`
	Src      string `json:"src"`
}

type pluginMCPPayload struct {
	Status    string   `json:"status"`
	Detail    string   `json:"detail,omitempty"`
	Tools     []string `json:"tools"`
	Sandboxed bool     `json:"sandboxed"`
}

const (
	pluginMCPStopped     = "stopped"
	pluginMCPStarting    = "starting"
	pluginMCPRunning     = "running"
	pluginMCPError       = "error"
	pluginMCPUnavailable = "unavailable"
)

// pluginTabSrc is where the frontend frames a tab: lasso's own /plugins/ route
// for an entry (each segment escaped, since the path came from a manifest), or
// the URL as given.
func pluginTabSrc(name string, t pluginTabSpec) string {
	if t.URL != "" {
		return t.URL
	}
	c, _ := cleanPluginPath(t.Entry)
	segs := strings.Split(c, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return "/plugins/" + name + "/" + strings.Join(segs, "/")
}

func (m *pluginManager) listing() pluginsPayload {
	grants := loadPluginGrants()
	m.mu.Lock()
	names := make([]string, 0, len(m.entries))
	for n := range m.entries {
		names = append(names, n)
	}
	sort.Strings(names)
	entries := make([]*pluginEntry, 0, len(names))
	servers := map[string]*pluginServer{}
	for _, n := range names {
		entries = append(entries, m.entries[n])
		if s := m.servers[n]; s != nil {
			servers[n] = s
		}
	}
	elsewhere := m.runnerElsewhere
	m.mu.Unlock()

	out := pluginsPayload{Dir: m.dir, Sandbox: currentSandboxStatus(), Plugins: []pluginPayload{}}
	for _, e := range entries {
		g := grants[e.Name]
		p := pluginPayload{
			Name:        e.Name,
			Dir:         e.Dir,
			State:       e.state(g),
			Error:       e.Err,
			Trusted:     g.Trusted,
			VM:          g.VM,
			Isolation:   pluginIsolation(g),
			Fingerprint: e.FP,
			Permissions: emptyPluginPerms(),
			Tabs:        []pluginTabOut{},
			Views:       []pluginTabOut{},
			Themes:      []pluginThemeOut{},
			Fonts:       []pluginFontOut{},
			ChatStyles:  []pluginChatStyleOut{},
			Warnings:    []string{},
			Source:      e.Src.out(),
			DataDir:     m.dataDir(e.Name),
		}
		if e.Man != nil {
			p.Version, p.Description = e.Man.Version, e.Man.Description
			p.Permissions = e.Man.perms()
			if p.State == pluginStateEnabled {
				for _, t := range e.Man.Tabs {
					p.Tabs = append(p.Tabs, pluginTabOut{
						ID:       t.ID,
						GlobalID: "plugin:" + e.Name + ":" + t.ID,
						Label:    t.Label,
						Icon:     t.Icon,
						Src:      pluginTabSrc(e.Name, t),
					})
				}
				for _, t := range e.Man.Views {
					p.Views = append(p.Views, pluginTabOut{
						ID:       t.ID,
						GlobalID: "plugin:" + e.Name + ":" + t.ID,
						Label:    t.Label,
						Icon:     t.Icon,
						Src:      pluginTabSrc(e.Name, t),
					})
				}
			}
			appearanceListing(&p, e, p.State == pluginStateEnabled)
			if e.Man.MCP != nil {
				mp := &pluginMCPPayload{Status: pluginMCPStopped, Tools: []string{}, Sandboxed: !g.Trusted}
				if s := servers[e.Name]; s != nil && p.State == pluginStateEnabled {
					st := s.snapshot()
					mp.Status, mp.Detail, mp.Tools, mp.Sandboxed = st.Status, st.Detail, st.Tools, s.isolation != pluginIsolationHost
				} else if p.State == pluginStateEnabled && elsewhere != "" {
					mp.Status, mp.Detail = pluginMCPUnavailable, elsewhere
				}
				p.MCP = mp
			}
		}
		out.Plugins = append(out.Plugins, p)
	}
	return out
}

// ---------------------------------------------------------------------------
// HTTP
// ---------------------------------------------------------------------------

// serveAPI answers /api/plugins and /api/plugins/… — ordinary UI routes behind
// UI_AUTH like the rest of the app.
func (m *pluginManager) serveAPI(w http.ResponseWriter, r *http.Request) {
	if m == nil {
		http.Error(w, "plugins are not configured on this lasso", http.StatusServiceUnavailable)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/plugins"), "/")
	if rest == "" {
		if r.Method != http.MethodGet {
			http.Error(w, "GET", http.StatusMethodNotAllowed)
			return
		}
		m.rescan()
		writeJSON(w, m.listing())
		return
	}
	if m.serveInstallAPI(w, r, rest) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	if rest == "reload" {
		m.rescan()
		m.changed() // an explicit reload always nudges every tab
		writeJSON(w, m.listing())
		return
	}
	name, action, ok := strings.Cut(rest, "/")
	if !ok || !pluginNameRE.MatchString(name) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	var err error
	switch action {
	case "enable":
		var in struct {
			Fingerprint string `json:"fingerprint"`
		}
		_ = json.Unmarshal(body, &in)
		err = m.enable(name, in.Fingerprint)
	case "disable":
		err = m.disable(name)
	case "trust":
		var in struct {
			Trusted *bool `json:"trusted"`
		}
		if json.Unmarshal(body, &in) != nil || in.Trusted == nil {
			http.Error(w, `body must be {"trusted": true|false}`, http.StatusBadRequest)
			return
		}
		err = m.setTrusted(name, *in.Trusted)
	case "isolation":
		var in struct {
			VM *bool `json:"vm"`
		}
		if json.Unmarshal(body, &in) != nil || in.VM == nil {
			http.Error(w, `body must be {"vm": true|false}`, http.StatusBadRequest)
			return
		}
		err = m.setVM(name, *in.VM)
	case "restart":
		err = m.restart(name)
	case "call":
		m.serveCall(w, r, name, body)
		return
	default:
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	switch {
	case errors.Is(err, errPluginNotFound):
		http.Error(w, fmt.Sprintf("no plugin %q", name), http.StatusNotFound)
	case errors.Is(err, errPluginChanged):
		http.Error(w, err.Error(), http.StatusConflict)
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		writeJSON(w, m.listing())
	}
}

// serveCall is a plugin TAB calling its own backend: POST
// /api/plugins/<name>/call {tool, arguments}. The name in the path is the
// plugin the parent page routed the request from (it knows which iframe spoke),
// and the tool must be one of THAT plugin's — so tab A can never call B's tools
// by naming them, prefixed or not.
func (m *pluginManager) serveCall(w http.ResponseWriter, r *http.Request, name string, body []byte) {
	var in struct {
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(body, &in); err != nil || in.Tool == "" {
		http.Error(w, `body must be {"tool": "...", "arguments": {...}}`, http.StatusBadRequest)
		return
	}
	s := m.serverFor(name)
	if s == nil {
		if m.entry(name) == nil {
			http.Error(w, fmt.Sprintf("no plugin %q", name), http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("plugin %q has no running MCP server", name), http.StatusConflict)
		return
	}
	var args any
	if len(in.Arguments) > 0 && string(in.Arguments) != "null" {
		var obj map[string]any
		if err := json.Unmarshal(in.Arguments, &obj); err != nil {
			http.Error(w, "arguments must be a JSON object", http.StatusBadRequest)
			return
		}
		args = obj
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	res, err := s.callOwn(ctx, in.Tool, args)
	switch {
	case errors.Is(err, errNotPluginTool):
		http.Error(w, fmt.Sprintf("%q is not one of plugin %q's tools", in.Tool, name), http.StatusForbidden)
	case errors.Is(err, errPluginNotRunning):
		http.Error(w, err.Error(), http.StatusConflict)
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadGateway)
	default:
		writeJSON(w, res)
	}
}

// pluginDocCSP is on every /plugins/ response. `sandbox` without
// allow-same-origin is what makes the document an opaque origin whether it is
// framed or opened top-level, so its scripts cannot ride the human's session to
// lasso's API. Never add allow-same-origin.
const pluginDocCSP = "sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads"

// serveFiles answers GET /plugins/<name>/<path> from an enabled, approved
// plugin's directory. Everything else is a 404 that does not say why — a
// disabled plugin's files are nobody's business.
func (m *pluginManager) serveFiles(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", pluginDocCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-cache") // plugins are edited in place
	h.Set("Referrer-Policy", "no-referrer")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET", http.StatusMethodNotAllowed)
		return
	}
	if m == nil {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/plugins/")
	name, rel, _ := strings.Cut(rest, "/")
	if name == "_sdk" {
		servePluginSDK(w, r, rel)
		return
	}
	if !pluginNameRE.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	e := m.entry(name)
	if e == nil || e.Man == nil || e.state(loadPluginGrants()[name]) != pluginStateEnabled {
		http.NotFound(w, r)
		return
	}
	if rel == "" {
		rel = "index.html"
	}
	clean, err := cleanPluginPath(rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// os.Root refuses any path — `..`, or a symlink anywhere along it — that
	// would resolve outside the plugin directory, which is the check that
	// matters: the string checks above cannot see a symlink.
	root, err := os.OpenRoot(e.Dir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()
	f, err := root.Open(clean)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if st.IsDir() {
		_ = f.Close()
		clean = path.Join(clean, "index.html")
		if f, err = root.Open(clean); err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		if st, err = f.Stat(); err != nil || st.IsDir() {
			http.NotFound(w, r)
			return
		}
	}
	if !st.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	// Font files get their registered type explicitly: Go's own table has none
	// of them, and nosniff (above) means a wrong or missing type is a font the
	// browser refuses to load.
	if ct, ok := pluginFontTypes[strings.ToLower(path.Ext(clean))]; ok {
		h.Set("Content-Type", ct)
	}
	http.ServeContent(w, r, path.Base(clean), st.ModTime(), f)
}
