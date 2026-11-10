package main

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// The creator's OMP model list is not a compiled-in guess about what a user
// runs: it is read from the roles and models that host's omp is actually
// configured to use. omp assigns a model to each ROLE in
// ~/.omp/agent/config.yml — the named roles under `modelRoles` (default, smol,
// slow, task, vision, designer, plus any custom role) — and each of those role
// names is also a model alias ("@slow"). The per-task-agent assignments under
// `task.agentModelOverrides` and each role's fallback models under
// `retry.fallbackChains` contribute their concrete selectors too. Those
// selectors are exactly the models the user has authenticated, picked
// deliberately, and already runs work on, so they are the right candidate set
// for `omp --model`.
//
// Deliberately NOT read:
//
//   - `enabledModels` / `modelTags` — catalog filtering and labels, not role
//     assignments.
//
// The path mirrors syncOmpTheme's: ~/.omp/agent is omp's config dir on every
// host lasso drives (`omp config path`), and a host where omp has never run has
// no file to read — the compiled-in suggestions then stand in (hostHarnesses).

// ompConfigRoles is the slice of omp's config.yml that carries role→model
// assignments. Both fields decode as yaml.Node rather than map[string]string so
// the config's own key order survives: the roles a user put first are the ones
// the picker should offer first, which a Go map would shuffle on every request.
type ompConfigRoles struct {
	ModelRoles yaml.Node `yaml:"modelRoles"`
	Retry      struct {
		FallbackChains yaml.Node `yaml:"fallbackChains"`
	} `yaml:"retry"`
	Task struct {
		AgentModelOverrides yaml.Node `yaml:"agentModelOverrides"`
	} `yaml:"task"`
}

// ompRoleModels extracts model selections from an omp config.yml: every named
// role's @alias first, in config order, then the concrete selectors (each
// role's assignment, the per-task-agent overrides, and each role's fallback
// models). Selectors are deduplicated; role aliases are deduplicated by their
// names. The returned map is each alias's assigned selector, for the picker's
// hover hint. A file it can't parse yields nothing rather than a partial list —
// the caller then falls back to the compiled-in suggestions.
func ompRoleModels(data []byte) ([]string, map[string]string) {
	var cfg ompConfigRoles
	if yaml.Unmarshal(data, &cfg) != nil {
		return nil, nil
	}
	var models []string
	var aliases []string
	aliasModels := map[string]string{}
	seen := map[string]bool{}
	add := func(raw string) string {
		sel := ompModelSelector(raw)
		if sel == "" {
			return ""
		}
		// A fallback entry may be a wildcard ("google/*") that matches models
		// without naming one — not a `--model` selector, so not a suggestion.
		if strings.Contains(sel, "*") {
			return ""
		}
		if !seen[sel] {
			seen[sel] = true
			models = append(models, sel)
		}
		return sel
	}
	for _, n := range []*yaml.Node{&cfg.ModelRoles, &cfg.Task.AgentModelOverrides} {
		if n.Kind != yaml.MappingNode {
			continue
		}
		// A mapping node's Content is a flat [key, value, key, value, …]. Only
		// named modelRoles are @aliases on the main session: a task-agent
		// override names an agent, not a role, and must stay a model suggestion
		// only. An alias is listed only when its assignment is a usable
		// selector, since the hover hint has to name the model it resolves to.
		for i := 1; i < len(n.Content); i += 2 {
			v := n.Content[i]
			if n == &cfg.ModelRoles {
				if v.Kind != yaml.ScalarNode {
					continue
				}
				role := strings.TrimSpace(n.Content[i-1].Value)
				sel := add(v.Value)
				if role == "" || sel == "" {
					continue
				}
				alias := "@" + role
				if _, ok := aliasModels[alias]; !ok {
					aliases = append(aliases, alias)
				}
				aliasModels[alias] = sel
				continue
			}
			if v.Kind == yaml.ScalarNode {
				add(v.Value)
			}
		}
	}
	// Fallback chains map a role (or a model selector) to the ordered models
	// to try after a retryable failure. Each chain is a YAML list of
	// selectors, in the order omp tries them — appended after the primaries,
	// in config order, since they are the user's second choices.
	if n := &cfg.Retry.FallbackChains; n.Kind == yaml.MappingNode {
		for i := 1; i < len(n.Content); i += 2 {
			chain := n.Content[i]
			switch chain.Kind {
			case yaml.SequenceNode:
				for _, item := range chain.Content {
					if item.Kind != yaml.ScalarNode {
						continue
					}
					add(item.Value)
				}
			case yaml.ScalarNode:
				add(chain.Value)
			}
		}
	}
	out := make([]string, 0, len(aliases)+len(models))
	out = append(out, aliases...)
	out = append(out, models...)
	return out, aliasModels
}

// ompModelSelector normalizes one role assignment into a `--model` value. omp's
// role syntax lets a selector pin a thinking level with a trailing ":<level>"
// ("anthropic/claude-opus-5:high"); lasso has its own effort field, so the level
// is dropped here rather than smuggled into --model where it would silently
// override — or fight — what the user picked in the creator. Only a suffix that
// IS one of omp's levels is stripped: model ids carry colons of their own
// (openrouter's "…:free"), and cutting one of those would break the selector.
func ompModelSelector(raw string) string {
	s := strings.TrimSpace(raw)
	i := strings.LastIndex(s, ":")
	if i <= 0 {
		return s
	}
	if !ompEffortLevel(s[i+1:]) {
		return s
	}
	return strings.TrimSpace(s[:i])
}

// ompEffortLevel reports whether s names a rung on omp's --thinking ladder (the
// harness registry's own list, so the two can't drift).
func ompEffortLevel(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, lvl := range harnessByID("omp").EffortLevels {
		if s == lvl {
			return true
		}
	}
	return false
}

// hostHarnesses is the harness registry as served for one host: the compiled-in
// table (harness.go) with omp's host-specific halves filled from that host's
// omp config: its model suggestions replaced by @role aliases (first) and the
// models they assign, its alias→model hints, and its model presets
// (omppresets.go) listed for the creator's preset select.
// Everything else in the registry — labels, effort ladders, plan-mode support —
// is compiled-in and host-independent.
//
// The registry slice and its entries are shared global state, so this copies
// before touching the omp entry. A host whose omp says nothing keeps the
// compiled-in suggestions and offers no presets.
func hostHarnesses(b Backend) []harnessDef {
	home, err := b.HomeDir()
	if err != nil || home == "" {
		return harnesses
	}
	data := ompGlobalConfig(b, home)
	models, aliases := ompRoleModels(data)
	presets := ompPresetNames(data)
	if len(models) == 0 && len(presets) == 0 {
		return harnesses
	}
	out := make([]harnessDef, len(harnesses))
	copy(out, harnesses)
	for i := range out {
		if out[i].ID != "omp" {
			continue
		}
		if len(models) > 0 {
			out[i].ModelSuggestions = models
		}
		out[i].ModelAliases = aliases
		out[i].ModelPresets = presets
	}
	return out
}
