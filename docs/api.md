# Panel API

Base path: `/api/v1`. All endpoints except `health` and `login` need `Authorization: Bearer <token>`
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
| POST | `/instances/{id}/command` | `{"line": "say hi"}` to the server console |
| DELETE | `/instances/{id}` | Delete a stopped server and its files |
| GET (WebSocket) | `/instances/{id}/console` | Last 500 console lines, then new lines as JSON strings |

Server states: `not_installed`, `installing`, `stopped`, `starting`, `running`, `stopping`, `error`.

## Planned for integrations

- Scoped API tokens (read-only or control, per instance) for bots and scripts.
- `GET /instances/{id}/stats`: status, players online, player list, uptime, CPU, RAM.
- Events over WebSocket and outgoing webhooks: player join and leave, server start, stop, crash, backup done.

## Agent protocol

Agents connect to `/agent/connect` with `Authorization: Bearer <agent token>` and exchange JSON envelopes
(`internal/protocol`): `hello` then `welcome`, then periodic `heartbeat`. The panel sends requests
(`instance.install`, `instance.start`, `instance.stop`, `instance.command`, `instance.delete`), each answered
by a `response` with the same `id`. The agent pushes `instance.status` and `console.line` events.
