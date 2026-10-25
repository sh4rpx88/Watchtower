import { FormEvent, ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Activity,
  AlertTriangle,
  Archive,
  Boxes,
  Cpu,
  Download,
  FolderOpen,
  HardDrive,
  LayoutDashboard,
  LogOut,
  MemoryStick,
  MoreVertical,
  Play,
  Plug,
  Plus,
  RotateCw,
  Search,
  Server as ServerIcon,
  Settings,
  Square,
  SquareTerminal,
  Trash2,
  UserMinus,
  UserPlus,
  Users,
} from "lucide-react";
import {
  ActivityEvent,
  Agent,
  ApiError,
  ConfigField,
  Instance,
  Stats,
  Template,
  api,
  consoleURL,
  formatDuration,
  formatSize,
  login,
  parseSize,
} from "./api";
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

function Banner() {
  return (
    <header className="banner">
      <img src="/art/banner.jpg" alt="Watchtower: servers, control, monitor, always online" />
    </header>
  );
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
    <>
      <Banner />
      <form className="card login" onSubmit={submit}>
        <h2>Enter the keep</h2>
        <input type="password" placeholder="Admin password" value={password} onChange={(e) => setPassword(e.target.value)} autoFocus />
        <button type="submit" className="primary">
          Log in
        </button>
        {error && <p className="error">{error}</p>}
      </form>
    </>
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

type Tab = "console" | "settings" | "files" | "backups";
type View = { page: "overview" | "servers" | "machines" | "integrations" } | { page: "server"; id: string; tab: Tab };

const tabs: { id: Tab; label: string; icon: ReactNode }[] = [
  { id: "console", label: "Console", icon: <SquareTerminal size={16} /> },
  { id: "settings", label: "Settings", icon: <Settings size={16} /> },
  { id: "files", label: "File Manager", icon: <FolderOpen size={16} /> },
  { id: "backups", label: "Backups", icon: <Archive size={16} /> },
];

const knownArt = new Set(["minecraft-java", "project-zomboid", "7-days-to-die", "ark"]);
const gameArt = (templateId: string) => (knownArt.has(templateId) ? `/art/${templateId}.jpg` : "/art/keep.jpg");

// Data is everything the dashboard polls for.
type Data = {
  agents: Agent[];
  templates: Template[];
  instances: Instance[];
  stats: Record<string, Stats>;
  activity: ActivityEvent[];
};

function Dashboard({ token, onLogout }: { token: string; onLogout: () => void }) {
  const [data, setData] = useState<Data>({ agents: [], templates: [], instances: [], stats: {}, activity: [] });
  const [view, setView] = useState<View>({ page: "overview" });
  const [creating, setCreating] = useState(false);

  const load = useCallback(async () => {
    try {
      const [agents, templates, instances, stats, activity] = await Promise.all([
        api<Agent[]>(token, "GET", "/agents"),
        api<Template[]>(token, "GET", "/templates"),
        api<Instance[]>(token, "GET", "/instances"),
        api<Stats[]>(token, "GET", "/stats"),
        api<ActivityEvent[]>(token, "GET", "/activity"),
      ]);
      setData({
        agents,
        templates,
        instances: instances.sort((x, y) => x.name.localeCompare(y.name)),
        stats: Object.fromEntries(stats.map((x) => [x.id, x])),
        activity,
      });
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) onLogout();
    }
  }, [token, onLogout]);

  useEffect(() => {
    load();
    const id = setInterval(load, 3000);
    return () => clearInterval(id);
  }, [load]);

  const { agents, templates, instances, stats, activity } = data;
  const current = view.page === "server" ? instances.find((i) => i.id === view.id) : undefined;
  const gameName = (id: string) => templates.find((t) => t.id === id)?.name ?? id;
  const openServer = (id: string, tab: Tab = "console") => setView({ page: "server", id, tab });
  const addServer = () => {
    setCreating(true);
    setView({ page: "servers" });
  };
  const top = view.page === "server" ? "servers" : view.page;

  const serversPanel = (
    <ServerList
      token={token}
      agents={agents}
      templates={templates}
      instances={instances}
      stats={stats}
      creating={creating}
      setCreating={setCreating}
      gameName={gameName}
      onOpen={openServer}
      onChanged={load}
    />
  );

  return (
    <div className="shell">
      <Banner />
      <nav className="topnav">
        {(
          [
            ["overview", "Dashboard", <LayoutDashboard size={18} key="i" />],
            ["servers", "Servers", <ServerIcon size={18} key="i" />],
            ["machines", "Machines", <Boxes size={18} key="i" />],
            ["integrations", "Integrations", <Plug size={18} key="i" />],
          ] as const
        ).map(([page, label, icon]) => (
          <button key={page} className={top === page ? "active" : ""} onClick={() => setView({ page })}>
            {icon}
            {label}
          </button>
        ))}
        <span className="spacer" />
        <span className="who">
          <Users size={18} /> admin
        </span>
        <button onClick={onLogout} title="Log out">
          <LogOut size={18} />
          <span className="hide-sm">Log out</span>
        </button>
      </nav>

      <div className="layout">
        <aside className="sidebar card">
          <h3>Server management</h3>
          <button className={view.page === "overview" ? "active" : ""} onClick={() => setView({ page: "overview" })}>
            <LayoutDashboard size={18} /> Overview
          </button>
          {instances.map((i) => (
            <div key={i.id}>
              <button className={current?.id === i.id ? "active" : ""} onClick={() => openServer(i.id)}>
                <span className={`dot ${i.state}`} /> {i.name}
              </button>
              {current?.id === i.id && view.page === "server" && (
                <div className="subnav">
                  {tabs.map((t) => (
                    <button key={t.id} className={view.tab === t.id ? "active" : ""} onClick={() => openServer(i.id, t.id)}>
                      {t.icon} {t.label}
                    </button>
                  ))}
                </div>
              )}
            </div>
          ))}
          <button onClick={addServer}>
            <Plus size={18} /> Add server
          </button>
          <div className="sidebar-art">
            <img src="/art/sidebar.jpg" alt="A stronger world together" />
          </div>
        </aside>

        <main>
          {view.page === "overview" && (
            <>
              <Totals agents={agents} instances={instances} stats={stats} />
              {serversPanel}
            </>
          )}
          {view.page === "servers" && serversPanel}
          {view.page === "machines" && <Machines agents={agents} instances={instances} />}
          {view.page === "integrations" && <Integrations token={token} />}
          {view.page === "server" &&
            (current ? (
              <ServerView
                token={token}
                instance={current}
                stats={stats[current.id]}
                agent={agents.find((a) => a.name === current.agent)}
                template={templates.find((t) => t.id === current.templateId)}
                gameName={gameName(current.templateId)}
                tab={view.tab}
                setTab={(tab) => openServer(current.id, tab)}
                onBack={() => setView({ page: "overview" })}
                onChanged={load}
              />
            ) : (
              <p className="muted">That server no longer exists.</p>
            ))}
        </main>

        {view.page === "overview" && (
          <aside className="rightcol">
            <RecentActivity activity={activity} onOpen={openServer} />
            <SystemStatus agents={agents} instances={instances} />
            <div className="keep-art card">
              <img src="/art/keep.jpg" alt="The keep never sleeps" />
            </div>
          </aside>
        )}
      </div>
    </div>
  );
}

function Meter({ value, max, color }: { value: number; max: number; color: string }) {
  const pct = max > 0 ? Math.min(100, (value / max) * 100) : 0;
  return (
    <div className="meter">
      <div style={{ width: `${pct}%`, background: `var(--${color})` }} />
    </div>
  );
}

function StatCard({ icon, value, unit, label, meter }: { icon: ReactNode; value: string; unit?: string; label: string; meter: ReactNode }) {
  return (
    <div className="card statcard">
      <div className="statcard-icon">{icon}</div>
      <div className="statcard-body">
        <div className="statcard-value">
          {value}
          {unit && <small>{unit}</small>}
        </div>
        <div className="statcard-label">{label}</div>
        {meter}
      </div>
    </div>
  );
}

function Totals({ agents, instances, stats }: { agents: Agent[]; instances: Instance[]; stats: Record<string, Stats> }) {
  const running = instances.filter((i) => i.state === "running");
  const live = running.map((i) => stats[i.id]).filter(Boolean);
  const players = live.reduce((n, s) => n + s.playerCount, 0);
  const maxPlayers = live.reduce((n, s) => n + (s.maxPlayers ?? 0), 0);
  const cores = agents.reduce((n, a) => n + a.numCpu, 0);
  const cpu = cores ? live.reduce((n, s) => n + s.cpuPercent, 0) / cores : 0;
  const ram = live.reduce((n, s) => n + s.memoryBytes, 0);
  const ramTotal = agents.reduce((n, a) => n + (a.memoryTotal ?? 0), 0);
  const [ramValue, ramUnit] = formatSize(ram).split(" ");
  return (
    <div className="totals">
      <StatCard
        icon={<ServerIcon size={34} />}
        value={`${running.length}`}
        unit={` / ${instances.length}`}
        label="Servers online"
        meter={<Meter value={running.length} max={instances.length} color="green" />}
      />
      <StatCard
        icon={<Users size={34} />}
        value={`${players}`}
        unit={maxPlayers ? ` / ${maxPlayers}` : undefined}
        label="Players online"
        meter={<Meter value={players} max={maxPlayers} color="blue" />}
      />
      <StatCard icon={<Cpu size={34} />} value={cpu.toFixed(0)} unit="%" label="CPU usage" meter={<Meter value={cpu} max={100} color="text" />} />
      <StatCard
        icon={<MemoryStick size={34} />}
        value={ramValue}
        unit={` ${ramUnit}`}
        label={ramTotal ? `RAM of ${formatSize(ramTotal)}` : "RAM usage"}
        meter={<Meter value={ram} max={ramTotal} color="orange" />}
      />
    </div>
  );
}

// memoryLimit is the memory a server is meant to use: its memory setting if
// the game has one, otherwise the whole machine.
function memoryLimit(instance: Instance, agent?: Agent) {
  return parseSize(instance.settings.memory) || agent?.memoryTotal || 0;
}

function ServerList({
  token,
  agents,
  templates,
  instances,
  stats,
  creating,
  setCreating,
  gameName,
  onOpen,
  onChanged,
}: {
  token: string;
  agents: Agent[];
  templates: Template[];
  instances: Instance[];
  stats: Record<string, Stats>;
  creating: boolean;
  setCreating: (v: boolean) => void;
  gameName: (id: string) => string;
  onOpen: (id: string, tab?: Tab) => void;
  onChanged: () => void;
}) {
  const [query, setQuery] = useState("");
  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    return instances.filter((i) => !q || i.name.toLowerCase().includes(q) || gameName(i.templateId).toLowerCase().includes(q));
  }, [instances, query, gameName]);

  return (
    <section className="card servers">
      <div className="panel-head">
        <h2>Servers</h2>
        <label className="search">
          <Search size={16} />
          <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search servers…" />
        </label>
        <button className="primary" onClick={() => setCreating(!creating)}>
          {creating ? (
            "Cancel"
          ) : (
            <>
              <Plus size={16} /> Add server
            </>
          )}
        </button>
      </div>
      {creating && (
        <CreateServer
          token={token}
          agents={agents}
          templates={templates}
          onCreated={(id) => {
            setCreating(false);
            onChanged();
            onOpen(id);
          }}
        />
      )}
      {instances.length === 0 && !creating && <p className="muted pad">No servers yet. Add one to get started.</p>}
      {shown.map((i) => (
        <ServerRow
          key={i.id}
          token={token}
          instance={i}
          stats={stats[i.id]}
          agent={agents.find((a) => a.name === i.agent)}
          gameName={gameName(i.templateId)}
          onOpen={onOpen}
          onChanged={onChanged}
        />
      ))}
    </section>
  );
}

function useActions(token: string, instance: Instance, onChanged: () => void) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const act = useCallback(
    async (path: string, method = "POST", body?: unknown) => {
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
    },
    [token, instance.id, onChanged],
  );
  const s = instance.state;
  return {
    act,
    busy,
    error,
    canInstall: s === "not_installed" || s === "stopped" || s === "error",
    canStart: s === "stopped" || s === "error",
    canStop: s === "running" || s === "starting",
    canRestart: s === "running",
  };
}

function StateLabel({ instance, stats }: { instance: Instance; stats?: Stats }) {
  const s = instance.state;
  return (
    <span className={`statelabel ${s}`}>
      <span className={`dot ${s}`} />
      {s === "running" && stats && !stats.ready ? "Starting…" : stateLabels[s] ?? s}
      {!instance.agentOnline && " (machine offline)"}
    </span>
  );
}

function ServerMeters({ instance, stats, agent }: { instance: Instance; stats?: Stats; agent?: Agent }) {
  const live = instance.state === "running" && stats;
  const players = live ? stats.playerCount : 0;
  const maxPlayers = stats?.maxPlayers ?? 0;
  const cores = agent?.numCpu || 1;
  const cpu = live ? stats.cpuPercent / cores : 0;
  const mem = live ? stats.memoryBytes : 0;
  const limit = memoryLimit(instance, agent);
  return (
    <div className="meters">
      <div className="meter-row players">
        <span>
          <b>{players}</b>
          {maxPlayers ? ` / ${maxPlayers}` : ""} Players
        </span>
        <Meter value={players} max={maxPlayers} color="blue" />
      </div>
      <div className="meter-row">
        <span className="meter-label">CPU</span>
        <div>
          <span>{live ? `${cpu.toFixed(0)}%` : "—"}</span>
          <Meter value={cpu} max={100} color="green" />
        </div>
      </div>
      <div className="meter-row">
        <span className="meter-label">RAM</span>
        <div>
          <span>
            {live ? formatSize(mem) : "—"}
            {limit ? <span className="muted"> / {formatSize(limit)}</span> : null}
          </span>
          <Meter value={mem} max={limit} color="orange" />
        </div>
      </div>
    </div>
  );
}

function ServerRow({
  token,
  instance,
  stats,
  agent,
  gameName,
  onOpen,
  onChanged,
}: {
  token: string;
  instance: Instance;
  stats?: Stats;
  agent?: Agent;
  gameName: string;
  onOpen: (id: string, tab?: Tab) => void;
  onChanged: () => void;
}) {
  const { act, busy, error, canStart, canStop, canRestart } = useActions(token, instance, onChanged);
  const live = instance.state === "running" && stats;
  return (
    <div className="server-row">
      <button className="thumb" onClick={() => onOpen(instance.id)} title={`Open ${instance.name}`}>
        <img src={gameArt(instance.templateId)} alt="" />
      </button>
      <div className="server-info">
        <button className="server-name" onClick={() => onOpen(instance.id)}>
          <span className={`dot ${instance.state}`} /> {instance.name}
        </button>
        <div className="muted">{gameName}</div>
        <div className="server-state">
          <StateLabel instance={instance} stats={stats} />
          {live && stats.uptimeSeconds ? <span className="muted"> ◷ {formatDuration(stats.uptimeSeconds)}</span> : null}
        </div>
        {error && <div className="error">{error}</div>}
      </div>
      <ServerMeters instance={instance} stats={stats} agent={agent} />
      <div className="row-actions">
        <button className="icon" disabled={busy || !canStart} onClick={() => act("/start")} title="Start">
          <Play size={18} />
        </button>
        <button className="icon" disabled={busy || !canStop} onClick={() => act("/stop")} title="Stop">
          <Square size={16} fill="currentColor" />
        </button>
        <button className="icon" disabled={busy || !canRestart} onClick={() => act("/restart")} title="Restart">
          <RotateCw size={18} />
        </button>
        <button className="icon" onClick={() => onOpen(instance.id)} title="Open server">
          <MoreVertical size={18} />
        </button>
      </div>
    </div>
  );
}

function timeOf(iso: string) {
  const d = new Date(iso);
  const sameDay = d.toDateString() === new Date().toDateString();
  return sameDay ? d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" }) : d.toLocaleDateString([], { month: "short", day: "numeric" });
}

const activityKinds: Record<string, { label: string; icon: ReactNode; color: string }> = {
  "player.join": { label: "Player joined", icon: <UserPlus size={22} />, color: "green" },
  "player.leave": { label: "Player left", icon: <UserMinus size={22} />, color: "red" },
  "server.ready": { label: "Server ready", icon: <Play size={22} />, color: "green" },
  "server.stopped": { label: "Server stopped", icon: <Square size={20} />, color: "muted" },
  "server.crashed": { label: "Server crashed", icon: <AlertTriangle size={22} />, color: "red" },
};

function RecentActivity({ activity, onOpen }: { activity: ActivityEvent[]; onOpen: (id: string) => void }) {
  const [all, setAll] = useState(false);
  const shown = all ? activity : activity.slice(0, 6);
  return (
    <section className="card activity">
      <div className="panel-head">
        <h3>Recent activity</h3>
        {activity.length > 6 && (
          <button className="link" onClick={() => setAll(!all)}>
            {all ? "Show less" : "View all"}
          </button>
        )}
      </div>
      {activity.length === 0 && <p className="muted">Nothing yet. Joins, leaves, starts and stops show up here.</p>}
      {shown.map((e, n) => {
        const kind = activityKinds[e.type] ?? { label: e.type, icon: <Activity size={22} />, color: "muted" };
        return (
          <button key={n} className="activity-item" onClick={() => onOpen(e.instance.id)}>
            <span className={`activity-icon ${kind.color}`}>{kind.icon}</span>
            <span className="activity-text">
              <span>{e.player ? `${e.player} ${kind.label.split(" ")[1]}` : kind.label}</span>
              <span className="muted">{e.instance.name}</span>
            </span>
            <span className="muted activity-time">{timeOf(e.time)}</span>
          </button>
        );
      })}
    </section>
  );
}

function SystemStatus({ agents, instances }: { agents: Agent[]; instances: Instance[] }) {
  const online = new Set(agents.map((a) => a.name));
  const offline = [...new Set(instances.filter((i) => !online.has(i.agent)).map((i) => i.agent))];
  const rows: { name: string; ok: boolean; detail: string }[] = [
    { name: "Watchtower core", ok: true, detail: "Online" },
    ...agents.map((a) => ({ name: `Machine ${a.name}`, ok: true, detail: "Online" })),
    ...offline.map((name) => ({ name: `Machine ${name}`, ok: false, detail: "Offline" })),
    ...agents
      .filter((a) => a.diskTotal)
      .map((a) => {
        const free = a.diskFree ?? 0;
        return { name: `Storage ${a.name}`, ok: free / (a.diskTotal ?? 1) > 0.1, detail: `${formatSize(free)} free` };
      }),
  ];
  return (
    <section className="card status">
      <h3>System status</h3>
      {rows.map((r) => (
        <div key={r.name} className="status-row">
          <span className={`ring ${r.ok ? "ok" : "bad"}`} />
          <span>{r.name}</span>
          <span className={r.ok ? "ok-text" : "error"}>
            <span className={`dot ${r.ok ? "running" : "error"}`} /> {r.detail}
          </span>
        </div>
      ))}
      {agents.length === 0 && offline.length === 0 && <p className="muted">No machines connected.</p>}
    </section>
  );
}

function Machines({ agents, instances }: { agents: Agent[]; instances: Instance[] }) {
  return (
    <section className="card">
      <div className="panel-head">
        <h2>Machines</h2>
      </div>
      {agents.length === 0 && <p className="muted pad">No agents connected.</p>}
      <div className="machines">
        {agents.map((a) => {
          const used = (a.diskTotal ?? 0) - (a.diskFree ?? 0);
          return (
            <div key={a.name} className="machine">
              <h3>
                <span className="dot running" /> {a.name}
              </h3>
              <p className="muted">
                {a.hostname} · {a.os}/{a.arch} · agent {a.version}
              </p>
              <p>
                <Cpu size={16} /> {a.numCpu} CPUs {a.memoryTotal ? <><MemoryStick size={16} /> {formatSize(a.memoryTotal)} RAM</> : null}
              </p>
              {a.diskTotal ? (
                <div className="meter-row">
                  <span>
                    <HardDrive size={16} /> {formatSize(used)} of {formatSize(a.diskTotal)} used
                  </span>
                  <Meter value={used} max={a.diskTotal} color="orange" />
                </div>
              ) : null}
              <p className="muted">{instances.filter((i) => i.agent === a.name).length} servers</p>
            </div>
          );
        })}
      </div>
    </section>
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
      <button type="submit" className="primary">Create</button>
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
      <button type="submit" className="primary">Save settings</button>
      {message && <p className="muted">{message}</p>}
      {error && <p className="error">{error}</p>}
    </form>
  );
}

function ServerView({
  token,
  instance,
  stats,
  agent,
  template,
  gameName,
  tab,
  setTab,
  onBack,
  onChanged,
}: {
  token: string;
  instance: Instance;
  stats?: Stats;
  agent?: Agent;
  template?: Template;
  gameName: string;
  tab: Tab;
  setTab: (tab: Tab) => void;
  onBack: () => void;
  onChanged: () => void;
}) {
  const { act, busy, error, canInstall, canStart, canStop, canRestart } = useActions(token, instance, onChanged);
  const s = instance.state;
  const installed = s !== "not_installed" && s !== "installing";
  const live = s === "running" && stats;

  async function remove() {
    if (!confirm(`Delete ${instance.name}, all of its files, and all of its backups? This cannot be undone.`)) return;
    if (await act("", "DELETE")) onBack();
  }

  return (
    <>
      <section className="card server-hero">
        <img className="hero-thumb" src={gameArt(instance.templateId)} alt="" />
        <div className="server-info">
          <h2>{instance.name}</h2>
          <div className="muted">
            {gameName} on {instance.agent} · ports{" "}
            {Object.entries(instance.ports)
              .map(([k, v]) => `${k} ${v}`)
              .join(", ")}
          </div>
          <div className="server-state">
            <StateLabel instance={instance} stats={stats} />
            {live && stats.uptimeSeconds ? <span className="muted"> ◷ up {formatDuration(stats.uptimeSeconds)}</span> : null}
          </div>
          {live && stats.players.length > 0 && <div className="muted">Online: {stats.players.join(", ")}</div>}
          {instance.error && <div className="error">{instance.error}</div>}
          {error && <div className="error">{error}</div>}
        </div>
        <ServerMeters instance={instance} stats={stats} agent={agent} />
        <div className="hero-actions">
          <button disabled={busy || !canInstall} onClick={() => act("/install")}>
            <Download size={16} /> {s === "not_installed" ? "Install" : "Update"}
          </button>
          <button className="primary" disabled={busy || !canStart} onClick={() => act("/start")}>
            <Play size={16} /> Start
          </button>
          <button disabled={busy || !canStop} onClick={() => act("/stop")}>
            <Square size={14} fill="currentColor" /> Stop
          </button>
          <button disabled={busy || !canRestart} onClick={() => act("/restart")}>
            <RotateCw size={16} /> Restart
          </button>
          <button disabled={busy || canStop || s === "installing"} className="danger" onClick={remove}>
            <Trash2 size={16} /> Delete
          </button>
        </div>
      </section>
      <section className="card">
        <nav className="tabs">
          {tabs.map((t) => (
            <button key={t.id} className={tab === t.id ? "active" : ""} onClick={() => setTab(t.id)}>
              {t.icon} {t.label}
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
          (installed && instance.agentOnline ? (
            <Files token={token} instance={instance} template={template} />
          ) : (
            <p className="muted">Install the server and keep its machine online to manage files.</p>
          ))}
        {tab === "backups" &&
          (installed && instance.agentOnline ? (
            <Backups token={token} instance={instance} />
          ) : (
            <p className="muted">Install the server and keep its machine online to manage backups.</p>
          ))}
      </section>
    </>
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
