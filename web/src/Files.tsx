import { useCallback, useEffect, useState } from "react";
import { FileInfo, api, formatSize, raw } from "./api";

const join = (dir: string, name: string) => (dir === "/" ? `/${name}` : `${dir}/${name}`);
const parent = (dir: string) => dir.slice(0, dir.lastIndexOf("/")) || "/";
const q = (path: string) => `?path=${encodeURIComponent(path)}`;

// Files browses and edits an instance's folder.
export default function Files({ token, id }: { token: string; id: string }) {
  const [dir, setDir] = useState("/");
  const [entries, setEntries] = useState<FileInfo[]>([]);
  const [editing, setEditing] = useState<{ path: string; text: string } | null>(null);
  const [error, setError] = useState("");
  const base = `/instances/${id}`;

  const load = useCallback(async () => {
    setError("");
    try {
      setEntries(await api<FileInfo[]>(token, "GET", `${base}/files${q(dir)}`));
    } catch (err) {
      setEntries([]);
      setError((err as Error).message);
    }
  }, [token, base, dir]);

  useEffect(() => {
    load();
  }, [load]);

  async function run(fn: () => Promise<unknown>) {
    setError("");
    try {
      await fn();
      await load();
    } catch (err) {
      setError((err as Error).message);
    }
  }

  async function open(path: string) {
    setError("");
    try {
      const res = await raw(token, "GET", `${base}/files/content${q(path)}`);
      setEditing({ path, text: await res.text() });
    } catch (err) {
      setError((err as Error).message);
    }
  }

  async function download(path: string) {
    const res = await raw(token, "GET", `${base}/files/content${q(path)}&download=1`);
    const url = URL.createObjectURL(await res.blob());
    const a = document.createElement("a");
    a.href = url;
    a.download = path.split("/").pop() ?? "file";
    a.click();
    URL.revokeObjectURL(url);
  }

  if (editing) {
    return (
      <div className="files">
        <div className="row">
          <strong>{editing.path}</strong>
          <span className="actions">
            <button
              onClick={() =>
                run(async () => {
                  await raw(token, "PUT", `${base}/files/content${q(editing.path)}`, editing.text);
                  setEditing(null);
                })
              }
            >
              Save
            </button>
            <button onClick={() => setEditing(null)}>Cancel</button>
          </span>
        </div>
        <p className="muted">Changes to game config files usually need a restart to take effect.</p>
        <textarea value={editing.text} onChange={(e) => setEditing({ ...editing, text: e.target.value })} spellCheck={false} />
        {error && <p className="error">{error}</p>}
      </div>
    );
  }

  return (
    <div className="files">
      <div className="row">
        <span>
          <strong>{dir}</strong>
        </span>
        <span className="actions">
          <button
            onClick={() => {
              const name = prompt("New folder name");
              if (name) run(() => api(token, "POST", `${base}/files/mkdir${q(join(dir, name))}`));
            }}
          >
            New folder
          </button>
          <label className="button">
            Upload
            <input
              type="file"
              hidden
              onChange={(e) => {
                const f = e.target.files?.[0];
                e.target.value = "";
                if (f) run(() => raw(token, "PUT", `${base}/files/content${q(join(dir, f.name))}`, f));
              }}
            />
          </label>
        </span>
      </div>
      <table>
        <tbody>
          {dir !== "/" && (
            <tr>
              <td colSpan={3}>
                <button className="link" onClick={() => setDir(parent(dir))}>
                  ..
                </button>
              </td>
            </tr>
          )}
          {entries.map((e) => {
            const path = join(dir, e.name);
            return (
              <tr key={e.name}>
                <td>
                  <button className="link" onClick={() => (e.dir ? setDir(path) : open(path))}>
                    {e.dir ? `${e.name}/` : e.name}
                  </button>
                </td>
                <td className="muted">{e.dir ? "" : formatSize(e.size)}</td>
                <td className="actions">
                  {!e.dir && <button onClick={() => download(path).catch((err) => setError(err.message))}>Download</button>}
                  <button
                    className="danger"
                    onClick={() => {
                      if (confirm(`Delete ${path}${e.dir ? " and everything in it" : ""}?`)) {
                        run(() => api(token, "DELETE", `${base}/files${q(path)}`));
                      }
                    }}
                  >
                    Delete
                  </button>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
      {entries.length === 0 && !error && <p className="muted">Empty folder.</p>}
      {error && <p className="error">{error}</p>}
      <p className="muted">Files up to 2 MB can be opened, edited, and uploaded here.</p>
    </div>
  );
}
