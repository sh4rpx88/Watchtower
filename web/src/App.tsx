import { FormEvent, useCallback, useEffect, useRef, useState } from "react";
import { Agent, ApiError, ConfigField, Instance, Stats, Template, api, consoleURL, formatDuration, formatSize, login } from "./api";
import Backups from "./Backups";
import Files from "./Files";
import Integrations from "./Integrations";

export default function App() {
  const [token, setToken] = useState(() => sessionStorage.getItem("token") ?? "");
  const logout = useCallback(() => {
    sessionStorage.removeItem("token");
    setToken("");
  }, []);
  if (!token) {
    return (
      <Login
        onLogin={(t) => {
          sessionStorage.setItem("token", t);
          setToken(t);
        }}
      />
    );
  }
  return <Dashboard token={token} onLogout={logout} />;
}

function Login({ onLogin }: { onLogin: (token: string) => void }) {
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  async function submit(e: FormEvent) {
    e.preventDefault();
    try {
      onLogin(await login(password));
    } catch (err) {
      setError((err as Error).message);
    }
  }
  return (
    <form className="card login" onSubmit={submit}>
      <h1>Watchtower</h1>
      <input type="password" placeholder="Admin password" value={password} onChange={(e) => setPassword(e.target.value)} />
      <button type="submit">Log in</button>
      {error && <p className="error">{error}</p>}
    </form>
  );
}

const stateLabels: Record<string, string> = {
  not_installed: "Not installed",
  installing: "Installing…",
  stopped: "Stopped",
  starting: "Starting…",
  running: "Running",
  stopping: "Stopping…",
  error: "Error",
};

function Dashboard({ token, onLogout }: { token: string; onLogout: () => void }) {
  const [agents, setAgents] = useState<Agent[]>([]);
  const [templates, setTemplates] = useState<Template[]>([]);
  const [instances, setInstances] = useState<Instance[]>([]);
  const [stats, setStats] = useState<Record<string, Stats>>({});
  const [selected, setSelected] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  const load = useCallback(async () => {
    try {
      const [a, t, i, st] = await Promise.all([
        api<Agent[]>(token, "GET", "/agents"),
        api<Template[]>(token, "GET", "/templates"),
        api<Instance[]>(token, "GET", "/instances"),
        api<Stats[]>(token, "GET", "/stats"),
      ]);
      setAgents(a);
      setTemplates(t);
      setStats(Object.fromEntries(st.map((x) => [x.id, x])));
      setInstances(i.sort((x, y) => x.name.localeCompare(y.name)));
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) onLogout();
    }
  }, [token, onLogout]);

  useEffect(() => {
    load();
    const id = setInterval(load, 3000);
    return () => clearInterval(id);
  }, [load]);

  const current = instances.find((i) => i.id === selected);
  const gameName = (id: string) => templates.find((t) => t.id === id)?.name ?? id;

  return (
    <main>
      <header>
        <h1>Watchtower</h1>
        <button onClick={onLogout}>Log out</button>
      </header>

      {current ? (
        <ServerView
          token={token}
          instance={current}
          stats={stats[current.id]}
          template={templates.find((t) => t.id === current.templateId)}
          gameName={gameName(current.templateId)}
          onBack={() => setSelected(null)}
          onChanged={load}
        />
      ) : (
        <>
          <section className="card">
            <div className="row">
              <h2>Servers</h2>
              <button onClick={() => setCreating(!creating)}>{creating ? "Cancel" : "New server"}</button>
            </div>
            {creating && (
              <CreateServer
                token={token}
                agents={agents}
                templates={templates}
                onCreated={(id) => {
                  setCreating(false);
                  load();
                  setSelected(id);
                }}
              />
            )}
            {instances.length === 0 && !creating ? (
              <p>No servers yet.</p>
            ) : (
              <ul className="servers">
                {instances.map((i) => (
                  <li key={i.id}>
                    <button className="link" onClick={() => setSelected(i.id)}>
                      <strong>{i.name}</strong>
                    </button>{" "}
                    {gameName(i.templateId)} on {i.agent} · <span className={`state ${i.state}`}>{stateLabels[i.state] ?? i.state}</span>
                    {i.state === "running" && stats[i.id] && (
                      <span className="muted">
                        {" "}
                        · {stats[i.id].playerCount}
                        {stats[i.id].maxPlayers ? `/${stats[i.id].maxPlayers}` : ""} players
                      </span>
                    )}
                    {!i.agentOnline && <span className="muted"> (machine offline)</span>}
                  </li>
                ))}
              </ul>
            )}
          </section>
          <section className="card">
            <h2>Machines</h2>
            {agents.length === 0 ? (
              <p>No agents connected.</p>
            ) : (
              <ul>
                {agents.map((a) => (
                  <li key={a.name}>
                    <strong>{a.name}</strong> {a.hostname} · {a.os}/{a.arch} · {a.numCpu} CPUs
                  </li>
                ))}
              </ul>
            )}
          </section>
          <Integrations token={token} />
        </>
      )}
    </main>
  );
}

function CreateServer({
  token,
  agents,
  templates,
  onCreated,
}: {
  token: string;
  agents: Agent[];
  templates: Template[];
  onCreated: (id: string) => void;
}) {
  const [templateId, setTemplateId] = useState(templates[0]?.id ?? "");
  const [name, setName] = useState("");
  const [agent, setAgent] = useState(agents[0]?.name ?? "");
  const [settings, setSettings] = useState<Record<string, string>>({});
  const [ports, setPorts] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const tpl = templates.find((t) => t.id === templateId);

  useEffect(() => {
    if (!agent && agents[0]) setAgent(agents[0].name);
  }, [agents, agent]);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    const portNumbers: Record<string, number> = {};
    for (const [k, v] of Object.entries(ports)) if (v) portNumbers[k] = Number(v);
    try {
      const created = await api<Instance>(token, "POST", "/instances", {
        name,
        templateId,
        agent,
        settings,
        ports: portNumbers,
      });
      onCreated(created.id);
    } catch (err) {
      setError((err as Error).message);
    }
  }

  if (agents.length === 0) return <p className="error">Connect an agent before creating a server.</p>;
  return (
    <form className="create" onSubmit={submit}>
      <label>
        Game
        <select
          value={templateId}
          onChange={(e) => {
            setTemplateId(e.target.value);
            setSettings({});
            setPorts({});
          }}
        >
          {templates.map((t) => (
            <option key={t.id} value={t.id}>
              {t.name}
            </option>
          ))}
        </select>
      </label>
      <label>
        Name
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder="My server" required />
      </label>
      <label>
        Machine
        <select value={agent} onChange={(e) => setAgent(e.target.value)}>
          {agents.map((a) => (
            <option key={a.name}>{a.name}</option>
          ))}
        </select>
      </label>
      {tpl && <SettingsFields fields={tpl.config} values={settings} onChange={setSettings} />}
      {tpl?.ports.map((p) => (
        <label key={p.name}>
          Port: {p.name} ({p.protocol})
          <input
            type="number"
            placeholder={String(p.port)}
            value={ports[p.name] ?? ""}
            onChange={(e) => setPorts({ ...ports, [p.name]: e.target.value })}
          />
        </label>
      ))}
      <button type="submit">Create</button>
      {error && <p className="error">{error}</p>}
    </form>
  );
}

function SettingsFields({
  fields,
  values,
  onChange,
}: {
  fields: ConfigField[];
  values: Record<string, string>;
  onChange: (values: Record<string, string>) => void;
}) {
  const set = (key: string, value: string) => onChange({ ...values, [key]: value });
  return (
    <>
      {fields.map((f) => {
        const value = values[f.key] ?? f.default;
        if (f.type === "bool") {
          return (
            <label key={f.key} className="check">
              <input type="checkbox" checked={value === "true"} onChange={(e) => set(f.key, String(e.target.checked))} />
              {f.label}
            </label>
          );
        }
        return (
          <label key={f.key}>
            {f.label}
            {f.required && " *"}
            {f.type === "select" && f.options ? (
              <select value={value} onChange={(e) => set(f.key, e.target.value)}>
                {f.options.map((o) => (
                  <option key={o}>{o}</option>
                ))}
              </select>
            ) : (
              <input type={f.type === "int" ? "number" : "text"} value={value} onChange={(e) => set(f.key, e.target.value)} />
            )}
          </label>
        );
      })}
    </>
  );
}

function ServerSettings({
  token,
  instance,
  template,
  onSaved,
}: {
  token: string;
  instance: Instance;
  template: Template;
  onSaved: () => void;
}) {
  const [values, setValues] = useState(instance.settings);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");

  async function save(e: FormEvent) {
    e.preventDefault();
    setError("");
    setMessage("");
    try {
      await api(token, "PUT", `/instances/${instance.id}/settings`, values);
      setMessage(instance.state === "running" ? "Saved. Restart the server to apply." : "Saved. Applies on next start.");
      onSaved();
    } catch (err) {
      setError((err as Error).message);
    }
  }

  return (
    <form className="create" onSubmit={save}>
      <SettingsFields fields={template.config} values={values} onChange={setValues} />
      <button type="submit">Save settings</button>
      {message && <p className="muted">{message}</p>}
      {error && <p className="error">{error}</p>}
    </form>
  );
}

function ServerView({
  token,
  instance,
  stats,
  template,
  gameName,
  onBack,
  onChanged,
}: {
  token: string;
  instance: Instance;
  stats?: Stats;
  template?: Template;
  gameName: string;
  onBack: () => void;
  onChanged: () => void;
}) {
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [tab, setTab] = useState<"console" | "settings" | "files" | "backups">("console");
  const s = instance.state;
  const installed = s !== "not_installed" && s !== "installing";

  async function act(path: string, method = "POST", body?: unknown) {
    setError("");
    setBusy(true);
    try {
      await api(token, method, `/instances/${instance.id}${path}`, body);
      onChanged();
      return true;
    } catch (err) {
      setError((err as Error).message);
      return false;
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    if (!confirm(`Delete ${instance.name}, all of its files, and all of its backups? This cannot be undone.`)) return;
    if (await act("", "DELETE")) onBack();
  }

  const canInstall = s === "not_installed" || s === "stopped" || s === "error";
  const canStart = s === "stopped" || s === "error";
  const canStop = s === "running" || s === "starting";

  return (
    <section className="card">
      <button className="link" onClick={onBack}>
        ← All servers
      </button>
      <div className="row">
        <h2>{instance.name}</h2>
        <span className={`state ${s}`}>{stateLabels[s] ?? s}</span>
      </div>
      <p className="muted">
        {gameName} on {instance.agent} · ports{" "}
        {Object.entries(instance.ports)
          .map(([k, v]) => `${k} ${v}`)
          .join(", ")}
        {!instance.agentOnline && " · machine offline"}
      </p>
      {stats && s === "running" && (
        <p className="stats">
          {stats.ready ? "Ready" : "Starting up"} · up {formatDuration(stats.uptimeSeconds ?? 0)} · CPU {stats.cpuPercent.toFixed(0)}% · RAM{" "}
          {formatSize(stats.memoryBytes)} · {stats.playerCount}
          {stats.maxPlayers ? `/${stats.maxPlayers}` : ""} players
          {stats.players.length > 0 && `: ${stats.players.join(", ")}`}
        </p>
      )}
      {instance.error && <p className="error">{instance.error}</p>}
      <div className="actions">
        <button disabled={busy || !canInstall} onClick={() => act("/install")}>
          {s === "not_installed" ? "Install" : "Update"}
        </button>
        <button disabled={busy || !canStart} onClick={() => act("/start")}>
          Start
        </button>
        <button disabled={busy || !canStop} onClick={() => act("/stop")}>
          Stop
        </button>
        <button disabled={busy || s !== "running"} onClick={() => act("/restart")}>
          Restart
        </button>
        <button disabled={busy || canStop || s === "installing"} className="danger" onClick={remove}>
          Delete
        </button>
      </div>
      {error && <p className="error">{error}</p>}
      <nav className="tabs">
        {(["console", "settings", "files", "backups"] as const).map((t) => (
          <button key={t} className={tab === t ? "active" : ""} onClick={() => setTab(t)}>
            {t[0].toUpperCase() + t.slice(1)}
          </button>
        ))}
      </nav>
      {tab === "console" && (
        <Console token={token} id={instance.id} running={s === "running"} onSend={(line) => act("/command", "POST", { line })} />
      )}
      {tab === "settings" && template && (
        <ServerSettings key={instance.id} token={token} instance={instance} template={template} onSaved={onChanged} />
      )}
      {tab === "files" &&
        (installed && instance.agentOnline ? <Files token={token} instance={instance} template={template} /> : <p className="muted">Install the server and keep its machine online to manage files.</p>)}
      {tab === "backups" &&
        (installed && instance.agentOnline ? <Backups token={token} instance={instance} /> : <p className="muted">Install the server and keep its machine online to manage backups.</p>)}
    </section>
  );
}

function Console({
  token,
  id,
  running,
  onSend,
}: {
  token: string;
  id: string;
  running: boolean;
  onSend: (line: string) => Promise<boolean>;
}) {
  const [lines, setLines] = useState<string[]>([]);
  const [input, setInput] = useState("");
  const box = useRef<HTMLPreElement>(null);

  useEffect(() => {
    setLines([]);
    let closed = false;
    let ws: WebSocket;
    let retry: number;
    const connect = () => {
      ws = new WebSocket(consoleURL(token, id));
      ws.onmessage = (e) => setLines((prev) => [...prev.slice(-999), JSON.parse(e.data)]);
      ws.onclose = () => {
        if (!closed) retry = window.setTimeout(connect, 2000);
      };
      // Reconnects replay the backlog, so start from empty.
      ws.onopen = () => setLines([]);
    };
    connect();
    return () => {
      closed = true;
      clearTimeout(retry);
      ws.close();
    };
  }, [token, id]);

  useEffect(() => {
    const el = box.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [lines]);

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (input.trim() && (await onSend(input))) setInput("");
  }

  return (
    <div className="console">
      <pre ref={box}>{lines.join("\n") || "No output yet."}</pre>
      <form onSubmit={submit}>
        <input
          value={input}
          onChange={(e) => setInput(e.target.value)}
          placeholder={running ? "Type a server command" : "Start the server to send commands"}
          disabled={!running}
        />
        <button type="submit" disabled={!running}>
          Send
        </button>
      </form>
    </div>
  );
}
