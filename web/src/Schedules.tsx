import { FormEvent, useCallback, useEffect, useState } from "react";
import { Instance, Schedule, api } from "./api";

const days = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
const actionLabels: Record<Schedule["action"], string> = {
  restart: "Restart",
  stop: "Stop",
  start: "Start",
  backup: "Back up",
  command: "Run a command",
};

function clock(h: number, m: number) {
  const d = new Date(2000, 0, 1, h, m);
  return d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
}

// describeCron turns the cron expressions the form makes back into words.
export function describeCron(cron: string): string {
  const [min, hour, dom, month, dow] = cron.split(" ");
  const num = (v: string) => /^\d+$/.test(v);
  if (dom === "*" && month === "*") {
    if (num(min) && num(hour)) {
      const at = clock(+hour, +min);
      if (dow === "*") return `Every day at ${at}`;
      if (/^[0-7](,[0-7])*$/.test(dow))
        return `${dow
          .split(",")
          .map((d) => days[+d % 7])
          .join(", ")} at ${at}`;
      if (dow === "1-5") return `Weekdays at ${at}`;
    }
    const every = /^\*\/(\d+)$/.exec(hour);
    if (num(min) && every && dow === "*") return `Every ${every[1]} hours${+min ? ` at ${min} past` : ""}`;
    if (min === "0" && hour === "*" && dow === "*") return "Every hour";
  }
  return `Cron: ${cron}`;
}

type When = { kind: "daily" | "hours" | "weekly" | "custom"; time: string; hours: number; days: number[]; cron: string };

function toCron(w: When): string {
  const [h, m] = w.time.split(":").map(Number);
  switch (w.kind) {
    case "daily":
      return `${m} ${h} * * *`;
    case "hours":
      return `0 */${w.hours} * * *`;
    case "weekly":
      return `${m} ${h} * * ${(w.days.length ? w.days : [0]).sort().join(",")}`;
    default:
      return w.cron;
  }
}

function fromCron(cron: string): When {
  const base: When = { kind: "custom", time: "04:00", hours: 6, days: [1], cron };
  const [min, hour, dom, month, dow] = cron.split(" ");
  if (dom !== "*" || month !== "*") return base;
  if (/^\d+$/.test(min) && /^\d+$/.test(hour)) {
    const time = `${hour.padStart(2, "0")}:${min.padStart(2, "0")}`;
    if (dow === "*") return { ...base, kind: "daily", time };
    if (/^[0-6](,[0-6])*$/.test(dow)) return { ...base, kind: "weekly", time, days: dow.split(",").map(Number) };
  }
  const every = /^\*\/(\d+)$/.exec(hour ?? "");
  if (min === "0" && every && dow === "*") return { ...base, kind: "hours", hours: +every[1] };
  return base;
}

const blank: Partial<Schedule> = { name: "", action: "restart", cron: "0 4 * * *", warn: true, enabled: true, keep: 7, command: "" };

export default function Schedules({ token, instance, timeZone }: { token: string; instance: Instance; timeZone: string }) {
  const [list, setList] = useState<Schedule[]>([]);
  const [editing, setEditing] = useState<Partial<Schedule> | null>(null);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const base = `/instances/${instance.id}/schedules`;

  const load = useCallback(() => api<Schedule[]>(token, "GET", base).then(setList, (e) => setError(e.message)), [token, base]);
  useEffect(() => {
    load();
    const id = setInterval(load, 15000);
    return () => clearInterval(id);
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

  async function runNow(sc: Schedule) {
    if (
      sc.action !== "backup" &&
      !confirm(`${actionLabels[sc.action]} ${instance.name} now? Players aren't warned when you run it by hand.`)
    )
      return;
    await run(async () => {
      const res = await api<{ result: string }>(token, "POST", `${base}/${sc.id}/run`);
      setMessage(`${sc.name}: ${res.result}`);
    });
  }

  return (
    <div className="schedules">
      <div className="row">
        <span className="muted">Times are in the panel's time zone: {timeZone || "…"}.</span>
        {!editing && (
          <button className="primary" onClick={() => setEditing({ ...blank })}>
            Add schedule
          </button>
        )}
      </div>
      {editing && (
        <ScheduleForm
          value={editing}
          onCancel={() => setEditing(null)}
          onSave={async (sc) => {
            const ok = await run(
              () => (sc.id ? api(token, "PUT", `${base}/${sc.id}`, sc) : api(token, "POST", base, sc)),
              sc.id ? "Schedule saved." : "Schedule added.",
            );
            if (ok) setEditing(null);
          }}
        />
      )}
      {list.length === 0 && !editing && <p className="muted">No schedules yet. A nightly restart and a daily backup are a good start.</p>}
      {list.length > 0 && (
        <table>
          <tbody>
            {list.map((sc) => (
              <tr key={sc.id} className={sc.enabled ? "" : "off"}>
                <td>
                  <strong>{sc.name}</strong>
                  <div className="muted">
                    {actionLabels[sc.action]}
                    {sc.action === "command" && `: ${sc.command}`}
                    {sc.action === "backup" && (sc.keep ? `, keep ${sc.keep}` : ", keep all")}
                    {sc.warn && ", warns players"}
                  </div>
                </td>
                <td>
                  {describeCron(sc.cron)}
                  <div className="muted">
                    {sc.enabled
                      ? sc.nextRun
                        ? `Next ${new Date(sc.nextRun).toLocaleString([], { weekday: "short", hour: "numeric", minute: "2-digit" })}`
                        : ""
                      : "Paused"}
                  </div>
                </td>
                <td className="muted">
                  {sc.lastRun
                    ? `Last ${new Date(sc.lastRun).toLocaleString([], { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" })}`
                    : "Not run yet"}
                  {sc.lastResult && <div className={sc.lastResult.startsWith("failed") ? "error" : ""}>{sc.lastResult}</div>}
                </td>
                <td className="actions">
                  <button onClick={() => run(() => api(token, "PUT", `${base}/${sc.id}`, { ...sc, enabled: !sc.enabled }))}>
                    {sc.enabled ? "Pause" : "Resume"}
                  </button>
                  <button onClick={() => runNow(sc)}>Run now</button>
                  <button onClick={() => setEditing(sc)}>Edit</button>
                  <button
                    className="danger"
                    onClick={() => confirm(`Remove "${sc.name}"?`) && run(() => api(token, "DELETE", `${base}/${sc.id}`))}
                  >
                    Remove
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

function ScheduleForm({
  value,
  onSave,
  onCancel,
}: {
  value: Partial<Schedule>;
  onSave: (sc: Partial<Schedule>) => void;
  onCancel: () => void;
}) {
  const [sc, setSc] = useState(value);
  const [when, setWhen] = useState<When>(() => fromCron(value.cron ?? "0 4 * * *"));
  const set = (patch: Partial<Schedule>) => setSc({ ...sc, ...patch });
  const setW = (patch: Partial<When>) => setWhen({ ...when, ...patch });
  const cron = toCron(when);

  function submit(e: FormEvent) {
    e.preventDefault();
    onSave({ ...sc, cron });
  }

  return (
    <form className="create schedule-form" onSubmit={submit}>
      <label>
        Do this
        <select value={sc.action} onChange={(e) => set({ action: e.target.value as Schedule["action"] })}>
          {Object.entries(actionLabels).map(([k, v]) => (
            <option key={k} value={k}>
              {v}
            </option>
          ))}
        </select>
      </label>
      {sc.action === "command" && (
        <label>
          Command
          <input value={sc.command} onChange={(e) => set({ command: e.target.value })} placeholder="say Remember to vote!" required />
        </label>
      )}
      <label>
        When
        <select value={when.kind} onChange={(e) => setW({ kind: e.target.value as When["kind"], cron })}>
          <option value="daily">Every day</option>
          <option value="weekly">On certain days</option>
          <option value="hours">Every few hours</option>
          <option value="custom">Custom (cron)</option>
        </select>
      </label>
      {(when.kind === "daily" || when.kind === "weekly") && (
        <label>
          At
          <input type="time" value={when.time} onChange={(e) => setW({ time: e.target.value })} required />
        </label>
      )}
      {when.kind === "weekly" && (
        <div className="checks">
          {days.map((d, i) => (
            <label key={d}>
              <input
                type="checkbox"
                checked={when.days.includes(i)}
                onChange={(e) => setW({ days: e.target.checked ? [...when.days, i] : when.days.filter((x) => x !== i) })}
              />
              {d}
            </label>
          ))}
        </div>
      )}
      {when.kind === "hours" && (
        <label>
          Every
          <select value={when.hours} onChange={(e) => setW({ hours: +e.target.value })}>
            {[1, 2, 3, 4, 6, 8, 12].map((h) => (
              <option key={h} value={h}>
                {h} {h === 1 ? "hour" : "hours"}
              </option>
            ))}
          </select>
        </label>
      )}
      {when.kind === "custom" && (
        <label>
          Cron expression (minute hour day month weekday)
          <input value={when.cron} onChange={(e) => setW({ cron: e.target.value })} placeholder="0 4 * * *" required />
        </label>
      )}
      <p className="muted">{describeCron(cron)}</p>
      {(sc.action === "restart" || sc.action === "stop") && (
        <label className="check">
          <input type="checkbox" checked={!!sc.warn} onChange={(e) => set({ warn: e.target.checked })} />
          Warn players in game 5 minutes, 1 minute and 10 seconds before
        </label>
      )}
      {sc.action === "backup" && (
        <label>
          Backups to keep (older ones are deleted; 0 keeps all)
          <input type="number" min={0} max={1000} value={sc.keep ?? 0} onChange={(e) => set({ keep: +e.target.value })} />
        </label>
      )}
      <label>
        Name (optional)
        <input value={sc.name} onChange={(e) => set({ name: e.target.value })} placeholder={actionLabels[sc.action ?? "restart"]} />
      </label>
      <label className="check">
        <input type="checkbox" checked={!!sc.enabled} onChange={(e) => set({ enabled: e.target.checked })} />
        Enabled
      </label>
      <div className="actions">
        <button type="submit" className="primary">
          {sc.id ? "Save schedule" : "Add schedule"}
        </button>
        <button type="button" onClick={onCancel}>
          Cancel
        </button>
      </div>
    </form>
  );
}
