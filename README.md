# Watchtower

A self-hosted, open source web panel for running game servers, in the spirit of CubeCoders AMP.
The first games are **Minecraft (Java)** and **Project Zomboid**, on Debian Linux with Docker.

> Status: early. You can create Minecraft and Project Zomboid servers, install them, start and stop them, edit their settings, browse and edit their files, take and restore backups, use a live console, and connect bots through API tokens, a stats API, and Discord-ready webhooks.

## How it fits together

- **Panel** (`cmd/panel`): the web UI, the REST API, users, and state.
- **Agent** (`cmd/agent`): runs on each machine, dials out to the panel over WebSocket, and starts game servers as Docker containers.
- **Templates** (`templates/*.yaml`): one file per game says how to install, start, stop, configure, and read stats from it. Adding a game means adding a template, not code.
- **Integrations**: everything goes through the public API (`docs/api.md`), so a Discord bot or stats site uses the same door as the web UI.

## Run it

On a Debian machine with Docker installed:

```sh
sudo mkdir -p /srv/watchtower   # game server files live here
cp .env.example .env            # set a password and an agent token
docker compose up -d
```

Then open http://localhost:8080, click **New server**, pick a game, and press **Install**, then **Start**.
Game servers run as their own containers named `watchtower-<id>` and keep running if the agent restarts.
Server files live in `/srv/watchtower/instances/` and backups in `/srv/watchtower/backups/`; keep an eye on disk space.
Remember to open the game ports (for example 25565/tcp for Minecraft, 16261-16262/udp for Project Zomboid) on your router or firewall.

## Develop

```sh
go test ./...
PANEL_ADMIN_PASSWORD=dev PANEL_AGENT_TOKEN=dev go run ./cmd/panel
AGENT_TOKEN=dev AGENT_DATA_DIR=$PWD/data/agent go run ./cmd/agent
cd web && npm install && npm run dev   # UI on :5173, proxies /api to :8080
```

## Roadmap

1. Skeleton: panel and agent connected, login, Docker Compose *(done)*
2. First games end to end: install, start, stop, live console *(done)*
3. Daily use: config editor, file manager, backups, stats API with tokens and webhooks *(done)*
4. Users: roles, per-instance access, audit log
5. Scale out: remote agents, scheduler, auto-updates, Discord bot

See [CONTRIBUTING.md](CONTRIBUTING.md) to help. Licensed under MIT.
