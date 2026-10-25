import { useCallback, useEffect, useState } from "react";
import { BackupInfo, Instance, api, formatSize } from "./api";

// Backups lists, creates, restores, and deletes an instance's backups.
export default function Backups({ token, instance }: { token: string; instance: Instance }) {
  const [backups, setBackups] = useState<BackupInfo[]>([]);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const base = `/instances/${instance.id}/backups`;

  const load = useCallback(async () => {
    try {
      setBackups(await api<BackupInfo[]>(token, "GET", base));
    } catch (err) {
      setError((err as Error).message);
    }
  }, [token, base]);

  useEffect(() => {
    load();
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  }, [load]);

  async function run(fn: () => Promise<unknown>, done: string) {
    setError("");
    setMessage("");
    try {
      await fn();
      setMessage(done);
      load();
    } catch (err) {
      setError((err as Error).message);
    }
  }

  const running = instance.state === "running" || instance.state === "starting";
  return (
    <div className="backups">
      <div className="row">
        <span className="muted">
          A backup saves the whole server folder. Running servers save their world first.
        </span>
        <button onClick={() => run(() => api(token, "POST", base), "Backup started. Watch the console for progress.")}>
          Back up now
        </button>
      </div>
      {backups.length === 0 ? (
        <p className="muted">No backups yet.</p>
      ) : (
        <table>
          <tbody>
            {backups.map((b) => (
              <tr key={b.name}>
                <td>{new Date(b.createdAt * 1000).toLocaleString()}</td>
                <td className="muted">{formatSize(b.size)}</td>
                <td className="actions">
                  <button
                    disabled={running}
                    title={running ? "Stop the server to restore" : ""}
                    onClick={() => {
                      if (confirm("Replace the server's files with this backup? Anything changed since will be lost.")) {
                        run(() => api(token, "POST", `${base}/${b.name}/restore`), "Restore started. Watch the console for progress.");
                      }
                    }}
                  >
                    Restore
                  </button>
                  <button
                    className="danger"
                    onClick={() => {
                      if (confirm("Delete this backup?")) run(() => api(token, "DELETE", `${base}/${b.name}`), "Backup deleted.");
                    }}
                  >
                    Delete
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {message && <p className="muted">{message}</p>}
      {error && <p className="error">{error}</p>}
    </div>
  );
}
