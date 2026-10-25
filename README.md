# Server Panel

A self-hosted, open source web panel for running game servers, in the spirit of CubeCoders AMP.
The first games are **Minecraft (Java)** and **Project Zomboid**, on Debian Linux with Docker.

> Status: early skeleton. The panel and agent connect and you can log in. Installing and running game servers comes next.

## How it fits together

- **Panel** (`cmd/panel`): the web UI, the REST API, users, and state.
- **Agent** (`cmd/agent`): runs on each machine, dials out to the panel over WebSocket, and starts game servers as Docker containers.
- **Templates** (`templates/*.yaml`): one file per game says how to install, start, stop, configure, and read stats from it. Adding a game means adding a template, not code.
- **Integrations**: everything goes through the public API (`docs/api.md`), so a Discord bot or stats site uses the same door as the web UI.

## Run it

```sh
cp .env.example .env   # set a password and an agent token
docker compose up -d
```

Then open http://localhost:8080.

## Develop

```sh
go test ./...
PANEL_ADMIN_PASSWORD=dev PANEL_AGENT_TOKEN=dev go run ./cmd/panel
AGENT_TOKEN=dev go run ./cmd/agent
cd web && npm install && npm run dev   # UI on :5173, proxies /api to :8080
```

## Roadmap

1. Skeleton: panel and agent connected, login, Docker Compose *(this)*
2. First game end to end: install, start, stop, live console
3. Daily use: config editor, file manager, backups, stats API with tokens and webhooks
4. Users: roles, per-instance access, audit log
5. Scale out: remote agents, scheduler, auto-updates, Discord bot

See [CONTRIBUTING.md](CONTRIBUTING.md) to help. Licensed under MIT.
