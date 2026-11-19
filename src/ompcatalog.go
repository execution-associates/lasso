package main

// GET /api/omp-models?host=<h>[&refresh=1] — every chat model omp can run on
// that host, for the creator's model dialog.
//
// The creator's suggestions (ompmodels.go) are only what omp's config ASSIGNS
// to a role; picking anything else meant typing a selector from memory. The
// full catalog is omp's own answer to "what can I run here": `omp models
// --json` lists the chat models of every provider the host has credentials for
// (and its custom models.yml), with the `selector` `--model` takes. It is read
// on the host, in a login shell, the way botToolVersion reads `omp --version`,
// because the catalog depends on that host's logins and that host's omp.
//
// It takes ~1.5s locally (more over ssh), so the answer is cached per host for
// ompCatalogTTL; `refresh=1` bypasses the cache. A failure is reported in
// `error` with an empty list (the dialog still takes a typed selector) and is
// not cached.

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"
)

const ompCatalogTTL = 5 * time.Minute

// ompCatalogModel is one row of the dialog: the fields it filters on and shows.
type ompCatalogModel struct {
	Selector      string   `json:"selector"`
	Name          string   `json:"name,omitempty"`
	Provider      string   `json:"provider"`
	ContextWindow int      `json:"context_window,omitempty"`
	Thinking      []string `json:"thinking,omitempty"`
}

type ompCatalogEntry struct {
	at     time.Time
	models []ompCatalogModel
}

var ompCatalogCache = struct {
	sync.Mutex
	byHost map[string]ompCatalogEntry
}{byHost: map[string]ompCatalogEntry{}}

// parseOmpCatalog reads `omp models --json` ({"models":[…]}), keeping chat
// models with a selector, in omp's own order (grouped by provider).
func parseOmpCatalog(data []byte) ([]ompCatalogModel, error) {
	var doc struct {
		Models []struct {
			Kind          string   `json:"kind"`
			Selector      string   `json:"selector"`
			Name          string   `json:"name"`
			Provider      string   `json:"provider"`
			ContextWindow int      `json:"contextWindow"`
			Thinking      []string `json:"thinking"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, errors.New("omp models --json did not print JSON (is this omp too old for --json?)")
	}
	out := make([]ompCatalogModel, 0, len(doc.Models))
	for _, m := range doc.Models {
		if m.Selector == "" || (m.Kind != "" && m.Kind != "chat") {
			continue
		}
		out = append(out, ompCatalogModel{
			Selector:      m.Selector,
			Name:          m.Name,
			Provider:      m.Provider,
			ContextWindow: m.ContextWindow,
			Thinking:      m.Thinking,
		})
	}
	return out, nil
}

// ompCatalog returns host's omp catalog, from the cache unless refresh.
func ompCatalog(b Backend, refresh bool) ([]ompCatalogModel, error) {
	host := b.Name()
	if !refresh {
		ompCatalogCache.Lock()
		e, ok := ompCatalogCache.byHost[host]
		ompCatalogCache.Unlock()
		if ok && time.Since(e.at) < ompCatalogTTL {
			return e.models, nil
		}
	}
	home, err := b.HomeDir()
	if err != nil {
		return nil, err
	}
	out, err := botRun(b, home, "omp", []string{"models", "--json"}, nil)
	if err != nil {
		return nil, err
	}
	models, err := parseOmpCatalog(out)
	if err != nil {
		return nil, err
	}
	ompCatalogCache.Lock()
	ompCatalogCache.byHost[host] = ompCatalogEntry{at: time.Now(), models: models}
	ompCatalogCache.Unlock()
	return models, nil
}

func serveOmpModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	host, ok := hostParam(r)
	if !ok {
		http.Error(w, "host not available", http.StatusBadRequest)
		return
	}
	b, err := hostBackend(host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	models, err := ompCatalog(b, r.URL.Query().Get("refresh") == "1")
	if err != nil {
		writeJSON(w, map[string]any{"models": []ompCatalogModel{}, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"models": models})
}
