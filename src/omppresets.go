package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// omp model presets: the creator's "Model preset" select.
//
// A preset is omp's named snapshot of every role assignment plus
// defaultThinkingLevel, kept under `modelPresets` in config.yml
// (`/modelpreset save`, the /models Roles view). omp has no launch flag for one —
// `/modelpreset switch` is interactive-only and WRITES the roles into the user's
// config, which a launch must never do. So lasso applies a preset the way it
// applies plan mode and the theme pin: as keys in the agent's own `--config`
// overlay (ompplan.go), merged over the user's config for that one run.
//
// The overlay carries what a switch would leave behind:
//
//   - the preset's roles, verbatim (selectors, `:level` suffixes, `@role`
//     aliases and comma-separated candidates all mean what they mean in omp);
//   - a `null` for every role a lower layer (the global or the project config)
//     assigns and the preset leaves out. A switch CLEARS such a role, and a
//     `null` in an overlay is omp's "unset" — measured on omp 18.14:
//     `modelRoles: {default: null}` dropped the global default and the session
//     came up on the provider default. Without it a hand-written preset that
//     omits `vision` would quietly inherit the global vision model;
//   - the preset's defaultThinkingLevel, when it has one.
//
// The overlay is a CLI layer, above the project config, so the agent's roles
// are exactly the preset's even in a repo whose .omp/config.yml assigns some —
// the user picked the preset for this agent. `--model` and `--thinking` are
// runtime overrides above every config layer, so the creator's explicit model
// and effort still win for the main session (measured: `--model
// deepseek/deepseek-v4-flash --thinking low` over the `cyber` overlay came up
// on that model at `low`), while every other role keeps the preset's.
//
// Name resolution follows omp's: a preset is resolved WHOLE from the highest
// layer that defines it — the project config (`<cwd>/.omp/config.yml`) before
// the global one — never merged across layers, and a `null` project entry is
// ignored so the global preset still applies. The creator lists the global
// presets (the form has no work dir yet); a launch resolves in the agent's
// work dir, as omp itself would.

// ompPresetsFile is the slice of an omp config.yml presets need. yaml.Node
// keeps the config's own order: presets listed in the order the user saved
// them, roles written in the order the preset has them.
type ompPresetsFile struct {
	ModelPresets yaml.Node `yaml:"modelPresets"`
	ModelRoles   yaml.Node `yaml:"modelRoles"`
}

// ompPreset is one resolved preset: its roles in config order and its
// defaultThinkingLevel ("" when it sets none).
type ompPreset struct {
	roles    [][2]string // role, selector
	thinking string
}

func parseOmpPresetsFile(data []byte) (ompPresetsFile, bool) {
	var f ompPresetsFile
	if len(data) == 0 || yaml.Unmarshal(data, &f) != nil {
		return ompPresetsFile{}, false
	}
	return f, true
}

// ompPresetNames lists the presets an omp config.yml defines, in config order.
// Unparseable YAML yields nothing.
func ompPresetNames(data []byte) []string {
	f, _ := parseOmpPresetsFile(data)
	return f.names()
}

// names lists this file's presets in config order. A null or non-mapping entry
// is not a preset (a null is how a higher layer hides one), so it is skipped.
func (f ompPresetsFile) names() []string {
	if f.ModelPresets.Kind != yaml.MappingNode {
		return nil
	}
	var out []string
	c := f.ModelPresets.Content
	for i := 0; i+1 < len(c); i += 2 {
		if c[i+1].Kind == yaml.MappingNode {
			out = append(out, c[i].Value)
		}
	}
	return out
}

// lookup returns the named preset from this file, ok=false when the file does
// not define it as a mapping (absent, null, or malformed).
func (f ompPresetsFile) lookup(name string) (ompPreset, bool) {
	if f.ModelPresets.Kind != yaml.MappingNode {
		return ompPreset{}, false
	}
	c := f.ModelPresets.Content
	for i := 0; i+1 < len(c); i += 2 {
		if c[i].Value != name || c[i+1].Kind != yaml.MappingNode {
			continue
		}
		var p ompPreset
		fields := c[i+1].Content
		for j := 0; j+1 < len(fields); j += 2 {
			k, v := fields[j].Value, fields[j+1]
			switch {
			case k == "defaultThinkingLevel" && v.Kind == yaml.ScalarNode && v.Tag != "!!null":
				p.thinking = v.Value
			case k == "modelRoles" && v.Kind == yaml.MappingNode:
				for r := 0; r+1 < len(v.Content); r += 2 {
					if sel := v.Content[r+1]; sel.Kind == yaml.ScalarNode && sel.Tag != "!!null" && sel.Value != "" {
						p.roles = append(p.roles, [2]string{v.Content[r].Value, sel.Value})
					}
				}
			}
		}
		return p, true
	}
	return ompPreset{}, false
}

// roleNames lists the roles this file assigns under modelRoles.
func (f ompPresetsFile) roleNames() []string {
	if f.ModelRoles.Kind != yaml.MappingNode {
		return nil
	}
	var out []string
	for i := 0; i+1 < len(f.ModelRoles.Content); i += 2 {
		out = append(out, f.ModelRoles.Content[i].Value)
	}
	return out
}

// overlay renders the preset as config-overlay YAML: its roles, a null for each
// of lowerRoles it leaves out, and its thinking level. Built as a node tree, not
// by string concatenation, so a selector YAML would read as something else (an
// `@slow` alias, a bare `null`, a leading `*`) is quoted by the encoder.
func (p ompPreset) overlay(name string, lowerRoles []string) []byte {
	str := func(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s} }
	roles := &yaml.Node{Kind: yaml.MappingNode}
	set := map[string]bool{}
	for _, r := range p.roles {
		set[r[0]] = true
		roles.Content = append(roles.Content, str(r[0]), str(r[1]))
	}
	for _, r := range lowerRoles {
		if set[r] {
			continue
		}
		set[r] = true
		roles.Content = append(roles.Content, str(r), &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"})
	}
	doc := &yaml.Node{Kind: yaml.MappingNode}
	if len(roles.Content) > 0 {
		doc.Content = append(doc.Content, str("modelRoles"), roles)
	}
	if p.thinking != "" {
		doc.Content = append(doc.Content, str("defaultThinkingLevel"), str(p.thinking))
	}
	body, err := yaml.Marshal(doc)
	if err != nil || len(doc.Content) == 0 {
		body = nil
	}
	return append([]byte("# omp model preset "+fmt.Sprintf("%q", name)+", applied by lasso for this run.\n"), body...)
}

// ompGlobalConfig reads host b's global omp config.yml, nil when there is none
// or it can't be read.
func ompGlobalConfig(b Backend, home string) []byte {
	data, err := b.ReadFile(filepath.Join(ompAgentDir(home), "config.yml"))
	if err != nil {
		return nil
	}
	return data
}

// ompPresetOverlay resolves preset name on host b for an omp started in
// projectDir ("" for none) and returns its overlay YAML. An error means the
// preset does not exist there: the caller must not launch as though it did.
func ompPresetOverlay(b Backend, projectDir, name string) ([]byte, error) {
	home, err := b.HomeDir()
	if err != nil || home == "" {
		return nil, fmt.Errorf("omp model preset %q: the host has no home directory", name)
	}
	global, _ := parseOmpPresetsFile(ompGlobalConfig(b, home))
	var project ompPresetsFile
	if projectDir != "" {
		if data, err := b.ReadFile(filepath.Join(projectDir, ".omp", "config.yml")); err == nil {
			project, _ = parseOmpPresetsFile(data)
		}
	}
	p, ok := project.lookup(name)
	if !ok {
		p, ok = global.lookup(name)
	}
	// The host is left out of these messages: they reach /mcp too, where lasso's
	// own box must be named by its hostname (mcphost.go), and every caller
	// already knows which host it asked.
	if !ok {
		defined := append(project.names(), global.names()...)
		if len(defined) == 0 {
			return nil, fmt.Errorf("omp model preset %q not found: the host's omp config defines no presets", name)
		}
		return nil, fmt.Errorf("omp model preset %q not found (defined on the host: %s)", name, strings.Join(defined, ", "))
	}
	return p.overlay(name, append(global.roleNames(), project.roleNames()...)), nil
}

// normalizeModelPreset keeps a preset only for a harness that can take one —
// omp, the harness lasso reaches through a config overlay. Same rule as
// normalizeAdvisor: the record must not claim a preset that never applied.
func normalizeModelPreset(agent, preset string) string {
	if !harnessByID(agent).stagesConfigOverlay {
		return ""
	}
	return strings.TrimSpace(preset)
}
