import {
  LogsTable,
  type ClickyDownloadOptions,
  type DataTablePagination,
  type OperationResultFilterConfig,
  type ResultRenderContext,
} from "@flanksource/clicky-ui";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useProfiles } from "./profilesQuery";

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

// useLogsEntityNames returns the set of dynamic-entity names whose profile
// declares `render: logs`. It reads the shared profile query, so a rename does
// not leave this view resolving against a stale profile list.
export function useLogsEntityNames(): Set<string> {
  const { data } = useProfiles();
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
function LogsResult({
  requestUrl,
  filterConfig,
  columnFilterKeys,
  pagination,
  download,
}: {
  requestUrl: string;
  filterConfig?: OperationResultFilterConfig;
  columnFilterKeys: Record<string, string>;
  pagination?: DataTablePagination;
  download?: ClickyDownloadOptions;
}): ReactNode {
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

  return (
    <LogsTable
      logs={data}
      autoFilter={false}
      fullscreenTitle="Logs"
      columnFilterKeys={columnFilterKeys}
      cellFilters={filterConfig?.cellFilters}
      onCellFilterChange={filterConfig?.onCellFilterChange}
      // A logs profile is paged by the server exactly like every other one, so
      // it gets the same pager and download menu. Dropping them here is what
      // made the first page look like the whole log.
      {...(pagination ? { pagination } : {})}
      {...(download ? { download } : {})}
    />
  );
}

export function logsColumnFilterKeys(payload: unknown): Record<string, string> {
  if (!payload || typeof payload !== "object") return {};
  const node = (payload as { node?: unknown }).node;
  if (!node || typeof node !== "object") return {};
  const table = node as { kind?: unknown; columns?: unknown };
  if (table.kind !== "table" || !Array.isArray(table.columns)) return {};

  return Object.fromEntries(
    table.columns.flatMap((value) => {
      if (!value || typeof value !== "object") return [];
      const column = value as { name?: unknown; filterKey?: unknown };
      return typeof column.name === "string" && typeof column.filterKey === "string"
        ? [[column.name, column.filterKey]]
        : [];
    }),
  );
}

// logsResultRenderer is the EntityExplorerApp result override: when the result's
// request URL targets a logs entity it renders LogsResult, otherwise it returns
// the default view unchanged.
export function logsResultRenderer(
  logsEntityNames: Set<string>,
): (ctx: ResultRenderContext) => ReactNode {
  return ({ response, defaultView, filterConfig, pagination, download }) => {
    const requestUrl = response?.requestUrl;
    if (!requestUrl || !logsEntityNames.has(entitySegment(requestUrl))) return defaultView;
    return (
      <LogsResult
        requestUrl={requestUrl}
        filterConfig={filterConfig}
        columnFilterKeys={logsColumnFilterKeys(response.parsed)}
        {...(pagination ? { pagination } : {})}
        {...(download ? { download } : {})}
      />
    );
  };
}
