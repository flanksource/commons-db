/**
 * The reconcile surface's pure logic: the route it lives at, the key expression
 * a field mapping generates, and the derivations the results view reads off a
 * ReconcileResult.
 *
 * Everything here mirrors semantics the engine already fixes — counts are per
 * key rather than per emitted row, a duplicated identity is one ambiguity and
 * not N findings, and a lag reads the way `query/reconcile_table.go` prints it —
 * so the browser cannot quietly disagree with the CLI about what a run found.
 */

import type { ResolvedOperation } from "@flanksource/clicky-ui";

export type ReconcileStatus = "matched" | "only_source" | "only_dest";

export type ResultRow = Record<string, unknown>;

/** One emitted pair, shaped as `query.ReconcileRow` marshals. */
export type ReconcileRow = {
  key: string;
  status: ReconcileStatus;
  source?: ResultRow;
  dest?: ResultRow;
  source_time?: string;
  dest_time?: string;
  /** dest_time - source_time, in nanoseconds (Go's time.Duration). */
  time_diff?: number;
  source_dup_index?: number;
  source_dup_count?: number;
  dest_dup_index?: number;
  dest_dup_count?: number;
};

export type ReconcileStats = {
  matched: number;
  only_source: number;
  only_dest: number;
  dup_keys: number;
};

/** `query.ReconcileResult` as the reconcile action returns it. */
export type ReconcileResult = {
  source: string;
  dest: string;
  rows: ReconcileRow[];
  stats: ReconcileStats;
  limit?: number;
  source_truncated?: boolean;
  dest_truncated?: boolean;
};

/** The `reconcile:` block a profile stores, as `query.ReconcileConfig`. */
export type ReconcileConfig = {
  dest?: string;
  limit?: number;
  params?: Record<string, string>;
  key?: { columns?: string[]; cel?: string };
  timeColumn?: string;
};

export type ProfileColumn = { name?: string; label?: string; kind?: string };
export type ProfileParam = { name?: string; label?: string };

/** The parts of a profile document this surface reads. */
export type ProfileDocument = {
  profile?: string;
  columns?: ProfileColumn[];
  params?: ProfileParam[];
  reconcile?: ReconcileConfig;
  [key: string]: unknown;
};

/* --------------------------------------------------------------- the route */

/**
 * A profile's dynamic-entity slug, mirroring `slugify` in
 * cmd/query/profiles/store.go. The route carries `profile-<slug>`, while the API
 * addresses a profile by its name — `GET /api/v1/profiles/<surface-key>` runs
 * the profile rather than returning its document — so the two are mapped here
 * rather than assumed interchangeable.
 */
export function profileSlug(name: string): string {
  let slug = "";
  for (const character of name.trim().toLowerCase()) {
    if ((character >= "a" && character <= "z") || (character >= "0" && character <= "9")) slug += character;
    else if (" -_/.".includes(character)) slug += "-";
  }
  return slug.replace(/^-+|-+$/g, "");
}

/** The stored profile a `profile-<slug>` surface key addresses. */
export function profileForSurface(
  documents: ProfileDocument[],
  surfaceKey: string,
): ProfileDocument | undefined {
  const slug = surfaceKey.replace(/^profile-/, "");
  return documents.find((document) => profileSlug(document.profile ?? "") === slug);
}

/** `/profile-<slug>/reconcile`, beside the profile's own `/edit` route. */
export function reconcileRoute(surfaceKey: string): string {
  return `/${surfaceKey}/reconcile`;
}

/** Surface key a reconcile route addresses, or null when the path is not one. */
export function reconcileSurfaceKey(pathname: string): string | null {
  return pathname.match(/^\/(profile-[^/]+)\/reconcile\/?$/)?.[1] ?? null;
}

/**
 * The bench state carried in the query string, so a reconcile someone is still
 * shaping can be shared or survive a reload before it is saved on the profile.
 */
export type ReconcileQuery = { dest?: string; cel?: string; limit?: number };

export function parseReconcileQuery(search: string): ReconcileQuery {
  const params = new URLSearchParams(search);
  const query: ReconcileQuery = {};
  const dest = params.get("dest");
  const cel = params.get("cel");
  const limit = Number(params.get("limit"));
  if (dest) query.dest = dest;
  if (cel) query.cel = cel;
  if (Number.isFinite(limit) && limit > 0) query.limit = limit;
  return query;
}

export function reconcileQueryString(query: ReconcileQuery): string {
  const params = new URLSearchParams();
  if (query.dest) params.set("dest", query.dest);
  if (query.cel) params.set("cel", query.cel);
  if (query.limit) params.set("limit", String(query.limit));
  const encoded = params.toString();
  return encoded ? `?${encoded}` : "";
}

/* ------------------------------------------------------------------ the key */

/** One field on each side carrying the same identity. */
export type KeyPairing = { source: string; dest: string };

/**
 * The CEL a set of pairings generates. One expression is evaluated against rows
 * from both sides, so each pairing has to branch on which field is present;
 * several pairings are joined with the NUL the engine puts between key parts,
 * and parenthesised because a conditional binds looser than concatenation.
 */
export function celForPairings(pairings: KeyPairing[]): string {
  const parts = pairings.map((pairing) => {
    const branch = `has(row.${pairing.source}) ? string(row.${pairing.source}) : string(row.${pairing.dest})`;
    return pairings.length > 1 ? `(${branch})` : branch;
  });
  return parts.join(' + "\\x00" + ');
}

/** Field names a profile document offers as key candidates. */
export function profileFields(document: ProfileDocument | undefined): string[] {
  const fields: string[] = [];
  for (const column of document?.columns ?? []) {
    const name = column.name?.trim() ?? "";
    if (name !== "") fields.push(name);
  }
  return fields;
}

/**
 * The profiles `reconcile` action, found by its clicky metadata rather than by
 * path, so the UI keeps working when the executor changes how it names routes.
 */
export function findReconcileAction(
  operations: ResolvedOperation[],
): ResolvedOperation | undefined {
  return operations.find((operation) => {
    const metadata = operation.operation["x-clicky"];
    return metadata?.surface === "profiles" && metadata.actionName === "reconcile";
  });
}

/** The column a side reads its event time from, when it declares one. */
export function timestampField(document: ProfileDocument | undefined): string {
  return (document?.columns ?? []).find((column) => column.kind === "timestamp")?.name ?? "";
}

export type ParamRouting = {
  name: string;
  value: string;
  sides: ("source" | "dest")[];
  /** True when one side does not declare it, so the engine drops it there. */
  dropped: boolean;
};

/**
 * Which side accepts each filter. The engine narrows a reconcile's params to
 * what each profile declares and drops the rest with nothing louder than a
 * debug log, so the bench says it before the run instead.
 */
export function routeParams(
  values: Record<string, string>,
  source: ProfileDocument | undefined,
  dest: ProfileDocument | undefined,
): ParamRouting[] {
  const declares = (document: ProfileDocument | undefined, name: string) =>
    (document?.params ?? []).some((param) => param.name === name);
  return Object.entries(values).map(([name, value]) => {
    const sides: ("source" | "dest")[] = [];
    if (declares(source, name)) sides.push("source");
    if (declares(dest, name)) sides.push("dest");
    return { name, value, sides, dropped: sides.length < 2 };
  });
}

/* -------------------------------------------------------------- the result */

/** All emitted rows for one key: 1 for a miss, N×M for a duplicated identity. */
export type KeyGroup = {
  key: string;
  status: ReconcileStatus;
  primary: ReconcileRow;
  pairs: ReconcileRow[];
  sourceDupCount: number;
  destDupCount: number;
};

export function groupByKey(rows: ReconcileRow[]): KeyGroup[] {
  const groups = new Map<string, KeyGroup>();
  for (const row of rows) {
    const existing = groups.get(row.key);
    if (existing) {
      existing.pairs.push(row);
      continue;
    }
    groups.set(row.key, {
      key: row.key,
      status: row.status,
      primary: row,
      pairs: [row],
      sourceDupCount: row.source_dup_count ?? (row.source ? 1 : 0),
      destDupCount: row.dest_dup_count ?? (row.dest ? 1 : 0),
    });
  }
  return [...groups.values()];
}

export function isDuplicated(group: KeyGroup): boolean {
  return group.sourceDupCount > 1 || group.destDupCount > 1;
}

/**
 * Recomputes the engine's summary from the emitted rows. Counts are per key: a
 * key with a 2×2 expansion counts once as matched while emitting four rows.
 */
export function deriveStats(rows: ReconcileRow[]): ReconcileStats {
  const stats: ReconcileStats = { matched: 0, only_source: 0, only_dest: 0, dup_keys: 0 };
  for (const group of groupByKey(rows)) {
    stats[group.status] += 1;
    if (isDuplicated(group)) stats.dup_keys += 1;
  }
  return stats;
}

export type LaneId = ReconcileStatus | "ambiguous";

export function laneGroups(groups: KeyGroup[], lane: LaneId): KeyGroup[] {
  return groups.filter((group) => (lane === "ambiguous" ? isDuplicated(group) : group.status === lane));
}

/** Keys per lane page. A reconcile over a wide key range has more one-sided
 * keys than a browser will render, and the lane is triage — it is read from the
 * top, a page at a time. */
export const LANE_PAGE_SIZE = 100;

export type LanePage = {
  groups: KeyGroup[];
  /** Zero-indexed page, clamped into range so an out-of-range page shows the
   * last one rather than an empty lane. */
  page: number;
  pages: number;
  total: number;
};

/**
 * lanePage windows a lane's keys. The whole reconcile is already in hand, so
 * this bounds the DOM rather than the read — but an unbounded lane table is its
 * own failure: a browser that stops painting at ten thousand rows has quietly
 * hidden the rest, which is the same lie a truncated read tells.
 */
export function lanePage(groups: KeyGroup[], page: number, pageSize = LANE_PAGE_SIZE): LanePage {
  const pages = Math.max(1, Math.ceil(groups.length / pageSize));
  const clamped = Math.min(Math.max(page, 0), pages - 1);
  const start = clamped * pageSize;
  return {
    groups: groups.slice(start, start + pageSize),
    page: clamped,
    pages,
    total: groups.length,
  };
}

const MILLISECOND = 1_000_000;

/**
 * Mirrors `humanizeDuration` in `query/reconcile_table.go`, so a lag reads the
 * same in the browser as on the CLI.
 */
export function humanizeDuration(ns: number): string {
  if (ns === 0) return "0s";
  const sign = ns < 0 ? "-" : "";
  const abs = Math.abs(ns);
  if (abs < 1_000) return `${sign}${abs}ns`;
  if (abs < MILLISECOND) return `${sign}${Math.floor(abs / 1_000)}µs`;
  if (abs < 1_000 * MILLISECOND) return `${sign}${Math.floor(abs / MILLISECOND)}ms`;
  if (abs < 60_000 * MILLISECOND) return `${sign}${(abs / (1_000 * MILLISECOND)).toFixed(2)}s`;
  const totalSeconds = Math.floor(abs / (1_000 * MILLISECOND));
  return `${sign}${Math.floor(totalSeconds / 60)}m${totalSeconds % 60}s`;
}

/** `15:04:05.000`, the format the CLI renderer uses for both side timestamps. */
export function formatTime(iso: string | undefined): string {
  if (!iso) return "";
  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) return "";
  const pad = (value: number, width = 2) => String(value).padStart(width, "0");
  return `${pad(at.getUTCHours())}:${pad(at.getUTCMinutes())}:${pad(at.getUTCSeconds())}.${pad(at.getUTCMilliseconds(), 3)}`;
}

export function displayKey(key: string): string {
  return key === "" ? "(empty)" : key;
}

export function cellText(row: ResultRow | undefined, field: string): string {
  const value = row?.[field];
  if (value === undefined || value === null) return "";
  return typeof value === "object" ? JSON.stringify(value) : String(value);
}

/**
 * What a bounded run has to say about itself. A key can only be missing from a
 * side that was read in full, so a truncated run names which of its findings
 * are not findings.
 */
export function boundWarning(result: ReconcileResult): string {
  const side =
    result.source_truncated && result.dest_truncated
      ? "both sides were"
      : result.source_truncated
        ? "the source was"
        : result.dest_truncated
          ? "the destination was"
          : "";
  if (!side) return "";
  return `Bounded to ${result.limit ?? 0} rows per side — ${side} cut short, so a one-sided key may simply be outside the window.`;
}

/** The reconcile block the bench saves back onto the source profile. */
export function storedConfig(state: {
  dest: string;
  cel: string;
  limit: number;
  params: Record<string, string>;
}): ReconcileConfig {
  const config: ReconcileConfig = { dest: state.dest, key: { cel: state.cel } };
  if (state.limit > 0) config.limit = state.limit;
  if (Object.keys(state.params).length > 0) config.params = state.params;
  return config;
}
