import { mountPath } from "./mount";

export type TraceState =
  | "idle"
  | "starting"
  | "running"
  | "stopping"
  | "stopped"
  | "error";

export type SessionInfo = {
  id: string;
  state: string;
  error?: string;
};

export type TraceRow = {
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

export type TraceSnapshot = { session: SessionInfo; rows: TraceRow[] };

export type TraceFilters = {
  database: string;
  users: string;
  apps: string;
  hosts: string;
  events: string;
  minDuration: string;
  maxDuration: string;
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

export function emptyTraceFilters(database: string): TraceFilters {
  return {
    database,
    users: "",
    apps: "",
    hosts: "",
    events: "",
    minDuration: "",
    maxDuration: "5m",
  };
}

async function requestJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, init);
  if (!response.ok)
    throw new Error(
      (await response.text()).trim() || `Request failed: ${response.status}`,
    );
  return response.json() as Promise<T>;
}

export async function stopSession(sessionID: string): Promise<void> {
  const response = await fetch(
    `${mountPath}/api/v1/sessions/${encodeURIComponent(sessionID)}/stop`,
    { method: "POST" },
  );
  if (!response.ok)
    throw new Error(
      (await response.text()).trim() || `Stop failed: ${response.status}`,
    );
}

function list(value: string): string[] | undefined {
  const values = value
    .split(",")
    .map((item) => item.trim())
    .filter(Boolean);
  return values.length ? values : undefined;
}

function startBody(filters: TraceFilters) {
  const { database, minDuration, maxDuration } = filters;
  return {
    ...(database.trim() ? { database: database.trim() } : {}),
    ...(list(filters.users) ? { users: list(filters.users) } : {}),
    ...(list(filters.apps) ? { apps: list(filters.apps) } : {}),
    ...(list(filters.hosts) ? { hosts: list(filters.hosts) } : {}),
    ...(list(filters.events) ? { events: list(filters.events) } : {}),
    ...(minDuration.trim() ? { minDuration: minDuration.trim() } : {}),
    duration: maxDuration.trim() || "5m",
  };
}

export function createTraceSession(
  connectionID: string,
  filters: TraceFilters,
): Promise<SessionInfo> {
  return requestJSON<SessionInfo>(
    `${mountPath}/api/v1/connection/${encodeURIComponent(connectionID)}/trace/sessions`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(startBody(filters)),
    },
  );
}

/** Reads a trace session's state together with the events captured so far. */
export async function fetchTraceSnapshot(
  sessionID: string,
): Promise<TraceSnapshot> {
  const base = `${mountPath}/api/v1/sessions/${encodeURIComponent(sessionID)}`;
  const [session, rows] = await Promise.all([
    requestJSON<SessionInfo>(base),
    requestJSON<TraceRow[]>(`${base}/result`),
  ]);
  return { session, rows };
}

export function isTerminalSession(session: SessionInfo): boolean {
  return TERMINAL_STATES.has(session.state.toLowerCase());
}

export function errorMessage(reason: unknown, fallback: string): string {
  return reason instanceof Error ? reason.message : fallback;
}

export function formatDuration(value?: number): string {
  if (value == null) return "—";
  if (value < 1_000_000) return `${(value / 1_000).toFixed(1)} µs`;
  if (value < 1_000_000_000) return `${(value / 1_000_000).toFixed(1)} ms`;
  return `${(value / 1_000_000_000).toFixed(2)} s`;
}

/**
 * Keys rows by their content plus the occurrence number of identical rows,
 * so a re-polled result keeps each row's identity without relying on position.
 */
export function keyTraceRows(
  rows: TraceRow[],
): { key: string; row: TraceRow }[] {
  const seen = new Map<string, number>();
  return rows.map((row) => {
    const signature = [
      row.timestamp,
      row.name,
      row.statement,
      row.error_message,
      row.duration,
      row.database_name,
      row.username,
    ].join("|");
    const occurrence = seen.get(signature) ?? 0;
    seen.set(signature, occurrence + 1);
    return { key: `${signature}#${occurrence}`, row };
  });
}
