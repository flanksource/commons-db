import { browserBaseUrl, useInspection } from "@flanksource/clicky-ui/profiles";
import { useCallback, useEffect, useRef, useState } from "react";

type TraceState =
  | "idle"
  | "starting"
  | "running"
  | "stopping"
  | "stopped"
  | "error";

type SessionInfo = {
  id: string;
  state: string;
  error?: string;
};

type TraceRow = {
  timestamp?: string;
  statement?: string;
  name?: string;
  duration?: number;
  cpu_time?: number;
  logical_reads?: number;
  writes?: number;
  row_count?: number;
  database_name?: string;
  username?: string;
  client_app_name?: string;
  error_message?: string;
};

const TERMINAL_STATES = new Set([
  "completed",
  "complete",
  "done",
  "stopped",
  "interrupted",
  "failed",
  "error",
  "cancelled",
  "canceled",
]);

async function requestJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, init);
  if (!response.ok)
    throw new Error(
      (await response.text()).trim() || `Request failed: ${response.status}`,
    );
  return response.json() as Promise<T>;
}

function list(value: string): string[] | undefined {
  const values = value
    .split(",")
    .map((item) => item.trim())
    .filter(Boolean);
  return values.length ? values : undefined;
}

function duration(value?: number): string {
  if (value == null) return "—";
  if (value < 1_000_000) return `${(value / 1_000).toFixed(1)} µs`;
  if (value < 1_000_000_000) return `${(value / 1_000_000).toFixed(1)} ms`;
  return `${(value / 1_000_000_000).toFixed(2)} s`;
}

/** Owns one SQL Server trace session and tears it down with its connection view. */
export function ConnectionTrace({
  id,
  defaultDatabase,
}: {
  id: string;
  defaultDatabase?: string;
}) {
  const [database, setDatabase] = useState(defaultDatabase ?? "");
  const inspection = useInspection({
    cacheKey: "connection-browser-inspection",
    id,
    baseUrl: browserBaseUrl(id),
    enabled: true,
    database: "",
    fallbackDatabase: defaultDatabase ?? "",
    target: "",
  });
  const [users, setUsers] = useState("");
  const [apps, setApps] = useState("");
  const [hosts, setHosts] = useState("");
  const [events, setEvents] = useState("");
  const [minDuration, setMinDuration] = useState("");
  const [maxDuration, setMaxDuration] = useState("5m");
  const [state, setState] = useState<TraceState>("idle");
  const [session, setSession] = useState<SessionInfo>();
  const [rows, setRows] = useState<TraceRow[]>([]);
  const [error, setError] = useState("");
  const sessionRef = useRef<string>();
  const startPending = useRef(false);
  const stopRequested = useRef(false);
  const mounted = useRef(true);

  const stop = useCallback(async () => {
    stopRequested.current = true;
    const sessionID = sessionRef.current;
    if (!sessionID) {
      if (startPending.current) setState("stopping");
      return;
    }
    setState("stopping");
    try {
      const response = await fetch(
        `/api/v1/sessions/${encodeURIComponent(sessionID)}`,
        { method: "DELETE" },
      );
      if (!response.ok)
        throw new Error(
          (await response.text()).trim() || `Stop failed: ${response.status}`,
        );
      if (mounted.current) setError("");
    } catch (reason) {
      if (mounted.current) {
        setError(
          reason instanceof Error ? reason.message : "Failed to stop trace",
        );
        setState("error");
      }
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      stopRequested.current = true;
      const sessionID = sessionRef.current;
      if (sessionID) {
        void fetch(`/api/v1/sessions/${encodeURIComponent(sessionID)}`, {
          method: "DELETE",
        }).catch(() => undefined);
      }
    };
  }, []);

  useEffect(() => {
    if (!session?.id || !["running", "stopping"].includes(state)) return;
    const sessionID = session.id;
    let canceled = false;
    let timer: number | undefined;

    const poll = async () => {
      try {
        const next = await requestJSON<SessionInfo>(
          `/api/v1/sessions/${encodeURIComponent(sessionID)}`,
        );
        if (canceled || sessionRef.current !== sessionID) return;
        setSession(next);

        const terminal = TERMINAL_STATES.has(next.state.toLowerCase());
        const result = await requestJSON<TraceRow[]>(
          `/api/v1/sessions/${encodeURIComponent(sessionID)}/result`,
        );
        if (canceled || sessionRef.current !== sessionID) return;
        setRows(result);

        if (terminal) {
          sessionRef.current = undefined;
          if (
            next.error ||
            ["failed", "error"].includes(next.state.toLowerCase())
          ) {
            setError(next.error || `Trace ${next.state}`);
            setState("error");
          } else {
            setState("stopped");
          }
          return;
        }
      } catch (reason) {
        if (!canceled && sessionRef.current === sessionID) {
          setError(
            reason instanceof Error
              ? reason.message
              : "Failed to read trace session",
          );
          setState("error");
        }
        return;
      }

      if (!canceled) timer = window.setTimeout(() => void poll(), 1_000);
    };

    void poll();
    return () => {
      canceled = true;
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, [session?.id, state]);

  const start = async () => {
    if (startPending.current || sessionRef.current) return;
    startPending.current = true;
    stopRequested.current = false;
    setState("starting");
    setError("");
    setRows([]);
    try {
      const created = await requestJSON<SessionInfo>(
        `/api/v1/connection/${encodeURIComponent(id)}/trace/sessions`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            ...(database.trim() ? { database: database.trim() } : {}),
            ...(list(users) ? { users: list(users) } : {}),
            ...(list(apps) ? { apps: list(apps) } : {}),
            ...(list(hosts) ? { hosts: list(hosts) } : {}),
            ...(list(events) ? { events: list(events) } : {}),
            ...(minDuration.trim() ? { minDuration: minDuration.trim() } : {}),
            duration: maxDuration.trim() || "5m",
          }),
        },
      );
      sessionRef.current = created.id;
      if (!mounted.current) {
        await fetch(`/api/v1/sessions/${encodeURIComponent(created.id)}`, {
          method: "DELETE",
        }).catch(() => undefined);
        return;
      }
      setSession(created);
      if (stopRequested.current) {
        setState("stopping");
        const response = await fetch(
          `/api/v1/sessions/${encodeURIComponent(created.id)}`,
          { method: "DELETE" },
        );
        if (!response.ok)
          throw new Error(
            (await response.text()).trim() || `Stop failed: ${response.status}`,
          );
      } else {
        setState("running");
      }
    } catch (reason) {
      if (mounted.current) {
        setError(
          reason instanceof Error ? reason.message : "Failed to start trace",
        );
        setState("error");
      }
    } finally {
      startPending.current = false;
    }
  };

  const busy =
    state === "starting" || state === "running" || state === "stopping";
  const canStart = !busy && !sessionRef.current;
  const canStop = state === "starting" || Boolean(sessionRef.current);
  const inputClass = "h-8 rounded-md border bg-background px-2 text-sm";
  return (
    <div className="flex min-h-[32rem] flex-col gap-3 rounded-xl border bg-card p-4">
      <div className="flex flex-wrap items-end gap-3">
        <label className="flex min-w-40 flex-1 flex-col gap-1 text-xs text-muted-foreground">
          Database
          <select
            className={inputClass}
            value={database}
            onChange={(e) => setDatabase(e.target.value)}
            disabled={busy}
          >
            <option value="">Connection default</option>
            {database && !inspection.databases.includes(database) ? (
              <option value={database}>{database}</option>
            ) : null}
            {inspection.databases.map((name) => (
              <option key={name} value={name}>{name}</option>
            ))}
          </select>
          {inspection.loading ? <span>Loading databases…</span> : null}
          {inspection.error ? (
            <span role="alert">Could not load databases. Connection default is still available.</span>
          ) : null}
        </label>
        <label className="flex min-w-40 flex-1 flex-col gap-1 text-xs text-muted-foreground">
          Users
          <input
            className={inputClass}
            value={users}
            onChange={(e) => setUsers(e.target.value)}
            disabled={busy}
            placeholder="Comma-separated"
          />
        </label>
        <label className="flex min-w-40 flex-1 flex-col gap-1 text-xs text-muted-foreground">
          Applications
          <input
            className={inputClass}
            value={apps}
            onChange={(e) => setApps(e.target.value)}
            disabled={busy}
            placeholder="Comma-separated"
          />
        </label>
        <label className="flex min-w-40 flex-1 flex-col gap-1 text-xs text-muted-foreground">
          Hosts
          <input
            className={inputClass}
            value={hosts}
            onChange={(e) => setHosts(e.target.value)}
            disabled={busy}
            placeholder="Comma-separated"
          />
        </label>
        <label className="flex min-w-40 flex-1 flex-col gap-1 text-xs text-muted-foreground">
          Events
          <input
            className={inputClass}
            value={events}
            onChange={(e) => setEvents(e.target.value)}
            disabled={busy}
            placeholder="Comma-separated"
          />
        </label>
        <label className="flex w-32 flex-col gap-1 text-xs text-muted-foreground">
          Min duration
          <input
            className={inputClass}
            value={minDuration}
            onChange={(e) => setMinDuration(e.target.value)}
            disabled={busy}
            placeholder="e.g. 100ms"
          />
        </label>
        <label className="flex w-32 flex-col gap-1 text-xs text-muted-foreground">
          Max duration
          <input
            className={inputClass}
            value={maxDuration}
            onChange={(e) => setMaxDuration(e.target.value)}
            disabled={busy}
          />
        </label>
        <button
          className="h-8 rounded-md bg-primary px-4 text-sm text-primary-foreground disabled:opacity-50"
          disabled={!canStart}
          onClick={() => void start()}
        >
          Start
        </button>
        <button
          className="h-8 rounded-md border px-4 text-sm disabled:opacity-50"
          disabled={!canStop}
          onClick={() => void stop()}
        >
          Stop
        </button>
      </div>
      <div className="flex items-center gap-2 text-sm">
        <span className="font-medium capitalize">{state}</span>
        {session?.id ? (
          <span className="text-muted-foreground">Session {session.id}</span>
        ) : null}
      </div>
      {error ? (
        <div
          role="alert"
          className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm text-destructive"
        >
          {error}
        </div>
      ) : null}
      <div className="min-h-0 flex-1 overflow-auto rounded-md border">
        <table className="w-full min-w-[1100px] text-left text-xs">
          <thead className="sticky top-0 bg-muted">
            <tr>
              {[
                "Time",
                "Event",
                "Statement",
                "Duration",
                "CPU",
                "Reads",
                "Writes",
                "Rows",
                "Database",
                "User",
                "Application",
              ].map((heading) => (
                <th key={heading} className="px-3 py-2 font-medium">
                  {heading}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.length ? (
              rows.map((row, index) => (
                <tr
                  key={`${row.timestamp ?? "row"}-${index}`}
                  className={`border-t align-top ${row.error_message ? "bg-amber-500/10" : ""}`}
                >
                  <td className="whitespace-nowrap px-3 py-2">
                    {row.timestamp ?? "—"}
                  </td>
                  <td className="px-3 py-2">
                    {row.name ?? (row.error_message ? "Warning" : "—")}
                  </td>
                  <td className="max-w-xl whitespace-pre-wrap px-3 py-2 font-mono">
                    {row.error_message || row.statement || "—"}
                  </td>
                  <td className="whitespace-nowrap px-3 py-2">
                    {duration(row.duration)}
                  </td>
                  <td className="whitespace-nowrap px-3 py-2">
                    {duration(row.cpu_time)}
                  </td>
                  <td className="px-3 py-2">{row.logical_reads ?? "—"}</td>
                  <td className="px-3 py-2">{row.writes ?? "—"}</td>
                  <td className="px-3 py-2">{row.row_count ?? "—"}</td>
                  <td className="px-3 py-2">{row.database_name ?? "—"}</td>
                  <td className="px-3 py-2">{row.username ?? "—"}</td>
                  <td className="px-3 py-2">{row.client_app_name ?? "—"}</td>
                </tr>
              ))
            ) : (
              <tr>
                <td
                  colSpan={11}
                  className="px-3 py-12 text-center text-muted-foreground"
                >
                  {state === "idle"
                    ? "Configure filters and start a trace."
                    : "No trace events yet."}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}
