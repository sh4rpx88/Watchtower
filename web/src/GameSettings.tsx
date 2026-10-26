import { useCallback, useEffect, useMemo, useState } from "react";
import { ChevronDown, Lock, Search } from "lucide-react";
import { api, GameConfigFile, GameSetting, Instance } from "./api";

// GameSettings edits the game's own settings files (for Project Zomboid, the
// server .ini and SandboxVars.lua). The agent reads every option from the
// files with the descriptions and choices in their comments, so the list
// always matches the installed game version.
export default function GameSettings({ token, instance }: { token: string; instance: Instance }) {
  const [files, setFiles] = useState<GameConfigFile[] | null>(null);
  const [error, setError] = useState("");

  const load = useCallback(
    () =>
      api<GameConfigFile[]>(token, "GET", `/instances/${instance.id}/game-config`).then(
        (f) => {
          setFiles(f);
          setError("");
        },
        (err) => setError((err as Error).message),
      ),
    [token, instance.id],
  );
  useEffect(() => {
    load();
  }, [load, instance.state]);

  if (error) return <p className="error">{error}</p>;
  if (!files) return <p className="muted">Reading the game's settings…</p>;
  return (
    <>
      {files.map((f, i) => (
        <GameConfigEditor key={f.path} token={token} instance={instance} file={f} index={i} onSaved={load} />
      ))}
    </>
  );
}

// label turns "PVPMeleeDamageModifier" into "PVP melee damage modifier".
export function settingLabel(key: string) {
  const name = key.slice(key.lastIndexOf(".") + 1);
  const words = name
    .replace(/_/g, " ")
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .replace(/([A-Z]+)([A-Z][a-z])/g, "$1 $2")
    .split(" ");
  return words.map((w, i) => (i === 0 || /^[A-Z0-9]+$/.test(w) ? w : w.toLowerCase())).join(" ");
}

function GameConfigEditor({
  token,
  instance,
  file,
  index,
  onSaved,
}: {
  token: string;
  instance: Instance;
  file: GameConfigFile;
  index: number;
  onSaved: () => void;
}) {
  const [changes, setChanges] = useState<Record<string, string>>({});
  const [query, setQuery] = useState("");
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const stopped = instance.state === "stopped" || instance.state === "error";
  const count = Object.keys(changes).length;

  const q = query.trim().toLowerCase();
  const sections = useMemo(
    () =>
      file.sections
        .map((s) => ({
          ...s,
          settings: q
            ? s.settings.filter((x) => `${x.key} ${settingLabel(x.key)} ${x.description ?? ""}`.toLowerCase().includes(q))
            : s.settings,
        }))
        .filter((s) => s.settings.length > 0),
    [file.sections, q],
  );
  const total = file.sections.reduce((n, s) => n + s.settings.length, 0);

  function change(s: GameSetting, value: string) {
    const next = { ...changes };
    if (value === s.value) delete next[s.key];
    else next[s.key] = value;
    setChanges(next);
    setMessage("");
  }

  async function save() {
    setSaving(true);
    setError("");
    try {
      await api(token, "PUT", `/instances/${instance.id}/game-config/${index}`, { values: changes });
      setChanges({});
      setMessage("Saved. The new settings apply when the server starts.");
      onSaved();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setSaving(false);
    }
  }

  return (
    <section className="game-config">
      <div className="game-config-head">
        <div>
          <h3 className="section-title">{file.label}</h3>
          <p className="muted">
            {file.help} <code>{file.path}</code>
          </p>
        </div>
        {file.exists && (
          <label className="search small">
            <Search size={16} />
            <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder={`Search ${total} settings`} />
          </label>
        )}
      </div>

      {!file.exists ? (
        <p className="notice">
          Project Zomboid writes this file the first time the server starts. Press Install, start the server once, wait until it says it's
          ready, then stop it. Every option for your game version will show up here.
        </p>
      ) : (
        <>
          {!stopped && (
            <p className="notice">
              <Lock size={14} /> Stop the server to change these. The game rewrites this file while it runs, so changes made now would be
              lost.
            </p>
          )}
          {sections.map((s) => (
            <details key={s.name} className="config-section" open={!!q}>
              <summary>
                <ChevronDown size={16} className="chev" />
                {s.name}
                <span className="muted">{s.settings.length}</span>
                {s.settings.some((x) => x.key in changes) && <span className="dot" title="Changed" />}
              </summary>
              <div className="config-grid">
                {s.settings.map((x) => (
                  <SettingInput
                    key={x.key}
                    setting={x}
                    value={changes[x.key] ?? x.value}
                    disabled={!stopped}
                    onChange={(v) => change(x, v)}
                  />
                ))}
              </div>
            </details>
          ))}
          {sections.length === 0 && <p className="muted">No settings match “{query}”.</p>}
          {(count > 0 || message || error) && (
            <div className="save-bar">
              {count > 0 && (
                <>
                  <span>
                    {count} unsaved change{count === 1 ? "" : "s"}
                  </span>
                  <button className="primary" onClick={save} disabled={saving || !stopped}>
                    {saving ? "Saving…" : `Save ${file.label.toLowerCase()}`}
                  </button>
                  <button onClick={() => setChanges({})}>Discard</button>
                </>
              )}
              {message && count === 0 && <span className="muted">{message}</span>}
              {error && <span className="error">{error}</span>}
            </div>
          )}
        </>
      )}
    </section>
  );
}

function SettingInput({
  setting: s,
  value,
  disabled,
  onChange,
}: {
  setting: GameSetting;
  value: string;
  disabled: boolean;
  onChange: (value: string) => void;
}) {
  const locked = !!s.locked;
  const changed = value !== s.value;
  const range = s.min !== undefined || s.max !== undefined ? `${s.min ?? "…"} to ${s.max ?? "…"}` : "";
  const defaultLabel = s.choices?.find((c) => c.value === s.default)?.label ?? s.default;
  const meta = [range && `Range ${range}`, s.default && `Default ${defaultLabel}`].filter(Boolean).join(" · ");

  let input;
  if (s.type === "bool") {
    input = (
      <label className="check">
        <input
          type="checkbox"
          checked={value.toLowerCase() === "true"}
          disabled={disabled || locked}
          onChange={(e) => onChange(String(e.target.checked))}
        />
        {value.toLowerCase() === "true" ? "On" : "Off"}
      </label>
    );
  } else if (s.type === "select" && s.choices) {
    const known = s.choices.some((c) => c.value === value);
    input = (
      <select value={value} disabled={disabled || locked} onChange={(e) => onChange(e.target.value)}>
        {!known && <option value={value}>{value}</option>}
        {s.choices.map((c) => (
          <option key={c.value} value={c.value}>
            {c.label}
          </option>
        ))}
      </select>
    );
  } else {
    const numeric = s.type === "int" || s.type === "number";
    input = (
      <input
        type={numeric ? "number" : "text"}
        step={s.type === "number" ? "any" : undefined}
        min={s.min}
        max={s.max}
        value={value}
        disabled={disabled || locked}
        onChange={(e) => onChange(e.target.value)}
      />
    );
  }

  return (
    <div className={`setting${changed ? " changed" : ""}${locked ? " locked" : ""}`}>
      <div className="setting-name">
        <strong>{settingLabel(s.key)}</strong>
        <code>{s.key}</code>
      </div>
      {s.description && <p className="setting-desc">{s.description}</p>}
      {input}
      {locked ? (
        <p className="setting-meta">
          <Lock size={12} /> {s.locked}
        </p>
      ) : (
        meta && <p className="setting-meta">{meta}</p>
      )}
    </div>
  );
}
