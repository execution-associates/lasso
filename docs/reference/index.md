---
title: Reference
description: Exact flags, environment variables, files, routes and internals of the lasso binary.
order: 95
---

These pages are lookup material. Each one is checked against the source, and each one names the exact spelling of a flag, a variable or a path, so you can copy it.

| page | what it covers |
| --- | --- |
| [CLI](./cli.md) | Every `lasso` subcommand, its flags and its aliases. |
| [Configuration](./configuration.md) | Every server flag with its default, and every environment variable lasso reads. |
| [Files and directories](./files.md) | What lasso reads and writes on disk: `~/.lasso`, `lasso.db`, herdr's `config.toml`, agent CLI theme files and more. |
| [HTTP routes](./http-routes.md) | The route table, and which auth gate guards each route. |
| [Architecture](./architecture.md) | How the binary, ttyd, herdr, SSH and the browser fit together. |
| [Development](./development.md) | Building from source, the dev loop, and cutting a release. |

For the reasoning behind the security-relevant defaults, read [Security](../security.md). For the MCP tool catalog, see [MCP tools](../mcp/tools.md).
