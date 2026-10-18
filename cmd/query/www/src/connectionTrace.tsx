import { browserBaseUrl, useInspection } from "@flanksource/clicky-ui/profiles";
import { useState } from "react";
import { emptyTraceFilters, type TraceFilters } from "./connectionTraceApi";
import { TraceFilterFields } from "./connectionTraceFilters";
import { TraceTable } from "./connectionTraceTable";
import { useTraceSession } from "./useTraceSession";

/** Owns one SQL Server trace session and tears it down with its connection view. */
export function ConnectionTrace({
  id,
  defaultDatabase,
}: {
  id: string;
  defaultDatabase?: string;
}) {
  const [filters, setFilters] = useState<TraceFilters>(() =>
    emptyTraceFilters(defaultDatabase ?? ""),
  );
  const inspection = useInspection({
    cacheKey: "connection-browser-inspection",
    id,
    baseUrl: browserBaseUrl(id),
    enabled: true,
    database: "",
    fallbackDatabase: defaultDatabase ?? "",
    target: "",
  });
  const trace = useTraceSession(id);
  const { state, error, sessionID, hasActiveSession } = trace;
  const busy =
    state === "starting" || state === "running" || state === "stopping";
  const canStart = !busy && !hasActiveSession;
  const canStop = state === "starting" || hasActiveSession;
  return (
    <div className="flex min-h-[32rem] flex-col gap-3 rounded-xl border bg-card p-4">
      <div className="flex flex-wrap items-end gap-3">
        <TraceFilterFields
          filters={filters}
          onChange={(change) => setFilters((current) => ({ ...current, ...change }))}
          disabled={busy}
          inspection={inspection}
        />
        <button
          className="h-8 rounded-md bg-primary px-4 text-sm text-primary-foreground disabled:opacity-50"
          disabled={!canStart}
          onClick={() => void trace.start(filters)}
        >
          Start
        </button>
        <button
          className="h-8 rounded-md border px-4 text-sm disabled:opacity-50"
          disabled={!canStop}
          onClick={() => void trace.stop()}
        >
          Stop
        </button>
        {state === "error" && hasActiveSession ? (
          <button
            className="h-8 rounded-md border px-4 text-sm"
            onClick={trace.retryPolling}
          >
            Retry polling
          </button>
        ) : null}
      </div>
      <div className="flex items-center gap-2 text-sm">
        <span className="font-medium capitalize">{state}</span>
        {sessionID ? (
          <span className="text-muted-foreground">Session {sessionID}</span>
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
      <TraceTable rows={trace.rows} idle={state === "idle"} />
    </div>
  );
}
