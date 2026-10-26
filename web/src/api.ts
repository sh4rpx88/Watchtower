export type Agent = {
  name: string;
  hostname: string;
  os: string;
  arch: string;
  version: string;
  connectedAt: string;
  lastHeartbeat: string;
  numCpu: number;
  memoryTotal?: number;
  diskTotal?: number;
  diskFree?: number;
};

export type ConfigField = {
  key: string;
  label: string;
  type: "string" | "int" | "bool" | "select";
  default: string;
  options?: string[];
  file?: string;
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
  connectAddress?: string;
  createdAt: string;
};

export class ApiError extends Error {
  constructor(
    message: string,
    public status: number,
  ) {
    super(message);
  }
}

export async function login(username: string, password: string): Promise<string> {
  const res = await fetch("/api/v1/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username, password }),
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

export type FileInfo = { name: string; dir: boolean; size: number; modTime: number };
export type BackupInfo = { name: string; size: number; createdAt: number };

// raw sends or fetches a file's bytes rather than JSON.
export async function raw(token: string, method: string, path: string, body?: BodyInit): Promise<Response> {
  const res = await fetch(`/api/v1${path}`, { method, headers: { Authorization: `Bearer ${token}` }, body });
  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new ApiError(err.error ?? `${method} ${path}: ${res.status}`, res.status);
  }
  return res;
}

export function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1 << 20) return `${(bytes / 1024).toFixed(1)} KB`;
  if (bytes < 1 << 30) return `${(bytes / (1 << 20)).toFixed(1)} MB`;
  return `${(bytes / (1 << 30)).toFixed(1)} GB`;
}

export type Stats = {
  id: string;
  name: string;
  state: string;
  ready: boolean;
  players: string[];
  playerCount: number;
  maxPlayers?: number;
  uptimeSeconds?: number;
  cpuPercent: number;
  memoryBytes: number;
};

export type APIToken = { id: string; name: string; scope: "read" | "control"; prefix: string; createdAt: string; lastUsed?: string };
export type Webhook = { id: string; name: string; url: string; format: "discord" | "json"; events: string[] };

export function formatDuration(seconds: number): string {
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (d) return `${d}d ${h}h`;
  if (h) return `${h}h ${m}m`;
  return `${m}m`;
}

export type ActivityEvent = {
  type: string;
  time: string;
  instance: { id: string; name: string; game: string };
  player?: string;
  playerCount: number;
  maxPlayers?: number;
  error?: string;
  actor?: string;
  message?: string;
};

export type Role = "viewer" | "operator" | "admin";
export type Me = { name: string; role: Role | "token" };
export type User = { id: string; username: string; role: Role; createdAt: string; lastLogin?: string };

// parseSize reads sizes like "2G" or "512M" from game settings, in bytes.
export function parseSize(value: string | undefined): number {
  const m = /^(\d+(?:\.\d+)?)\s*([KMGT]?)B?$/i.exec(value?.trim() ?? "");
  if (!m) return 0;
  const units: Record<string, number> = { "": 1, K: 1 << 10, M: 1 << 20, G: 1 << 30, T: 2 ** 40 };
  return Number(m[1]) * units[m[2].toUpperCase()];
}

export type Schedule = {
  id: string;
  instanceId: string;
  name: string;
  action: "restart" | "stop" | "start" | "backup" | "command";
  cron: string;
  command?: string;
  keep?: number;
  warn: boolean;
  enabled: boolean;
  lastRun?: string;
  lastResult?: string;
  nextRun?: string;
};

export type PanelSettings = { publicAddress: string; lanAddress: string };
export type SettingsResponse = { settings: PanelSettings; timeZone: string };
