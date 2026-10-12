# Transcription (`src/transcribe.go`, `src/pluginmcp.go:lookupSecret`)

`POST /api/transcribe` turns a short recording into text for lasso's own clients: a desktop app's push-to-talk, a future mic button in the web composer. The client sends a WAV; lasso sends it to the provider the `transcribe` setting names and answers the transcript. The client holds no provider key, and one setting decides where every client's audio goes.

## A core route, not a plugin

A plugin could host this, and it was costed as one. Three things decided against it:

- **A plugin cannot reach a provider on the server.** The point of `openai-compatible` with a loopback or tailnet `base_url` is a whisper (or Parakeet) server lasso's own machine runs, so audio never leaves the box. A plugin's sandbox reaches the network only through isb's egress, and isb refuses IP literals (`checkPluginHost`), so `127.0.0.1:8080` is unreachable from inside one.
- **A plugin call body is cut at 4 MiB** (`/api/plugins/<name>/call` reads at most that), and the audio would have to travel as base64 inside JSON: a third larger, so about 95 seconds of 16 kHz mono instead of 120, and silently truncated past that rather than refused.
- **`/call` is built for a tab's iframe.** It trusts the plugin name in the path because lasso's own page put it there. A native client calling it is off-label, and the plugin needs a human to approve it and a running sandbox before the first word is transcribed.

The core route is about the same amount of code, takes raw WAV, and needs no approval or sandbox.

## Limits

Each limit is checked before the provider is called, so a refused request costs nothing upstream.

| limit | answer | why |
| --- | --- | --- |
| provider `off` (the default) or misconfigured | 503, with the reason | GET says the same as `available:false` + `reason`, which a client shows at onboarding. |
| `prompt` over 512 characters, `language` not a language code | 400 | Both go to the provider as form fields. |
| 2 transcriptions in flight | 429 | A slot is taken **before** the body is read, so at most 2 × 4 MiB of audio is in memory at once. A buffered channel, non-blocking: a third caller is told now rather than queued behind a 60 s upstream. |
| body over 4 MiB | 413 | `http.MaxBytesReader`. 120 s of 16 kHz mono PCM16 is 3.84 MB. |
| empty body, or a WAV with no samples | 400 | |
| not a PCM16 WAV | 415 | RIFF/WAVE, PCM (or EXTENSIBLE with a PCM subformat), 16-bit, 1-2 channels, 8-48 kHz. Parsed from the header, not trusted from `Content-Type`. |
| over 120 s of audio | 413 | The duration is the data chunk's size, clamped to the bytes actually present, so a streaming writer's placeholder size (`0xFFFFFFFF`) reads as what was sent. |
| no key | 503 | Names the variable to set, never a value. |
| provider slower than 60 s | 504 | |
| provider error | 502 | A 401/403 says only that the key was refused; anything else carries the provider's own message, cut to 200 characters. |

## Providers

Every provider speaks OpenAI's `POST {base}/audio/transcriptions`: multipart `file` (the WAV as `audio.wav`, `audio/wav`), `model`, optional `language` and `prompt`, `response_format=json`, answered as `{"text": …}`.

| provider | base_url | model | key_name |
| --- | --- | --- | --- |
| `groq` | `https://api.groq.com/openai/v1` | `whisper-large-v3-turbo` | `GROQ_API_KEY` |
| `openai` | `https://api.openai.com/v1` | `gpt-4o-mini-transcribe` | `OPENAI_API_KEY` |
| `openai-compatible` | required, http or https | none: no `model` field is sent unless one is set | none: no `Authorization` header unless one is set |

The setting stores only explicit choices; an empty field means the provider's default. It is written through `/api/transcribe/config`, which is also the `transcribe` section of `get_settings`/`update_settings`. A patch changing the provider resets the fields it does not name, since a Groq model means nothing to OpenAI. A `base_url` carrying credentials, a query or a fragment is refused.

## Keys

- **Named, never stored.** The setting holds the NAME of a variable (`key_name`). The value comes from lasso's environment, else `secret <key_name>` with a 10 s timeout: `lookupSecret`, the same resolver plugin secrets use (`pluginSecretLookup` is a var over it so its tests can stand in for the CLI). Nothing writes a key to `lasso.db`, and the settings section shows names only.
- **Cached in memory for 10 minutes**, so a burst of dictation runs the `secret` CLI once. A failed lookup is not cached, so a key set a moment ago works on the next request. Any write to the setting drops the cache, which is also how a rotated secret takes effect at once.
- **GET resolves the key too.** A provider with no key is not available, and onboarding should say so before the first recording, not after.

## Privacy

- **The audio stays in memory.** The body is read once (at most 4 MiB, so the header can be validated before anything goes upstream), then sent as the fields, the audio bytes straight from that buffer, and the closing boundary, through an `io.MultiReader` with an exact `Content-Length`. Nothing is written to disk, and a second copy of the audio is never built.
- **Nothing about a request is logged** beyond a provider name and a status code on failure: never the transcript, the audio, the prompt, or any header (the key rides in `Authorization`).
- **It is a UI_AUTH route.** It sits behind `withAuthExcept` like every `/api/*` route and is deliberately not an MCP tool: audio in tool arguments is a poor fit, and an open `/mcp` would let any caller spend the provider key.
