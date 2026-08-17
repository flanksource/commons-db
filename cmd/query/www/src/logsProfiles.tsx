import {
  LogsTable,
  type ClickyNode,
  type ResultRenderContext,
} from "@flanksource/clicky-ui";
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

export function logsColumnFilterKeys(payload: unknown): Record<string, string> {
  const table = clickyTable(payload);
  if (!table || !Array.isArray(table.columns)) return {};

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

export function clickyLogsRows(payload: unknown): Record<string, unknown>[] | undefined {
  const table = clickyTable(payload);
  if (!table) return undefined;
  return (table.rows ?? []).map((row) =>
    Object.fromEntries(
      Object.entries(row.cells).map(([key, value]) => [key, clickyNodeValue(value)]),
    ),
  );
}

function clickyTable(payload: unknown): ClickyNode | undefined {
  if (!payload || typeof payload !== "object") return undefined;
  const candidate = payload as Partial<ClickyNode> & { node?: unknown };
  const node = candidate.node && typeof candidate.node === "object"
    ? (candidate.node as ClickyNode)
    : (candidate as ClickyNode);
  if (node.kind === "table") return node;
  for (const child of [
    ...(node.children ?? []),
    ...(node.items ?? []),
    ...(node.fields ?? []).map((field) => field.value),
  ]) {
    const table = clickyTable(child);
    if (table) return table;
  }
  return undefined;
}

function clickyNodeValue(node: ClickyNode): unknown {
  if (node.kind === "map") {
    return Object.fromEntries(
      (node.fields ?? []).map((field) => [field.name, clickyNodeValue(field.value)]),
    );
  }
  if (node.kind === "list") return (node.items ?? []).map(clickyNodeValue);
  if (node.plain !== undefined) return node.plain;
  if (node.text !== undefined) return node.text;
  if (node.source !== undefined) return node.source;
  if (node.filterValue !== undefined) return node.filterValue;
  if (node.children) return node.children.map(clickyNodeValue).join("");
  return "";
}

// logsResultRenderer is the EntityExplorerApp result override: when the result's
// request URL targets a logs entity it renders its current page through
// LogsTable, otherwise it returns the default view unchanged.
export function logsResultRenderer(
  logsEntityNames: Set<string>,
): (ctx: ResultRenderContext) => ReactNode {
  return ({ response, loading, defaultView, filterConfig, pagination, download }) => {
    const requestUrl = response?.requestUrl;
    if (!requestUrl || !logsEntityNames.has(entitySegment(requestUrl))) return defaultView;
    const logs = clickyLogsRows(response.parsed);
    if (!logs) return defaultView;
    return (
      <LogsTable
        logs={logs}
        loading={loading}
        autoFilter={false}
        fullscreenTitle="Logs"
        columnFilterKeys={logsColumnFilterKeys(response.parsed)}
        cellFilters={filterConfig?.cellFilters}
        onCellFilterChange={filterConfig?.onCellFilterChange}
        {...(filterConfig?.filters && filterConfig.filters.length > 0
          ? { externalFilters: filterConfig.filters }
          : {})}
        {...(filterConfig?.search ? { externalSearch: filterConfig.search } : {})}
        {...(filterConfig?.timeRange ? { externalTimeRange: filterConfig.timeRange } : {})}
        {...(pagination ? { pagination } : {})}
        {...(download ? { download } : {})}
      />
    );
  };
}
