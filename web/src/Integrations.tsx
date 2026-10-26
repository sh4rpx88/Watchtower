import { FormEvent, useCallback, useEffect, useState } from "react";
import { APIToken, Webhook, api } from "./api";

const eventLabels: Record<string, string> = {
  "player.join": "Player joins",
  "player.leave": "Player leaves",
  "server.ready": "Server ready",
  "server.stopped": "Server stopped",
  "server.crashed": "Server crashed",
  "backup.done": "Backup saved",
  "backup.failed": "Backup failed",
};

// Integrations manages API tokens for bots and webhooks that announce events.
export default function Integrations({ token }: { token: string }) {
  return (
    <section className="card">
      <h2>Integrations</h2>
      <Tokens token={token} />
      <Webhooks token={token} />
    </section>
  );
}

function Tokens({ token }: { token: string }) {
  const [tokens, setTokens] = useState<APIToken[]>([]);
  const [name, setName] = useState("");
  const [scope, setScope] = useState<"read" | "control">("read");
  const [secret, setSecret] = useState("");
  const [error, setError] = useState("");

  const load = useCallback(() => api<APIToken[]>(token, "GET", "/tokens").then(setTokens, (e) => setError(e.message)), [token]);
  useEffect(() => {
    load();
  }, [load]);

  async function create(e: FormEvent) {
    e.preventDefault();
    setError("");
    try {
      const res = await api<{ secret: string }>(token, "POST", "/tokens", { name, scope });
      setSecret(res.secret);
      setName("");
      load();
    } catch (err) {
      setError((err as Error).message);
    }
  }

  return (
    <div className="integration">
      <h3>API tokens</h3>
      <p className="muted">
        For bots and scripts. <strong>Read</strong> sees servers and stats. <strong>Control</strong> can also start, stop, restart, and send
        console commands. See docs/api.md for the endpoints.
      </p>
      {secret && (
        <p className="secret">
          Copy this token now; it won't be shown again: <code>{secret}</code>{" "}
          <button onClick={() => navigator.clipboard?.writeText(secret)}>Copy</button>
        </p>
      )}
      <table>
        <tbody>
          {tokens.map((t) => (
            <tr key={t.id}>
              <td>{t.name}</td>
              <td className="muted">{t.scope}</td>
              <td className="muted">
                <code>{t.prefix}…</code>
              </td>
              <td className="muted">{t.lastUsed ? `used ${new Date(t.lastUsed).toLocaleString()}` : "never used"}</td>
              <td className="actions">
                <button
                  className="danger"
                  onClick={() => {
                    if (confirm(`Revoke ${t.name}? Anything using it stops working.`)) {
                      api(token, "DELETE", `/tokens/${t.id}`).then(load, (e) => setError(e.message));
                    }
                  }}
                >
                  Revoke
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <form className="inline" onSubmit={create}>
        <input placeholder="Token name, e.g. Discord bot" value={name} onChange={(e) => setName(e.target.value)} required />
        <select value={scope} onChange={(e) => setScope(e.target.value as "read" | "control")}>
          <option value="read">Read</option>
          <option value="control">Control</option>
        </select>
        <button type="submit">Create token</button>
      </form>
      {error && <p className="error">{error}</p>}
    </div>
  );
}

function Webhooks({ token }: { token: string }) {
  const [hooks, setHooks] = useState<Webhook[]>([]);
  const [name, setName] = useState("");
  const [url, setUrl] = useState("");
  const [format, setFormat] = useState<"discord" | "json">("discord");
  const [events, setEvents] = useState<string[]>(Object.keys(eventLabels));
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");

  const load = useCallback(() => api<Webhook[]>(token, "GET", "/webhooks").then(setHooks, (e) => setError(e.message)), [token]);
  useEffect(() => {
    load();
  }, [load]);

  async function run(fn: () => Promise<unknown>, done = "") {
    setError("");
    setMessage("");
    try {
      await fn();
      setMessage(done);
      load();
    } catch (err) {
      setError((err as Error).message);
    }
  }

  return (
    <div className="integration">
      <h3>Webhooks</h3>
      <p className="muted">
        Post events to a URL. For Discord, open a channel's settings, then Integrations, Webhooks, New Webhook, and paste its URL here with
        the Discord format.
      </p>
      <table>
        <tbody>
          {hooks.map((h) => (
            <tr key={h.id}>
              <td>{h.name}</td>
              <td className="muted">{h.format}</td>
              <td className="muted">{h.events.map((e) => eventLabels[e] ?? e).join(", ")}</td>
              <td className="actions">
                <button onClick={() => run(() => api(token, "POST", `/webhooks/${h.id}/test`), `Sent a test message to ${h.name}.`)}>
                  Test
                </button>
                <button
                  className="danger"
                  onClick={() => {
                    if (confirm(`Delete webhook ${h.name}?`)) run(() => api(token, "DELETE", `/webhooks/${h.id}`));
                  }}
                >
                  Delete
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <form
        className="create"
        onSubmit={(e) => {
          e.preventDefault();
          run(async () => {
            await api(token, "POST", "/webhooks", { name, url, format, events });
            setName("");
            setUrl("");
          }, "Webhook added. Press Test to check it.");
        }}
      >
        <input placeholder="Name, e.g. #server-status" value={name} onChange={(e) => setName(e.target.value)} required />
        <input placeholder="https://discord.com/api/webhooks/..." value={url} onChange={(e) => setUrl(e.target.value)} required />
        <select value={format} onChange={(e) => setFormat(e.target.value as "discord" | "json")}>
          <option value="discord">Discord message</option>
          <option value="json">JSON (for your own app)</option>
        </select>
        <div className="checks">
          {Object.entries(eventLabels).map(([key, label]) => (
            <label key={key} className="check">
              <input
                type="checkbox"
                checked={events.includes(key)}
                onChange={(e) => setEvents(e.target.checked ? [...events, key] : events.filter((x) => x !== key))}
              />
              {label}
            </label>
          ))}
        </div>
        <button type="submit">Add webhook</button>
      </form>
      {message && <p className="muted">{message}</p>}
      {error && <p className="error">{error}</p>}
    </div>
  );
}
