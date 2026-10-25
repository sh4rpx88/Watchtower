# Panel API

Base path: `/api/v1`. All endpoints except `health` and `login` need `Authorization: Bearer <token>`, either a login
session token or an API token (see below),
(or `?access_token=<token>` for the console WebSocket, since browsers can't set headers there).

| Method | Path | What it does |
| --- | --- | --- |
| GET | `/health` | Panel status and version |
| POST | `/login` | `{"password": "..."}` returns `{"token": "..."}` |
| GET | `/agents` | Connected machines |
| GET | `/templates` | Available games |
| GET | `/instances` | All servers, with state and whether their machine is online |
| POST | `/instances` | Create: `{"name", "templateId", "agent", "settings": {...}, "ports": {"game": 25566}}` |
| GET | `/instances/{id}` | One server |
| PUT | `/instances/{id}/settings` | Change settings, e.g. `{"motd": "Hi"}`; applies on next start |
| POST | `/instances/{id}/install` | Install or update the game files |
| POST | `/instances/{id}/start` | Start |
| POST | `/instances/{id}/stop` | Send the game's stop command; killed after 60 s |
| POST | `/instances/{id}/restart` | Stop, then start again once stopped |
| POST | `/instances/{id}/command` | `{"line": "say hi"}` to the server console |
| DELETE | `/instances/{id}` | Delete a stopped server and its files |
| GET (WebSocket) | `/instances/{id}/console` | Last 500 console lines, then new lines as JSON strings |
| GET | `/instances/{id}/files?path=/world` | List a folder |
| GET | `/instances/{id}/files/content?path=...` | A file's bytes (up to 2 MB); add `&download=1` to save it |
| PUT | `/instances/{id}/files/content?path=...` | Write the request body to the file (up to 2 MB) |
| POST | `/instances/{id}/files/mkdir?path=...` | Create a folder |
| DELETE | `/instances/{id}/files?path=...` | Delete a file or folder |
| GET | `/instances/{id}/backups` | Backups, newest first |
| POST | `/instances/{id}/backups` | Start a backup; a running server saves its world first |
| POST | `/instances/{id}/backups/{name}/restore` | Replace the server's files with a backup (server must be stopped) |
| DELETE | `/instances/{id}/backups/{name}` | Delete a backup |

Server states: `not_installed`, `installing`, `stopped`, `starting`, `running`, `stopping`, `error`.

## Stats

| Method | Path | What it does |
| --- | --- | --- |
| GET | `/stats` | Stats for every server |
| GET | `/instances/{id}/stats` | Stats for one server |

```json
{"id": "…", "name": "Survival", "game": "Minecraft (Java Edition)", "state": "running", "ready": true,
 "players": ["Alex", "Steve"], "playerCount": 2, "maxPlayers": 20, "uptimeSeconds": 5400,
 "cpuPercent": 12.5, "memoryBytes": 1610612736}
```

Players are followed from console output using each template's `stats` patterns, so the list starts empty if the panel
restarts while a server is running and fills in as players join. CPU and memory come from `docker stats` every 10 seconds.

## API tokens

Create tokens on the dashboard under **Integrations**, or with `POST /tokens {"name", "scope"}` (admin only; the secret
is in the response once). Send them as `Authorization: Bearer wt_…`.

| Scope | Can use |
| --- | --- |
| `read` | `GET` agents, templates, instances, stats |
| `control` | read, plus install, start, stop, restart, console commands, and the console stream |
| admin (logged-in session) | everything, including settings, files, backups, tokens, and webhooks |

`GET /tokens`, `POST /tokens`, `DELETE /tokens/{id}` manage them.

## Webhooks

`POST /webhooks {"name", "url", "format": "discord" | "json", "events": [...]}` (admin only). Events:
`player.join`, `player.leave`, `server.ready`, `server.stopped`, `server.crashed`.

- `discord` posts `{"content": "**Survival**: Steve joined (3/20 online)"}`, so a Discord channel webhook URL works as is.
- `json` posts the event:

```json
{"type": "player.join", "time": "2026-10-25T07:00:00Z", "instance": {"id": "…", "name": "Survival", "game": "Minecraft (Java Edition)"},
 "player": "Steve", "playerCount": 3, "maxPlayers": 20}
```

`GET /webhooks`, `DELETE /webhooks/{id}`, and `POST /webhooks/{id}/test` (sends a sample message) manage them.

## Agent protocol

Agents connect to `/agent/connect` with `Authorization: Bearer <agent token>` and exchange JSON envelopes
(`internal/protocol`): `hello` then `welcome`, then periodic `heartbeat`. The panel sends requests
(`instance.install`, `instance.start`, `instance.stop`, `instance.command`, `instance.delete`), each answered
by a `response` with the same `id`. The agent pushes `instance.status` and `console.line` events.
