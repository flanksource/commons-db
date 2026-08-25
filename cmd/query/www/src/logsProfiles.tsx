import {
  type ClickyNode,
  type ExecutionResponse,
  type ResultRenderContext,
} from "@flanksource/clicky-ui";
import type { ReactNode } from "react";
import { LogsSurface } from "./logsFollow";
import type { ProfileDocument } from "./reconcileModel";
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

/** What the logs renderer needs to know about a profile beyond that it is one. */
export type LogsSurfaceProfile = {
  /**
   * The profile as it is stored. The route and the list request both carry the
   * slug, but the session endpoint resolves through `store.Get(name)` — see
   * cmd/query/profiles/resolver.go — so the slug is not an address it accepts,
   * and the mapping back has to be kept rather than reconstructed.
   */
  profile: string;
  /**
   * Whether the provider can tail its source, as `GET /api/v1/profiles` derives
   * it. It decides whether the Follow control exists at all: offering one over a
   * provider that cannot stream is offering a button whose only outcome is 400.
   */
  follow: boolean;
};

// logsProfileSurfaces indexes the profiles that declare `render: logs` by the
// dynamic-entity name their surface is addressed as ("profile-" + slug), which
// is what appears in a list-operation request URL.
export function logsProfileSurfaces(
  documents: readonly ProfileDocument[],
): Map<string, LogsSurfaceProfile> {
  const surfaces = new Map<string, LogsSurfaceProfile>();
  for (const p of documents) {
    if (p.render !== "logs") continue;
    const name = typeof p.profile === "string" ? p.profile : typeof p.name === "string" ? p.name : "";
    if (name) surfaces.set("profile-" + slugify(name), { profile: name, follow: p.follow === true });
  }
  return surfaces;
}

// useLogsSurfaces reads the shared profile query, so a rename does not leave this
// view resolving against a stale profile list.
export function useLogsSurfaces(): Map<string, LogsSurfaceProfile> {
  const { data } = useProfiles();
  return logsProfileSurfaces(data ?? []);
}

// followParams is the walk's own parameters, handed to the follow session so the
// tail is asked for the query the reader is already looking at rather than the
// profile's defaults.
//
// The paging and rendering keys are dropped because they describe one request
// and not the query: `limit` is the size of the page just fetched, and handing it
// to a session that is meant to run until it is stopped would cap the tail at a
// page. The list is cmd/query/profiles/execution.go's IsReservedParam — the
// server drops them too, and disagreeing with it here would silently change the
// query the tail runs.
//
// What survives the filter does reach the session: useLogTail serialises these
// into the start request's *query string* (clicky-ui's encodeTailParams), which
// is exactly where sessionHandler.start reads its params from — see
// cmd/query/sessions/service.go. So a filtered walk tails what it is showing.
const RESERVED_PARAMS = new Set([
  "format",
  "scope",
  "page",
  "limit",
  "offset",
  "filename",
  "_download",
  "args",
  "__schema",
  "__info",
  "__lookup",
  "__lookup_filter",
  "__lookup_q",
]);

export function followParams(requestUrl: string): Record<string, string> {
  const query = requestUrl.split("?")[1];
  if (!query) return {};
  const params: Record<string, string> = {};
  for (const [key, value] of new URLSearchParams(query)) {
    if (!RESERVED_PARAMS.has(key)) params[key] = value;
  }
  return params;
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

// clickyLogsWalk flattens every page of a cursor walk into one ascending run of
// records, oldest first.
//
// The pages arrive unmerged because concatenating two rendered documents is not
// something the explorer can do generically — see ResultRenderContext.pages. For
// a log table it is exactly a concatenation, because the walk is ordered and
// each page resumes after the last row of the one before it.
//
// A page that is not a table fails the whole walk rather than being skipped. A
// gap in the middle of an ordered run is the one thing a log reader cannot see:
// the rows either side still look consecutive, so dropping one page quietly
// turns "these lines are what happened" into a claim nobody can check.
export function clickyLogsWalk(
  pages: readonly ExecutionResponse[] | undefined,
  response: ExecutionResponse,
): Record<string, unknown>[] | undefined {
  if (!pages || pages.length <= 1) return clickyLogsRows(response.parsed);
  const walk: Record<string, unknown>[] = [];
  for (const page of pages) {
    const rows = clickyLogsRows(page.parsed);
    if (!rows) return undefined;
    walk.push(...rows);
  }
  return walk;
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
  // A column with a display width is rendered truncated — a twelve-character
  // `timestamp` arrives as "2026-08-18 …" — and the node keeps what it was
  // rendered from in its children. Reading `plain` here would take the
  // rendering, and this table formats values itself: the truncated form is not
  // a shorter answer but an unparseable one, so `2026-08-18 07:39:59` becomes
  // Invalid Date and the column renders blank rather than narrow.
  if (node.style?.maxWidth && node.children?.length) {
    return node.children.map(clickyNodeValue).join("");
  }
  if (node.plain !== undefined) return node.plain;
  if (node.text !== undefined) return node.text;
  if (node.source !== undefined) return node.source;
  if (node.filterValue !== undefined) return node.filterValue;
  if (node.children) return node.children.map(clickyNodeValue).join("");
  return "";
}

// logsResultRenderer is the EntityExplorerApp result override: when the result's
// request URL targets a logs entity it hands the walk so far to LogsSurface,
// otherwise it returns the default view unchanged.
//
// It stops at the element. The follow session is state with a lifecycle — a
// websocket to Loki or a stream to a kubelet that has to be released again — and
// this function is called during the parent's render, where no hook may run. The
// component is the seam that can hold it.
export function logsResultRenderer(
  logsSurfaces: Map<string, LogsSurfaceProfile>,
): (ctx: ResultRenderContext) => ReactNode {
  return ({ response, loading, defaultView, filterConfig, pagination, download, pages, infinite }) => {
    const requestUrl = response?.requestUrl;
    const surface = requestUrl ? logsSurfaces.get(entitySegment(requestUrl)) : undefined;
    if (!requestUrl || !surface) return defaultView;
    const logs = clickyLogsWalk(pages, response);
    if (!logs) return defaultView;
    return (
      <LogsSurface
        // Keyed by profile so moving between two logs surfaces remounts rather
        // than re-props: Follow is a session against one profile, and carrying
        // the switch across would open a stream on whichever log was navigated to
        // without anyone having asked for it.
        key={surface.profile}
        history={logs}
        profile={surface.profile}
        canFollow={surface.follow}
        params={followParams(requestUrl)}
        loading={loading}
        columnFilterKeys={logsColumnFilterKeys(response.parsed)}
        {...(filterConfig ? { filterConfig } : {})}
        {...(infinite ? { infinite } : {})}
        {...(pagination ? { pagination } : {})}
        {...(download ? { download } : {})}
      />
    );
  };
}
