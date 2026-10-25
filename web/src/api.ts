export type Agent = {
  name: string;
  hostname: string;
  os: string;
  arch: string;
  version: string;
  connectedAt: string;
  lastHeartbeat: string;
  numCpu: number;
};

export type Template = { id: string; name: string; description: string };

export async function login(password: string): Promise<string> {
  const res = await fetch("/api/v1/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ password }),
  });
  if (!res.ok) throw new Error((await res.json()).error ?? "Login failed");
  return (await res.json()).token;
}

export async function get<T>(path: string, token: string): Promise<T> {
  const res = await fetch(`/api/v1${path}`, { headers: { Authorization: `Bearer ${token}` } });
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json();
}
