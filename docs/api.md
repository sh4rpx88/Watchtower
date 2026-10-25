# Panel API

Base path: `/api/v1`. All endpoints except `health` and `login` need `Authorization: Bearer <token>`.

| Method | Path | What it does |
| --- | --- | --- |
| GET | `/health` | Panel status and version |
| POST | `/login` | `{"password": "..."}` returns `{"token": "..."}` |
| GET | `/agents` | Connected machines |
| GET | `/templates` | Available games |

## Planned for integrations

- Scoped API tokens (read-only or control, per instance) for bots and scripts.
- `GET /instances/{id}/stats`: status, players online, player list, uptime, CPU, RAM.
- Events over WebSocket and outgoing webhooks: player join and leave, server start, stop, crash, backup done.

## Agent protocol

Agents connect to `/agent/connect` with `Authorization: Bearer <agent token>` and exchange JSON envelopes
(`internal/protocol`): `hello` then `welcome`, then periodic `heartbeat`.
