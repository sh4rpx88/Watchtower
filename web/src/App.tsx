import { FormEvent, useEffect, useState } from "react";
import { Agent, Template, get, login } from "./api";

export default function App() {
  const [token, setToken] = useState(() => sessionStorage.getItem("token") ?? "");
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
  return (
    <Dashboard
      token={token}
      onLogout={() => {
        sessionStorage.removeItem("token");
        setToken("");
      }}
    />
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
    <form className="card login" onSubmit={submit}>
      <h1>Server Panel</h1>
      <input type="password" placeholder="Admin password" value={password} onChange={(e) => setPassword(e.target.value)} />
      <button type="submit">Log in</button>
      {error && <p className="error">{error}</p>}
    </form>
  );
}

function Dashboard({ token, onLogout }: { token: string; onLogout: () => void }) {
  const [agents, setAgents] = useState<Agent[]>([]);
  const [templates, setTemplates] = useState<Template[]>([]);

  useEffect(() => {
    const load = () =>
      Promise.all([get<Agent[]>("/agents", token), get<Template[]>("/templates", token)])
        .then(([a, t]) => {
          setAgents(a);
          setTemplates(t);
        })
        .catch(onLogout);
    load();
    const id = setInterval(load, 5000);
    return () => clearInterval(id);
  }, [token, onLogout]);

  return (
    <main>
      <header>
        <h1>Server Panel</h1>
        <button onClick={onLogout}>Log out</button>
      </header>
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
      <section className="card">
        <h2>Games</h2>
        <ul>
          {templates.map((t) => (
            <li key={t.id}>
              <strong>{t.name}</strong> {t.description}
            </li>
          ))}
        </ul>
      </section>
    </main>
  );
}
