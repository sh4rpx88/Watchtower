import { FormEvent, useCallback, useEffect, useState } from "react";
import { APIToken, DiscordView, Webhook, api } from "./api";

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
      <DiscordBot token={token} />
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

// Permissions the bot needs in its channel: view, send, embed links, attach
// files, read history.
const botPermissions = 1024 + 2048 + 16384 + 32768 + 65536;

function DiscordBot({ token }: { token: string }) {
  const [view, setView] = useState<DiscordView | null>(null);
  const [form, setForm] = useState({ enabled: false, token: "", channelId: "", controlRoleId: "", guildId: "" });
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");

  const load = useCallback(
    (fill: boolean) =>
      api<DiscordView>(token, "GET", "/discord").then(
        (v) => {
          setView(v);
          if (fill)
            setForm({
              enabled: v.settings.enabled,
              token: "",
              channelId: v.settings.channelId,
              controlRoleId: v.settings.controlRoleId,
              guildId: v.settings.guildId,
            });
        },
        (e) => setError(e.message),
      ),
    [token],
  );
  useEffect(() => {
    load(true);
    const timer = setInterval(() => load(false), 5000);
    return () => clearInterval(timer);
  }, [load]);

  async function run(fn: () => Promise<DiscordView>, done: string) {
    setError("");
    setMessage("");
    try {
      setView(await fn());
      setForm((f) => ({ ...f, token: "" }));
      setMessage(done);
    } catch (err) {
      setError((err as Error).message);
    }
  }

  if (!view) return null;
  const s = view.status;
  const state = !view.settings.enabled
    ? { label: "Off", cls: "off" }
    : s.connected
      ? { label: `Online as ${s.botName}`, cls: "ok" }
      : s.error
        ? { label: s.error, cls: "bad" }
        : { label: "Connecting…", cls: "wait" };
  const invite = s.applicationId
    ? `https://discord.com/oauth2/authorize?client_id=${s.applicationId}&scope=bot%20applications.commands&permissions=${botPermissions}`
    : "";

  return (
    <div className="integration discord">
      <div className="integration-head">
        <h3>Discord bot</h3>
        <span className={`pill ${state.cls}`}>{state.label}</span>
      </div>
      <p className="muted">
        A bot that keeps a live status board in a channel, styled like this panel, with a menu to open any server and buttons to start,
        stop, restart and back it up. It also adds <code>/status</code>, <code>/server</code> and <code>/players</code>. It connects out to
        Discord, so nothing needs to be opened on your router.
      </p>
      <details className="guide" open={!view.hasToken}>
        <summary>How to set it up</summary>
        <ol>
          <li>
            Open the{" "}
            <a href="https://discord.com/developers/applications" target="_blank" rel="noreferrer">
              Discord Developer Portal
            </a>
            , press <b>New Application</b> and name it Watchtower. You can use{" "}
            <a href="/icons/icon-512.png" download="watchtower.png">
              the Watchtower icon
            </a>{" "}
            as its picture.
          </li>
          <li>
            Under <b>Bot</b>, press <b>Reset Token</b>, copy the token and paste it below. Keep it secret; it's the bot's password.
          </li>
          <li>
            Save with the bot turned on. Once it says Online, press <b>Invite the bot</b> and pick your Discord server.
          </li>
          <li>
            In Discord, turn on <b>Developer Mode</b> (User Settings, Advanced). Right-click the channel for the status board and choose{" "}
            <b>Copy Channel ID</b>. For the role that may control servers, open Server Settings, Roles, right-click the role and choose{" "}
            <b>Copy Role ID</b>. Paste both below and save.
          </li>
        </ol>
        <p className="muted">
          Anyone in your Discord can see the board and open a server's details. Only people with the control role, or with Manage Server,
          can press Start, Stop, Restart and Backup. Stop and Restart ask first and list who is online. Everything they do shows in the
          Activity log with their Discord name.
        </p>
      </details>
      <form
        className="create"
        onSubmit={(e: FormEvent) => {
          e.preventDefault();
          run(() => api<DiscordView>(token, "PUT", "/discord", form), "Saved.");
        }}
      >
        <label className="check">
          <input type="checkbox" checked={form.enabled} onChange={(e) => setForm({ ...form, enabled: e.target.checked })} />
          Bot turned on
        </label>
        <label>
          Bot token
          <input
            type="password"
            autoComplete="off"
            value={form.token}
            onChange={(e) => setForm({ ...form, token: e.target.value })}
            placeholder={view.hasToken ? "Saved. Paste a new one to replace it." : "Paste the token from the Bot page"}
          />
        </label>
        <label>
          Status board channel ID
          <input
            value={form.channelId}
            onChange={(e) => setForm({ ...form, channelId: e.target.value })}
            placeholder="e.g. 1234567890123456789"
          />
        </label>
        <label>
          Control role ID (optional)
          <input
            value={form.controlRoleId}
            onChange={(e) => setForm({ ...form, controlRoleId: e.target.value })}
            placeholder="Without one, only people with Manage Server can control servers"
          />
        </label>
        <label>
          Discord server ID (optional)
          <input
            value={form.guildId}
            onChange={(e) => setForm({ ...form, guildId: e.target.value })}
            placeholder="Only needed if the bot is in more than one server"
          />
        </label>
        <div className="actions">
          <button type="submit" className="primary">
            Save
          </button>
          {invite && (
            <a className="button" href={invite} target="_blank" rel="noreferrer">
              Invite the bot
            </a>
          )}
          {s.connected && view.settings.channelId && (
            <button
              type="button"
              onClick={() => run(() => api<DiscordView>(token, "POST", "/discord/board"), "Posted a new status board.")}
            >
              Post a new status board
            </button>
          )}
        </div>
      </form>
      {message && <p className="muted">{message}</p>}
      {error && <p className="error">{error}</p>}
    </div>
  );
}
