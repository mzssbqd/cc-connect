# Project structure

This repository uses Go packages at the root and a separate React frontend.

| Path | Responsibility |
| --- | --- |
| `cmd/cc-connect/` | CLI/service entry point, plugin registration and configuration wiring |
| `config/` | TOML configuration, persistence and migration |
| `core/` | Engine, sessions, workspace routing, commands, cards, management API and shared interfaces |
| `core/panel.go` | Control-panel pages, session/model actions and settings persistence callbacks |
| `agent/*/` | Agent process/protocol adapters |
| `platform/*/` | Messaging adapters; `platform/telegram/panel.go` renders the panel and validates callback tokens |
| `daemon/` | Service installation and lifecycle |
| `web/` | React/TypeScript console and embedded frontend resources; source in `web/src/`, generated assets in `web/dist/` |
| `docs/` | Maintained setup and feature documentation |
| `tests/` | Integration, black-box and release checks; unit tests also live alongside Go source |
| `tmp/` | Ignored task-scoped diagnostics and temporary verification tools |
| `bin/` | Ignored local executable build output |

Startup loads `config.toml`, creates registered agent/platform adapters and wires
an engine per project. Platforms deliver messages to the engine, which resolves
the workspace and session before calling the agent. Panel callbacks re-enter the
same message/command route. The platform owns rendering and token validation;
core owns actions and policy. `cmd/` supplies configuration-save callbacks.

Dependencies flow from `cmd/` to `config/`, `core/` and adapters. Adapters depend
on `core/`; core does not import adapters. `web/src/` calls the management API.
Build the frontend with `cd web && pnpm run build` before `go build ./...`, because
Go embeds `web/dist/`. Run `go test ./...` and `go test ./core -run TestCUJ` for
validation. See [AGENTS.md](../AGENTS.md) for development rules and the README for
installation and startup commands.
