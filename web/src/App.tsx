import { FormEvent, useCallback, useEffect, useRef, useState } from "react";
import { Agent, ApiError, Instance, Template, api, consoleURL, login } from "./api";

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
  const [selected, setSelected] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  const load = useCallback(async () => {
    try {
      const [a, t, i] = await Promise.all([
        api<Agent[]>(token, "GET", "/agents"),
        api<Template[]>(token, "GET", "/templates"),
        api<Instance[]>(token, "GET", "/instances"),
      ]);
      setAgents(a);
      setTemplates(t);
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
      {tpl?.config.map((f) =>
        f.type === "bool" ? (
          <label key={f.key} className="check">
            <input
              type="checkbox"
              checked={(settings[f.key] ?? f.default) === "true"}
              onChange={(e) => setSettings({ ...settings, [f.key]: String(e.target.checked) })}
            />
            {f.label}
          </label>
        ) : (
          <label key={f.key}>
            {f.label}
            {f.required && " *"}
            <input
              type={f.type === "int" ? "number" : "text"}
              value={settings[f.key] ?? f.default}
              onChange={(e) => setSettings({ ...settings, [f.key]: e.target.value })}
            />
          </label>
        ),
      )}
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

function ServerView({
  token,
  instance,
  gameName,
  onBack,
  onChanged,
}: {
  token: string;
  instance: Instance;
  gameName: string;
  onBack: () => void;
  onChanged: () => void;
}) {
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const s = instance.state;

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
    if (!confirm(`Delete ${instance.name} and all of its files? This cannot be undone.`)) return;
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
        <button disabled={busy || canStop || s === "installing"} className="danger" onClick={remove}>
          Delete
        </button>
      </div>
      {error && <p className="error">{error}</p>}
      <Console token={token} id={instance.id} running={s === "running"} onSend={(line) => act("/command", "POST", { line })} />
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
