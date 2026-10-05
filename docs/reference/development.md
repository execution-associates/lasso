---
title: Development
description: Building lasso from source, the dev loop, and cutting a release.
order: 101
---

lasso's Go backend lives in `src/` (the module root, with `go.mod`). The frontend is a React, Vite and Tailwind (shadcn/ui) app in `src/web/`, built into `src/web/dist` and embedded into the binary with `go:embed`, so the shipped binary is self-contained. `src/web/dist` is gitignored: build it locally, and CI builds it for releases.

## Prerequisites

- [mise](https://mise.jdx.dev). `mise.toml` pins Go, bun, node and [isb](https://github.com/execution-associates/isb); `mise install` fetches them.
- [incus](https://linuxcontainers.org/incus/), for the frontend container (below).
- herdr and ttyd, to run what you build.
- `mise run dev` binds your tailscale address, so tailscale must be up.

## Tasks

| task | what it does |
| --- | --- |
| `mise run build` | Build the frontend into `src/web/dist` (in the container), then `go build` the binary to `./lasso`. |
| `mise run dev` | Build, then run the Go backend on loopback with `-dev` and the Vite dev server (hot reload) on your tailscale IP, proxying API and terminal routes to the backend. |
| `mise run test` | `go test .` in `src/`. |
| `mise run typecheck` | `tsc -b` on the frontend. |
| `mise run lint` | Biome lint on the frontend. |
| `mise run check` | `biome check --write`: format, lint fixes, import and class sorting. |
| `mise run icons` | Re-render the favicons and home-screen icons from `brand/icon/icon.png`. |
| `mise run diagram` | Render `docs/assets/architecture/*.reladraw` to SVG. |
| `mise run dev:containers` | List the per-worktree dev containers, the worktree each belongs to, and whether it still exists. |
| `mise run dev:prune` | Delete dev containers whose worktree is gone. A dry run unless you pass `-y`. Never touches a container whose worktree exists. |
| `mise run bump [major\|minor\|patch]` | Bump `lassoSemver` in `src/version.go` (default `patch`). `--commit` commits the change. |

Run `mise run typecheck` and `mise run lint` before calling frontend work done. Note that `tsc -b` is the real check: the root `tsconfig.json` has `files: []`, so a bare `tsc --noEmit` checks nothing.

```bash
mise run build
./lasso            # serves on 127.0.0.1:8090 and spawns ttyd running herdr
```

## The dev loop

`mise run dev` starts the backend on `127.0.0.1:8190`, a dev port deliberately apart from the production default `8090`. With `-dev`, a busy port bumps to the next free one, so several dev instances can run at once, and the backend's log is teed to `/tmp/lasso-dev.log` together with browser-side events. Vite serves the UI on the next free tailnet port from 5173 and prints its URL. Frontend edits reload instantly; Go changes need the task restarted.

One `mise run dev` runs per worktree; a second in the same worktree is refused. Dev servers in different worktrees run side by side.

To run lasso from inside a herdr pane (developing lasso with lasso), set `allow_nested = true` under `[experimental]` in `~/.config/herdr/config.toml`, or the embedded terminal refuses to nest.

## Why the frontend runs in a container

`bun install`, Vite, tsc and Biome are third-party code, and `bun install` runs dependencies' install scripts. On your machine that code would run as you, next to your SSH keys and credentials. So every frontend task (`build`, `dev`, `typecheck`, `lint`, `check`, `icons`) runs inside an unprivileged incus container, where a compromised dependency is an unprivileged user with one directory mounted. The Go half (`go build`, `go test`) runs on the host: Go has no install hooks, and the binary has to run there to drive herdr.

- **One container per worktree**, named after the worktree and mounting only its `src/web` (and `brand/icon` for `icons`), never the repo root, so an install script cannot write a git hook into `.git`. `node_modules` lives in each worktree's own `src/web`; only bun's download cache is shared between containers.
- **Declared, not hand-built.** The containers are described in `scripts/isb/*.yaml` and driven by isb, pinned in `mise.toml` as a prebuilt release binary. Add a mount or setting in the YAML, not with `incus config`. `scripts/container.sh` documents the arrangement.
- **Disposable.** Delete one and the next task rebuilds it from the `dev-base` image (built by `scripts/dev-base.sh`). `mise run dev` holds its container in the foreground, so the container stops when the dev server ends.
- **The diagram renders elsewhere.** `mise run diagram` uses a separate `sandbox` container (Debian, `scripts/isb/sandbox.yaml`) with only `docs/assets/architecture` mounted, since reladraw is fetched from npm at render time.

Do not run `bun` directly in `src/web/`; that puts the dependency tree back on the host.

## Style

- Frontend: Biome (`src/web/biome.json`). Two-space indent, no semicolons, double quotes, ES5 trailing commas, 80 columns. Tailwind classes are sorted by Biome's `useSortedClasses`.
- Go: standard `gofmt`.

## Releasing

Releases are cut by CI when a version tag is pushed:

```bash
mise run bump patch --commit      # bump lassoSemver in src/version.go and commit
git tag "v$(grep -oP 'lassoSemver = "\K[^"]+' src/version.go)"
git push origin main --tags
```

The [release workflow](https://github.com/execution-associates/lasso/blob/main/.github/workflows/release.yml) then:

1. Checks that the tag (minus its `v`) equals `lassoSemver` in `src/version.go`, and fails otherwise.
2. Builds the frontend with `bun install --frozen-lockfile && bun run build`.
3. Cross-compiles static binaries (`CGO_ENABLED=0`) for linux and darwin on amd64 and arm64, stamping the version in with `-ldflags -X main.lassoSemver=<version>`.
4. Publishes a GitHub Release with the binaries (`lasso-<os>-<arch>`), a `checksums.txt` of their SHA-256 sums, and `install.sh`, with generated release notes.

`lasso update` and the install script both consume these assets, and `lasso update` verifies the download against `checksums.txt`.

## Brand assets

The app icon's source is `brand/icon/icon.png`, a raster used at every size down to the 16 px browser tab; `mise run icons` regenerates the favicon, ICO and home-screen icons in `src/web/public/`. Icon URLs carry a `?v=` in `src/web/index.html` and `src/web/public/manifest.json`; bump it when the art changes so cached icons are replaced. The wordmark is `brand/lasso-wordmark.png`.

## License

lasso is licensed under the [Apache License 2.0](https://github.com/execution-associates/lasso/blob/main/LICENSE), the same license as herdr. See [NOTICE](https://github.com/execution-associates/lasso/blob/main/NOTICE).
