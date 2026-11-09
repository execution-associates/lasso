# Internal AI (`llm.go`)

lasso's own small language-model errands go through one configurable
OpenAI-compatible endpoint. The first two consumers are the auto-titler
(`autotitle.go`) and the terminal tab titler (`terminaltitle.go`): both already
send a prompt excerpt to a local agent CLI and use the answer as a name.

## Why an endpoint at all

The CLI titler chain (claude → codex → opencode → omp → pi) requires a
logged-in agent CLI on the lasso host, spends a full agent turn on what is a
one-line classification, and cannot be pointed at a small local model. An
OpenAI-compatible endpoint is the boring common denominator: llama.cpp, Ollama,
vLLM, LM Studio and every hosted provider speak it.

## Settings

One `settings` row, `llm`, holding only explicit choices:

```json
{"base_url":"http://127.0.0.1:8080/v1","model":"gpt-4.1-mini","key_name":"INTERNAL_AI_KEY"}
```

- `base_url` — the API root. `http`/`https` only; no credentials, query or
  fragment. Empty turns the feature off. Validated by `validAPIBaseURL`, shared
  with `transcribe.base_url`.
- `model` — checked against `modelNameRE`. The settings picker is editable, so
  an endpoint that hides a model from `/models` can still be typed in.
- `key_name` — where the key comes from, never the key: the name of a
  variable in lasso's environment, or a 1Password reference
  `op://vault/item/field` (`validKeyRef`), read with `op read` under lasso's
  own environment with a 10 s timeout (`lookupSecret`). The value is cached in
  memory for 10 minutes and never written to `lasso.db`, returned by a route,
  or logged. Config GET answers the reference only.

State as `GET /api/llm/config` reports it: `unconfigured` (no base URL),
`incomplete` (no model yet — a draft the group can hold), `ready`, or
`unreadable` (the stored JSON does not parse).

## Routes

| Route | Purpose |
|---|---|
| `GET /api/llm/config` | stored choices + state; no network call |
| `POST /api/llm/config` | merge-patch; "" clears a field; clears the key and model caches |
| `GET /api/llm/models` | the endpoint's `/models` (`?refresh=1` bypasses the 60s cache) |
| `POST /api/llm/test` | one tiny completion, to prove the endpoint and key work |

All are behind `UI_AUTH` like every `/api` route. A model-list failure is
reported in the body (`{models:[], error}`) rather than as an HTTP error: the
settings group around it stays usable.

`/models` parsing accepts OpenAI's `{"data":[{"id":…}]}` and Ollama's
`{"models":[{"name":…}]}`, deduped and sorted. Reads are capped at 1 MiB and
10 s; a completion at 1 MiB and 90 s.

## Titler integration

A configured endpoint goes FIRST — it was configured to be used — then the CLIs:

```
askTitlers(instruction)
  [ llm (when llmConfigured) ] → [ claude, codex, opencode, omp, pi ]
        first parseable answer wins
```

Its failure is one entry in the failure list, not fatal: the CLIs still try, so
a broken endpoint cannot break naming. Unset, the chain is exactly the old one.

The completion is one non-streaming user turn, `temperature: 0`,
`max_tokens: 200`. The instruction already tells the model not to attempt the
task, so no system message is needed. The prompt excerpt is the same ≤4000 bytes
the CLIs already receive; that and the answer are never logged.

## Invariants

- A key's value never leaves the process. Errors carry the status and at most
  200 runes of one body line, with the key replaced by `[redacted]`.
- Only `http`/`https`; no credentials in the URL.
- `get_settings` returns the `llm` section without a network call; asking for
  the section by name adds the endpoint's `models`.
- The model list is a convenience, never a constraint.
