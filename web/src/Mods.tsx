import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { ArrowDown, ArrowUp, ExternalLink, Lock, Package, Plus, Search, Trash2, X } from "lucide-react";
import { Instance, ModList, Template, WorkshopItem, api, formatSize } from "./api";

// Mods manages a server's mods: Steam Workshop items for games like Project
// Zomboid, or a Modrinth modpack for Minecraft.
export default function Mods({
  token,
  instance,
  template,
  onChanged,
}: {
  token: string;
  instance: Instance;
  template: Template;
  onChanged: () => void;
}) {
  if (template.mods?.kind === "workshop") return <WorkshopMods token={token} instance={instance} />;
  if (template.mods?.kind === "modrinth") return <Modpacks token={token} instance={instance} onChanged={onChanged} />;
  return null;
}

// Entry is one Workshop item on the server, with the mod IDs it loads.
type Entry = { id: string; item?: WorkshopItem; modIds: string[]; enabled: string[] };

type ModsResponse = { list: ModList; items: Record<string, WorkshopItem>; baseMap?: string; steamError?: string };

// entriesFrom matches the mod IDs in the settings file to the Workshop items
// whose pages list them. IDs no item claims are kept as "other mods".
function entriesFrom(r: ModsResponse): { entries: Entry[]; other: string[] } {
  const claimed = new Set<string>();
  const entries = r.list.workshopItems.map((id) => {
    const item = r.items[id];
    const modIds = item?.modIds ?? [];
    const enabled = r.list.mods.filter((m) => modIds.includes(m) && !claimed.has(m));
    enabled.forEach((m) => claimed.add(m));
    return { id, item, modIds, enabled };
  });
  return { entries, other: r.list.mods.filter((m) => !claimed.has(m)) };
}

function WorkshopMods({ token, instance }: { token: string; instance: Instance }) {
  const base = `/instances/${instance.id}/mods`;
  const [loaded, setLoaded] = useState<ModsResponse | null>(null);
  const [entries, setEntries] = useState<Entry[]>([]);
  const [other, setOther] = useState<string[]>([]);
  const [maps, setMaps] = useState("");
  const [dirty, setDirty] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [saving, setSaving] = useState(false);
  const stopped = instance.state === "stopped" || instance.state === "error";

  const load = useCallback(async () => {
    try {
      const r = await api<ModsResponse>(token, "GET", base);
      const { entries, other } = entriesFrom(r);
      setLoaded(r);
      setEntries(entries);
      setOther(other);
      setMaps(r.list.maps.join(";"));
      setDirty(false);
      setError("");
    } catch (err) {
      setError((err as Error).message);
    }
  }, [token, base]);
  useEffect(() => {
    load();
  }, [load]);

  function change(next: Entry[], nextMaps = maps) {
    setEntries(next);
    setMaps(nextMaps);
    setDirty(true);
    setMessage("");
  }

  // add puts newly found items at the end, skipping ones already on the
  // server, and puts their map folders ahead of the game's own map.
  function add(found: Entry[]) {
    const have = new Set(entries.map((e) => e.id));
    const fresh = found.filter((e) => !have.has(e.id));
    const mapList = maps
      .split(";")
      .map((m) => m.trim())
      .filter(Boolean);
    const baseMap = loaded?.baseMap;
    for (const e of fresh) {
      for (const folder of e.item?.mapFolders ?? []) {
        if (mapList.includes(folder)) continue;
        const at = baseMap ? mapList.indexOf(baseMap) : -1;
        if (at >= 0) mapList.splice(at, 0, folder);
        else mapList.push(folder);
      }
    }
    if (baseMap && mapList.length > 0 && !mapList.includes(baseMap)) mapList.push(baseMap);
    change([...entries, ...fresh], mapList.join(";"));
  }

  const move = (i: number, by: number) => {
    const next = [...entries];
    const [e] = next.splice(i, 1);
    next.splice(i + by, 0, e);
    change(next);
  };
  const update = (i: number, e: Entry) => change(entries.map((x, j) => (j === i ? e : x)));

  async function save() {
    setSaving(true);
    setError("");
    try {
      const mods = [...entries.flatMap((e) => e.modIds.filter((m) => e.enabled.includes(m))), ...other];
      const list = {
        workshopItems: entries.map((e) => e.id),
        mods: [...new Set(mods)],
        maps: maps
          .split(";")
          .map((m) => m.trim())
          .filter(Boolean),
      };
      await api(token, "PUT", base, list);
      setMessage("Saved. The server downloads new Workshop items when it next starts; the first start can take a few minutes.");
      await load();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setSaving(false);
    }
  }

  if (!loaded) return error ? <p className="error">{error}</p> : <p className="muted">Reading the server's mods…</p>;
  const ids = new Set(entries.map((e) => e.id));
  const enabledCount = entries.reduce((n, e) => n + e.enabled.length, 0) + other.length;

  return (
    <div className="mods">
      <p className="muted">
        Add mods from the Steam Workshop by pasting a link, an item ID, or a whole collection. The server downloads them itself when it
        starts, and players get them when they join. Mods load top to bottom, so libraries go first. They're saved in{" "}
        <code>{loaded.list.file}</code>.
      </p>
      {!stopped && (
        <p className="notice">
          <Lock size={14} /> Stop the server to change mods. The game rewrites its settings file while it runs.
        </p>
      )}
      {loaded.steamError && <p className="notice">Couldn't ask Steam for mod names ({loaded.steamError}), so items show their IDs.</p>}

      <AddMods token={token} base={base} onAdd={add} have={ids} />

      <div className="mods-head">
        <h3 className="section-title">
          On this server{" "}
          <span className="muted">
            {entries.length} items, {enabledCount} mods on
          </span>
        </h3>
      </div>
      {entries.length === 0 && <p className="muted">No Workshop mods yet.</p>}
      <ol className="mod-list">
        {entries.map((e, i) => (
          <li key={e.id} className="mod-card">
            <ModThumb item={e.item} />
            <div className="mod-body">
              <div className="mod-title">
                <strong>{e.item?.title ?? `Workshop item ${e.id}`}</strong>
                <a href={e.item?.url ?? `https://steamcommunity.com/sharedfiles/filedetails/?id=${e.id}`} target="_blank" rel="noreferrer">
                  {e.id} <ExternalLink size={12} />
                </a>
                {e.item?.size ? <span className="muted">{formatSize(e.item.size)}</span> : null}
              </div>
              <ModIdPicker entry={e} onChange={(next) => update(i, next)} />
              <MissingRequirements entry={e} ids={ids} token={token} base={base} onAdd={add} />
            </div>
            <div className="mod-actions">
              <button title="Load earlier" disabled={i === 0} onClick={() => move(i, -1)}>
                <ArrowUp size={14} />
              </button>
              <button title="Load later" disabled={i === entries.length - 1} onClick={() => move(i, 1)}>
                <ArrowDown size={14} />
              </button>
              <button title="Remove" className="danger" onClick={() => change(entries.filter((_, j) => j !== i))}>
                <Trash2 size={14} />
              </button>
            </div>
          </li>
        ))}
      </ol>

      {other.length > 0 && (
        <div className="mod-other">
          <h3 className="section-title">Other mod IDs</h3>
          <p className="muted">These are in the settings file but no Workshop item above lists them. They load after the rest.</p>
          <div className="chips">
            {other.map((m) => (
              <span key={m} className="chip on">
                {m}
                <button
                  title="Remove"
                  onClick={() => {
                    setOther(other.filter((x) => x !== m));
                    setDirty(true);
                  }}
                >
                  <X size={12} />
                </button>
              </span>
            ))}
          </div>
        </div>
      )}

      <label className="mod-maps">
        Map folders, in load order
        <input value={maps} onChange={(e) => change(entries, e.target.value)} placeholder={loaded.baseMap} />
        <span className="help">
          Separate them with semicolons. Map mods go first and {loaded.baseMap || "the game's own map"} goes last. Don't add a map mod to a
          world that's already been played without a backup.
        </span>
      </label>

      {(dirty || message || error) && (
        <div className="save-bar">
          {dirty && (
            <>
              <span>Unsaved changes</span>
              <button className="primary" onClick={save} disabled={saving || !stopped}>
                {saving ? "Saving…" : "Save mods"}
              </button>
              <button onClick={load}>Discard</button>
            </>
          )}
          {message && !dirty && <span className="muted">{message}</span>}
          {error && <span className="error">{error}</span>}
        </div>
      )}
    </div>
  );
}

function ModThumb({ item }: { item?: WorkshopItem }) {
  return item?.previewUrl ? (
    <img className="mod-thumb" src={item.previewUrl} alt="" loading="lazy" />
  ) : (
    <div className="mod-thumb empty">
      <Package size={22} />
    </div>
  );
}

// ModIdPicker turns a Workshop item's mod IDs on and off. Some items hold
// several mods, often alternatives, and some pages don't list their IDs, so
// one can be typed in.
function ModIdPicker({ entry, onChange }: { entry: Entry; onChange: (e: Entry) => void }) {
  const [typed, setTyped] = useState("");
  function toggle(m: string) {
    const enabled = entry.enabled.includes(m) ? entry.enabled.filter((x) => x !== m) : [...entry.enabled, m];
    onChange({ ...entry, enabled });
  }
  function addTyped(e: FormEvent) {
    e.preventDefault();
    const m = typed.trim();
    if (!m || /[;=\\]/.test(m)) return;
    onChange({
      ...entry,
      modIds: entry.modIds.includes(m) ? entry.modIds : [...entry.modIds, m],
      enabled: [...new Set([...entry.enabled, m])],
    });
    setTyped("");
  }
  return (
    <div className="chips">
      {entry.modIds.length === 0 && (
        <span className="error small">No mod ID on its Workshop page. Type the ID from the page or mod.info.</span>
      )}
      {entry.modIds.map((m) => (
        <label key={m} className={`chip${entry.enabled.includes(m) ? " on" : ""}`}>
          <input type="checkbox" checked={entry.enabled.includes(m)} onChange={() => toggle(m)} />
          {m}
        </label>
      ))}
      <form className="chip-add" onSubmit={addTyped}>
        <input value={typed} onChange={(e) => setTyped(e.target.value)} placeholder="Add a mod ID" />
      </form>
    </div>
  );
}

function MissingRequirements({
  entry,
  ids,
  token,
  base,
  onAdd,
}: {
  entry: Entry;
  ids: Set<string>;
  token: string;
  base: string;
  onAdd: (found: Entry[]) => void;
}) {
  const missing = (entry.item?.requires ?? []).filter((r) => !ids.has(r));
  const [busy, setBusy] = useState(false);
  if (missing.length === 0) return null;
  async function addThem() {
    setBusy(true);
    try {
      const r = await api<{ items: WorkshopItem[] }>(token, "POST", `${base}/lookup`, { query: missing.join(" ") });
      onAdd(r.items.map(toEntry));
    } finally {
      setBusy(false);
    }
  }
  return (
    <p className="notice small">
      Needs {missing.length} more Workshop item{missing.length === 1 ? "" : "s"} ({missing.join(", ")}).
      <button onClick={addThem} disabled={busy}>
        <Plus size={14} /> Add {missing.length === 1 ? "it" : "them"}
      </button>
    </p>
  );
}

const toEntry = (item: WorkshopItem): Entry => ({ id: item.id, item, modIds: item.modIds, enabled: item.modIds });

// AddMods looks up pasted links or IDs and shows what was found before it's added.
function AddMods({ token, base, onAdd, have }: { token: string; base: string; onAdd: (found: Entry[]) => void; have: Set<string> }) {
  const [query, setQuery] = useState("");
  const [found, setFound] = useState<Entry[] | null>(null);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [notes, setNotes] = useState<string[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function lookup(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const r = await api<{ items: WorkshopItem[]; notes: string[] }>(token, "POST", `${base}/lookup`, { query });
      const entries = r.items.map(toEntry);
      setFound(entries);
      setPicked(new Set(entries.filter((x) => !have.has(x.id)).map((x) => x.id)));
      setNotes(r.notes ?? []);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  function confirm() {
    onAdd((found ?? []).filter((e) => picked.has(e.id)));
    setFound(null);
    setQuery("");
    setNotes([]);
  }

  const togglePick = (id: string) => {
    const next = new Set(picked);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    setPicked(next);
  };

  return (
    <div className="mod-add">
      <form onSubmit={lookup}>
        <label className="search">
          <Search size={16} />
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Paste a Workshop link, item ID, or collection link"
            aria-label="Workshop link or ID"
          />
        </label>
        <button type="submit" className="primary" disabled={busy || !query.trim()}>
          {busy ? "Looking up…" : "Find"}
        </button>
      </form>
      {error && <p className="error">{error}</p>}
      {found && (
        <div className="mod-found">
          {notes.map((n) => (
            <p key={n} className="muted small">
              {n}
            </p>
          ))}
          {found.length === 0 ? (
            <p className="muted">Nothing to add.</p>
          ) : (
            <>
              <p className="muted small">
                Found {found.length} item{found.length === 1 ? "" : "s"}. Items another one needs are listed first so they load first.
                Untick any mod IDs you don't want; some items offer alternatives.
              </p>
              <ul className="mod-list compact">
                {found.map((e, i) => (
                  <li key={e.id} className={`mod-card${have.has(e.id) ? " have" : ""}`}>
                    <input type="checkbox" checked={picked.has(e.id)} disabled={have.has(e.id)} onChange={() => togglePick(e.id)} />
                    <ModThumb item={e.item} />
                    <div className="mod-body">
                      <div className="mod-title">
                        <strong>{e.item?.title}</strong>
                        <span className="muted">{have.has(e.id) ? "already on the server" : e.id}</span>
                      </div>
                      <ModIdPicker entry={e} onChange={(next) => setFound(found.map((x, j) => (j === i ? next : x)))} />
                    </div>
                  </li>
                ))}
              </ul>
              <div className="row">
                <button className="primary" onClick={confirm} disabled={picked.size === 0}>
                  <Plus size={16} /> Add {picked.size} to the list
                </button>
                <button onClick={() => setFound(null)}>Cancel</button>
              </div>
            </>
          )}
        </div>
      )}
    </div>
  );
}

// Modrinth's API allows browser requests, so modpack search runs here.
const MODRINTH = "https://api.modrinth.com/v2";

async function modrinth<T>(path: string): Promise<T> {
  const res = await fetch(MODRINTH + path);
  if (!res.ok) throw new Error(res.status === 404 ? "Modrinth doesn't have that" : `Modrinth answered ${res.status}`);
  return res.json();
}

type PackHit = {
  project_id: string;
  slug: string;
  title: string;
  description: string;
  icon_url?: string;
  downloads: number;
  author?: string;
};
type PackVersion = {
  id: string;
  project_id: string;
  name: string;
  version_number: string;
  game_versions: string[];
  loaders: string[];
  date_published: string;
};
type PackProject = { id: string; slug: string; title: string; description: string; icon_url?: string; downloads: number };

const loaderLabel: Record<string, string> = { fabric: "Fabric", forge: "Forge", neoforge: "NeoForge", quilt: "Quilt" };
const serverLoader = (v: PackVersion) => v.loaders.find((l) => l in loaderLabel) ?? "vanilla";

function Modpacks({ token, instance, onChanged }: { token: string; instance: Instance; onChanged: () => void }) {
  const current = instance.settings.modpack ?? "";
  const loader = instance.settings.loader ?? "vanilla";
  const [installed, setInstalled] = useState<{ version: PackVersion; project: PackProject } | null>(null);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const stopped = instance.state === "stopped" || instance.state === "error" || instance.state === "not_installed";

  useEffect(() => {
    setInstalled(null);
    if (!current) return;
    modrinth<PackVersion>(`/version/${current}`)
      .then(async (version) => setInstalled({ version, project: await modrinth<PackProject>(`/project/${version.project_id}`) }))
      .catch((err) => setError(`Couldn't read the modpack from Modrinth: ${(err as Error).message}`));
  }, [current]);

  async function install(v: PackVersion, title: string) {
    const ok = window.confirm(
      `Install ${title} ${v.version_number} on ${instance.name}?\n\nThis sets Minecraft ${v.game_versions[0]} with ${loaderLabel[serverLoader(v)] ?? "no loader"} and replaces the mods from any previous pack. The world stays, but mods that change can break it, so make a backup first if you care about it.`,
    );
    if (!ok) return;
    setError("");
    try {
      await api(token, "PUT", `/instances/${instance.id}/settings`, {
        modpack: v.id,
        loader: serverLoader(v),
        version: v.game_versions[0],
      });
      await api(token, "POST", `/instances/${instance.id}/install`);
      setMessage(`Installing ${title}. Follow along on the Console tab.`);
      onChanged();
    } catch (err) {
      setError((err as Error).message);
    }
  }

  async function removePack() {
    if (!window.confirm("Stop using this modpack? Press Update afterwards to remove its mods. The world and configs stay.")) return;
    try {
      await api(token, "PUT", `/instances/${instance.id}/settings`, { modpack: "" });
      setMessage("Modpack removed. Press Update to take its mods off the server.");
      onChanged();
    } catch (err) {
      setError((err as Error).message);
    }
  }

  return (
    <div className="mods">
      <p className="muted">
        Install a modpack from Modrinth and Watchtower sets up the Minecraft version and mod loader it needs, downloads its server mods, and
        keeps them in step when you pick a newer version. To run a loader with your own mods instead, pick the loader under Settings and put
        the mods in the <code>mods</code> folder with the File Manager.
      </p>
      {!stopped && (
        <p className="notice">
          <Lock size={14} /> Stop the server to install or change a modpack.
        </p>
      )}
      {error && <p className="error">{error}</p>}
      {message && <p className="notice">{message}</p>}

      <h3 className="section-title">Installed</h3>
      {current ? (
        installed ? (
          <div className="mod-card pack current">
            <PackIcon url={installed.project.icon_url} />
            <div className="mod-body">
              <div className="mod-title">
                <strong>{installed.project.title}</strong>
                <a href={`https://modrinth.com/modpack/${installed.project.slug}`} target="_blank" rel="noreferrer">
                  Modrinth <ExternalLink size={12} />
                </a>
              </div>
              <div className="muted">
                Version {installed.version.version_number} · Minecraft {installed.version.game_versions.join(", ")} ·{" "}
                {loaderLabel[serverLoader(installed.version)] ?? "Vanilla"}
              </div>
            </div>
            <div className="mod-actions">
              <button className="danger" onClick={removePack} disabled={!stopped}>
                <Trash2 size={14} /> Remove
              </button>
            </div>
          </div>
        ) : (
          <p className="muted">Modpack version {current}</p>
        )
      ) : (
        <p className="muted">
          No modpack. Running Minecraft {instance.settings.version || "latest"}{" "}
          {loader === "vanilla" ? "(vanilla)" : `on ${loaderLabel[loader] ?? loader}`}.
        </p>
      )}
      {installed && (
        <PackVersions
          project={installed.project}
          currentVersion={current}
          disabled={!stopped}
          onInstall={(v) => install(v, installed.project.title)}
          label="Change version"
        />
      )}

      <h3 className="section-title">Find a modpack</h3>
      <PackSearch disabled={!stopped} onInstall={install} />
    </div>
  );
}

function PackIcon({ url }: { url?: string }) {
  return url ? (
    <img className="mod-thumb" src={url} alt="" loading="lazy" />
  ) : (
    <div className="mod-thumb empty">
      <Package size={22} />
    </div>
  );
}

// PackSearch searches Modrinth for modpacks that run on servers. A
// modrinth.com link opens that pack directly.
function PackSearch({ disabled, onInstall }: { disabled: boolean; onInstall: (v: PackVersion, title: string) => void }) {
  const [query, setQuery] = useState("");
  const [hits, setHits] = useState<PackHit[] | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const search = useCallback(async (q: string) => {
    setBusy(true);
    setError("");
    try {
      const link = q.match(/modrinth\.com\/modpack\/([A-Za-z0-9_.-]+)/);
      if (link) {
        const p = await modrinth<PackProject>(`/project/${link[1]}`);
        setHits([
          { project_id: p.id, slug: p.slug, title: p.title, description: p.description, icon_url: p.icon_url, downloads: p.downloads },
        ]);
        setOpen(p.id);
        return;
      }
      const facets = encodeURIComponent(JSON.stringify([["project_type:modpack"], ["server_side:required", "server_side:optional"]]));
      const r = await modrinth<{ hits: PackHit[] }>(`/search?limit=20&index=relevance&facets=${facets}&query=${encodeURIComponent(q)}`);
      setHits(r.hits);
      setOpen(null);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }, []);

  useEffect(() => {
    search("");
  }, [search]);

  return (
    <div className="mod-add">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          search(query.trim());
        }}
      >
        <label className="search">
          <Search size={16} />
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search Modrinth, or paste a modrinth.com link"
            aria-label="Search modpacks"
          />
        </label>
        <button type="submit" disabled={busy}>
          {busy ? "Searching…" : "Search"}
        </button>
      </form>
      {error && <p className="error">{error}</p>}
      {hits && hits.length === 0 && <p className="muted">No modpacks match.</p>}
      <ul className="mod-list">
        {hits?.map((h) => (
          <li key={h.project_id} className="mod-card pack">
            <PackIcon url={h.icon_url} />
            <div className="mod-body">
              <div className="mod-title">
                <strong>{h.title}</strong>
                {h.author && <span className="muted">by {h.author}</span>}
                <span className="muted">{h.downloads.toLocaleString()} downloads</span>
                <a href={`https://modrinth.com/modpack/${h.slug}`} target="_blank" rel="noreferrer">
                  Modrinth <ExternalLink size={12} />
                </a>
              </div>
              <p className="setting-desc">{h.description}</p>
              {open === h.project_id && (
                <PackVersions
                  project={{ id: h.project_id, slug: h.slug, title: h.title, description: h.description, downloads: h.downloads }}
                  disabled={disabled}
                  onInstall={(v) => onInstall(v, h.title)}
                  label="Version"
                />
              )}
            </div>
            {open !== h.project_id && (
              <div className="mod-actions">
                <button onClick={() => setOpen(h.project_id)}>Choose</button>
              </div>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}

function PackVersions({
  project,
  currentVersion,
  disabled,
  onInstall,
  label,
}: {
  project: PackProject;
  currentVersion?: string;
  disabled: boolean;
  onInstall: (v: PackVersion) => void;
  label: string;
}) {
  const [versions, setVersions] = useState<PackVersion[] | null>(null);
  const [picked, setPicked] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    modrinth<PackVersion[]>(`/project/${project.id}/version`).then(
      (v) => {
        setVersions(v);
        setPicked(v[0]?.id ?? "");
      },
      (err) => setError((err as Error).message),
    );
  }, [project.id]);

  const version = useMemo(() => versions?.find((v) => v.id === picked), [versions, picked]);
  if (error) return <p className="error">{error}</p>;
  if (!versions) return <p className="muted small">Loading versions…</p>;
  if (versions.length === 0) return <p className="muted small">This pack has no versions.</p>;
  return (
    <div className="pack-versions">
      <label>
        {label}
        <select value={picked} onChange={(e) => setPicked(e.target.value)}>
          {versions.map((v) => (
            <option key={v.id} value={v.id}>
              {v.version_number} · Minecraft {v.game_versions[0]} · {loaderLabel[serverLoader(v)] ?? "Vanilla"}
              {v.id === currentVersion ? " (installed)" : ""}
            </option>
          ))}
        </select>
      </label>
      <button
        className="primary"
        disabled={disabled || !version || version.id === currentVersion}
        onClick={() => version && onInstall(version)}
      >
        <Package size={16} /> {currentVersion ? "Install this version" : "Install"}
      </button>
    </div>
  );
}
