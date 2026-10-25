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

export type ConfigField = {
  key: string;
  label: string;
  type: "string" | "int" | "bool" | "select";
  default: string;
  options?: string[];
  required?: boolean;
};

export type Port = { name: string; port: number; protocol: string };

export type Template = {
  id: string;
  name: string;
  description: string;
  ports: Port[];
  config: ConfigField[];
};

export type Instance = {
  id: string;
  name: string;
  templateId: string;
  agent: string;
  settings: Record<string, string>;
  ports: Record<string, number>;
  state: string;
  error?: string;
  agentOnline: boolean;
};

export class ApiError extends Error {
  constructor(
    message: string,
    public status: number,
  ) {
    super(message);
  }
}

export async function login(password: string): Promise<string> {
  const res = await fetch("/api/v1/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ password }),
  });
  if (!res.ok) throw new ApiError((await res.json()).error ?? "Login failed", res.status);
  return (await res.json()).token;
}

export async function api<T>(token: string, method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`/api/v1${path}`, {
    method,
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new ApiError(err.error ?? `${method} ${path}: ${res.status}`, res.status);
  }
  return res.status === 204 ? (undefined as T) : res.json();
}

export function consoleURL(token: string, id: string): string {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${location.host}/api/v1/instances/${id}/console?access_token=${encodeURIComponent(token)}`;
}
