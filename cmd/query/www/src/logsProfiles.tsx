import { LogsTable, type ResultRenderContext } from "@flanksource/clicky-ui";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";

// slugify mirrors cmd/query/profilestore.go slugify so a profile name maps to its
// dynamic-entity name ("profile-" + slug). The entity name (not the pluralized
// surface key) is what appears in the list-operation request URL. Keep in sync.
function slugify(name: string): string {
  let out = "";
  for (const ch of name.trim().toLowerCase()) {
    if ((ch >= "a" && ch <= "z") || (ch >= "0" && ch <= "9")) out += ch;
    else if (ch === " " || ch === "-" || ch === "_" || ch === "/" || ch === ".") out += "-";
  }
  return out.replace(/^-+|-+$/g, "");
}

// asArray pulls the row/item array out of whatever envelope an endpoint returns
// (a bare array, or a { rows } / { data } / { items } wrapper).
function asArray(payload: unknown): Record<string, unknown>[] {
  if (Array.isArray(payload)) return payload as Record<string, unknown>[];
  if (payload && typeof payload === "object") {
    for (const key of ["rows", "data", "items"]) {
      const v = (payload as Record<string, unknown>)[key];
      if (Array.isArray(v)) return v as Record<string, unknown>[];
    }
  }
  return [];
}

// entitySegment returns the trailing path segment of a request URL, e.g.
// "/api/v1/profiles/profile-logs-http-demo?limit=50" -> "profile-logs-http-demo".
function entitySegment(url: string): string {
  const path = url.split("?")[0] ?? "";
  return path.split("/").filter(Boolean).pop() ?? "";
}

// useLogsEntityNames fetches the profile definitions and returns the set of
// dynamic-entity names whose profile declares `render: logs`. Failure yields an
// empty set, so logs profiles simply fall back to the default table.
export function useLogsEntityNames(): Set<string> {
  const { data } = useQuery({
    queryKey: ["logs-entity-names"],
    queryFn: async () => {
      const res = await fetch("/api/v1/profiles", { headers: { Accept: "application/json" } });
      if (!res.ok) return [] as Record<string, unknown>[];
      return asArray(await res.json());
    },
  });
  const names = new Set<string>();
  for (const p of data ?? []) {
    if (p.render === "logs") {
      const name = typeof p.profile === "string" ? p.profile : typeof p.name === "string" ? p.name : "";
      if (name) names.add("profile-" + slugify(name));
    }
  }
  return names;
}

// LogsResult re-fetches the profile's rows as plain JSON from the same request URL
// that produced the result (so the active server-side filters are preserved) and
// renders them through clicky-ui's canonical LogsTable. Client-side filtering and
// sorting are disabled — filtering stays server-side via the profile's params.
function LogsResult({ requestUrl }: { requestUrl: string }): ReactNode {
  const { data, isLoading, isError } = useQuery({
    queryKey: ["logs-rows", requestUrl],
    queryFn: async () => {
      const res = await fetch(requestUrl, { headers: { Accept: "application/json" } });
      if (!res.ok) throw new Error(`failed to load rows: ${res.status}`);
      return asArray(await res.json());
    },
  });

  if (isLoading) return <div className="text-sm text-muted-foreground">Loading logs…</div>;
  if (isError || !data) return null;

  return <LogsTable logs={data} autoFilter={false} fullscreenTitle="Logs" />;
}

// logsResultRenderer is the EntityExplorerApp result override: when the result's
// request URL targets a logs entity it renders LogsResult, otherwise it returns
// the default view unchanged.
export function logsResultRenderer(
  logsEntityNames: Set<string>,
): (ctx: ResultRenderContext) => ReactNode {
  return ({ response, defaultView }) => {
    const requestUrl = response?.requestUrl;
    if (!requestUrl || !logsEntityNames.has(entitySegment(requestUrl))) return defaultView;
    return <LogsResult requestUrl={requestUrl} />;
  };
}
