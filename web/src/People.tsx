import { FormEvent, ReactNode, useCallback, useEffect, useState } from "react";
import { Activity, AlertTriangle, Play, Square, UserMinus, UserPlus } from "lucide-react";
import { ActivityEvent, Instance, Me, Role, User, api } from "./api";

function timeOf(iso: string) {
  const d = new Date(iso);
  const sameDay = d.toDateString() === new Date().toDateString();
  return sameDay
    ? d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })
    : d.toLocaleDateString([], { month: "short", day: "numeric" });
}

const activityKinds: Record<string, { label: string; icon: ReactNode; color: string }> = {
  "player.join": { label: "joined", icon: <UserPlus size={22} />, color: "green" },
  "player.leave": { label: "left", icon: <UserMinus size={22} />, color: "red" },
  "server.ready": { label: "Server ready", icon: <Play size={22} />, color: "green" },
  "server.stopped": { label: "Server stopped", icon: <Square size={20} />, color: "muted" },
  "server.crashed": { label: "Server crashed", icon: <AlertTriangle size={22} />, color: "red" },
  action: { label: "", icon: <Activity size={22} />, color: "action" },
};

function activityText(e: ActivityEvent) {
  if (e.type === "action") return `${e.actor} ${e.message}`;
  const label = activityKinds[e.type]?.label ?? e.type;
  return e.player ? `${e.player} ${label}` : label;
}

export function ActivityItem({ event: e, onOpen, full }: { event: ActivityEvent; onOpen: (id: string) => void; full?: boolean }) {
  const kind = activityKinds[e.type] ?? activityKinds.action;
  const time = full ? new Date(e.time).toLocaleString([], { dateStyle: "medium", timeStyle: "short" }) : timeOf(e.time);
  return (
    <button className="activity-item" onClick={() => e.instance?.id && onOpen(e.instance.id)} disabled={!e.instance?.id}>
      <span className={`activity-icon ${kind.color}`}>{kind.icon}</span>
      <span className="activity-text">
        <span>{activityText(e)}</span>
        {e.instance?.name && <span className="muted">{e.instance.name}</span>}
        {e.error && <span className="error">{e.error}</span>}
      </span>
      <span className="muted activity-time">{time}</span>
    </button>
  );
}

// ActivityLog is the full log: game events and what people did.
export function ActivityLog({ token, instances, onOpen }: { token: string; instances: Instance[]; onOpen: (id: string) => void }) {
  const [events, setEvents] = useState<ActivityEvent[]>([]);
  const [kind, setKind] = useState("");
  const [instance, setInstance] = useState("");
  const [error, setError] = useState("");

  const load = useCallback(() => {
    const q = new URLSearchParams({ limit: "500", kind, instance });
    api<ActivityEvent[]>(token, "GET", `/activity?${q}`).then(setEvents, (e) => setError(e.message));
  }, [token, kind, instance]);
  useEffect(() => {
    load();
    const id = setInterval(load, 5000);
    return () => clearInterval(id);
  }, [load]);

  return (
    <section className="card activity">
      <div className="panel-head">
        <h2>Activity</h2>
        <select value={kind} onChange={(e) => setKind(e.target.value)}>
          <option value="">Everything</option>
          <option value="action">What people did</option>
          <option value="game">Players and servers</option>
        </select>
        <select value={instance} onChange={(e) => setInstance(e.target.value)}>
          <option value="">All servers</option>
          {instances.map((i) => (
            <option key={i.id} value={i.id}>
              {i.name}
            </option>
          ))}
        </select>
      </div>
      {error && <p className="error">{error}</p>}
      {events.length === 0 && <p className="muted">Nothing here yet.</p>}
      {events.map((e, n) => (
        <ActivityItem key={n} event={e} onOpen={onOpen} full />
      ))}
      <p className="muted">The last 1,000 entries are kept.</p>
    </section>
  );
}

const roleHelp: Record<Role, string> = {
  viewer: "sees servers, players and activity",
  operator: "also starts and stops servers, uses the console, settings, files and backups",
  admin: "also adds and deletes servers, and manages users, tokens and webhooks",
};

export function UserList({ token, me }: { token: string; me: Me }) {
  const [users, setUsers] = useState<User[]>([]);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<Role>("operator");
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");

  const load = useCallback(() => api<User[]>(token, "GET", "/users").then(setUsers, (e) => setError(e.message)), [token]);
  useEffect(() => {
    load();
  }, [load]);

  async function run(fn: () => Promise<unknown>, done?: string) {
    setError("");
    setMessage("");
    try {
      await fn();
      if (done) setMessage(done);
      load();
      return true;
    } catch (e) {
      setError((e as Error).message);
      return false;
    }
  }

  async function add(e: FormEvent) {
    e.preventDefault();
    if (await run(() => api(token, "POST", "/users", { username, password, role }), `Added ${username}.`)) {
      setUsername("");
      setPassword("");
    }
  }

  function resetPassword(u: User) {
    const pw = prompt(`New password for ${u.username} (at least 8 characters). They'll be logged out everywhere.`);
    if (pw) run(() => api(token, "PATCH", `/users/${u.id}`, { password: pw }), `Changed the password for ${u.username}.`);
  }

  function remove(u: User) {
    if (confirm(`Remove ${u.username}? They'll be logged out right away.`)) run(() => api(token, "DELETE", `/users/${u.id}`));
  }

  return (
    <section className="card integration">
      <div className="panel-head">
        <h2>Users</h2>
      </div>
      <ul className="muted roles">
        {(Object.keys(roleHelp) as Role[]).map((r) => (
          <li key={r}>
            <b>{r[0].toUpperCase() + r.slice(1)}</b> {roleHelp[r]}
          </li>
        ))}
      </ul>
      <table>
        <tbody>
          {users.map((u) => (
            <tr key={u.id}>
              <td>
                <strong>{u.username}</strong>
                {u.username === me.name && <span className="muted"> (you)</span>}
              </td>
              <td>
                <select value={u.role} onChange={(e) => run(() => api(token, "PATCH", `/users/${u.id}`, { role: e.target.value }))}>
                  <option value="viewer">Viewer</option>
                  <option value="operator">Operator</option>
                  <option value="admin">Admin</option>
                </select>
              </td>
              <td className="muted">{u.lastLogin ? `last in ${new Date(u.lastLogin).toLocaleString()}` : "never logged in"}</td>
              <td className="actions">
                <button onClick={() => resetPassword(u)}>Reset password</button>
                <button className="danger" disabled={u.username === me.name} onClick={() => remove(u)}>
                  Remove
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <h3>Add a user</h3>
      <form className="inline" onSubmit={add}>
        <input placeholder="Username" value={username} onChange={(e) => setUsername(e.target.value)} required />
        <input
          type="password"
          placeholder="Password (8+ characters)"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          required
        />
        <select value={role} onChange={(e) => setRole(e.target.value as Role)}>
          <option value="viewer">Viewer</option>
          <option value="operator">Operator</option>
          <option value="admin">Admin</option>
        </select>
        <button type="submit" className="primary">
          Add user
        </button>
      </form>
      {message && <p className="muted">{message}</p>}
      {error && <p className="error">{error}</p>}
    </section>
  );
}

export function Account({ token, me }: { token: string; me: Me }) {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirmNext, setConfirmNext] = useState("");
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");

  async function save(e: FormEvent) {
    e.preventDefault();
    setError("");
    setMessage("");
    if (next !== confirmNext) {
      setError("The new passwords don't match.");
      return;
    }
    try {
      await api(token, "PUT", "/me/password", { current, new: next });
      setMessage("Password changed. Your other sessions were logged out.");
      setCurrent("");
      setNext("");
      setConfirmNext("");
    } catch (err) {
      setError((err as Error).message);
    }
  }

  return (
    <section className="card">
      <div className="panel-head">
        <h2>Your account</h2>
      </div>
      <p>
        Logged in as <strong>{me.name}</strong> ({me.role}).
      </p>
      <form className="create" onSubmit={save}>
        <h3>Change password</h3>
        <input
          type="password"
          placeholder="Current password"
          autoComplete="current-password"
          value={current}
          onChange={(e) => setCurrent(e.target.value)}
          required
        />
        <input
          type="password"
          placeholder="New password (8+ characters)"
          autoComplete="new-password"
          value={next}
          onChange={(e) => setNext(e.target.value)}
          required
        />
        <input
          type="password"
          placeholder="New password again"
          autoComplete="new-password"
          value={confirmNext}
          onChange={(e) => setConfirmNext(e.target.value)}
          required
        />
        <button type="submit" className="primary">
          Change password
        </button>
        {message && <p className="muted">{message}</p>}
        {error && <p className="error">{error}</p>}
      </form>
    </section>
  );
}
