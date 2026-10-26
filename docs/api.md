# Panel API

Base path: `/api/v1`. All endpoints except `health` and `login` need `Authorization: Bearer <token>`, either a login
session token or an API token (see below),
(or `?access_token=<token>` for the console WebSocket, since browsers can't set headers there).

| Method | Path | What it does |
| --- | --- | --- |
| GET | `/health` | Panel status and version |
| POST | `/login` | `{"username": "...", "password": "..."}` returns `{"token": "..."}`; sessions last 24 hours |
| POST | `/logout` | End this session |
| GET | `/me` | `{"name", "role"}` for whoever is calling (role is `token` for API tokens) |
| PUT | `/me/password` | `{"current", "new"}`; logs out your other sessions |
| GET | `/agents` | Connected machines |
| GET | `/templates` | Available games |
| GET | `/instances` | All servers, with state and whether their machine is online |
| POST | `/instances` | Create: `{"name", "templateId", "agent", "settings": {...}, "ports": {"game": 25566}}` |
| GET | `/instances/{id}` | One server |
| PATCH | `/instances/{id}` | `{"name", "connectAddress"}`; either field can be left out. `connectAddress` overrides the address shown to players |
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
| GET | `/instances/{id}/schedules` | The server's schedules, each with `nextRun` |
| POST | `/instances/{id}/schedules` | Add one (see Schedules) |
| PUT | `/instances/{id}/schedules/{scheduleId}` | Replace one |
| DELETE | `/instances/{id}/schedules/{scheduleId}` | Remove one |
| POST | `/instances/{id}/schedules/{scheduleId}/run` | Run it now |
| GET | `/settings` | `{"settings": {"publicAddress", "lanAddress"}, "timeZone"}` |
| PUT | `/settings` | `{"publicAddress", "lanAddress"}` (admin only) |

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
`cpuPercent` is per core, as Docker reports it, so a server using two full cores shows 200.

## Users and roles

Each person logs in with their own username. The first admin, `admin`, is created from `PANEL_ADMIN_PASSWORD` when
there are no users yet; after that, change passwords in the UI (changing the variable does nothing).

| Who | Can use |
| --- | --- |
| `viewer` user, `read` token | servers, templates, machines, stats, activity |
| `control` token | read, plus install, start, stop, restart, console commands, and the console stream |
| `operator` user | control, plus settings, files and backups |
| `admin` user | everything: creating and deleting servers, users, tokens and webhooks |

Admins manage users with `GET /users`, `POST /users {"username", "password", "role"}`,
`PATCH /users/{id} {"role"?, "password"?}` and `DELETE /users/{id}`. Passwords need 8 characters. The last admin can't
be demoted or removed, and nobody can remove themselves. Role changes apply on the next request; password resets and
removals log the user out.

## Activity log

`GET /activity` returns the newest entries first. Options: `limit` (default 50, up to 1000), `instance=<id>`, and
`kind=action` (what people did) or `kind=game` (players and servers). The panel keeps the last 1,000 in
`activity.jsonl` in its data directory.

Game events look like webhook events. Actions add who and what, with `instance` left out when there isn't one:

```json
{"type": "action", "time": "…", "instance": {"id": "…", "name": "Survival", "game": "…"},
 "actor": "sh4rpx", "message": "started the server", "playerCount": 0}
```

Logged actions: logging in, password changes, user changes, creating and deleting servers, install, start, stop,
restart, console commands, settings, file saves and deletes, new folders, backups, tokens and webhooks.

`GET /agents` reports each machine's `numCpu`, and on Linux also `memoryTotal`, `diskTotal` and `diskFree` in bytes
(disk is for the filesystem holding the agent's data directory).

## Schedules

```json
{"name": "Nightly restart", "action": "restart", "cron": "0 4 * * *", "warn": true, "enabled": true}
```

`action` is `restart`, `stop`, `start`, `backup` or `command`. `cron` is the usual five fields (minute, hour, day of
month, month, day of week) in the panel's time zone, set with `TZ` in `.env`. `warn` tells players in game 5 minutes,
1 minute and 10 seconds before a restart or stop, using the template's `say` command. A backup schedule can set `keep`
to delete all but the newest N backups, and a command schedule sets `command`. A scheduled restart skips a server
that isn't running. Each run is written to the activity log.

## API tokens

Create tokens on the dashboard under **Integrations**, or with `POST /tokens {"name", "scope"}` (admin only; the secret
is in the response once). Send them as `Authorization: Bearer wt_…`.

Scopes are `read` and `control`; see the table under Users and roles.

`GET /tokens`, `POST /tokens`, `DELETE /tokens/{id}` manage them.

## Webhooks

`POST /webhooks {"name", "url", "format": "discord" | "json", "events": [...]}` (admin only). Events:
`player.join`, `player.leave`, `server.ready`, `server.stopped`, `server.crashed`, `backup.done`, `backup.failed`.

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
