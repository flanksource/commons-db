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

import type {
  OpenAPIParameter,
  OperationsApiClient,
  ResolvedOperation,
} from "@flanksource/clicky-ui";

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

export type SnapshotColumn = {
  name: string;
  label?: string;
  type?: string;
  hidden?: boolean;
};

/**
 * `query.ProviderDiagnostics` as it marshals.
 *
 * clicky-ui exports a QueryBrowserDiagnostics of the same lineage, but its
 * published copy predates `rendered`, `connection` and `pages` — the three
 * fields this surface exists to show — so the shape is stated here against the
 * Go struct rather than imported at a version behind it.
 */
export type ProviderDiagnostics = {
  provider: string;
  request: {
    /** The statement the provider issued; the first page's, for a walk. */
    query?: string;
    /** The profile's query once the engine templated it. */
    rendered?: string;
    connection?: string;
    arguments?: unknown[];
    options?: Record<string, unknown>;
    details?: Record<string, unknown>;
  };
  response?: {
    durationMs?: number;
    returnedRows?: number;
    pages?: number;
    details?: Record<string, unknown>;
    preview?: string;
    contentType?: string;
    truncated?: boolean;
  };
  error?: string;
};

/**
 * What one side of a run actually asked its backend, as
 * `query.ReconcileSideExecution` marshals.
 */
export type ReconcileSideExecution = {
  side: string;
  profile: string;
  provider: string;
  /** The profile's query as authored; the rendered form is on `diagnostics`. */
  query?: string;
  filters?: Record<string, unknown>;
  diagnostics?: ProviderDiagnostics;
  rows: number;
  pages?: number;
  durationMs: number;
  backendMs?: number;
  truncated?: boolean;
};

/**
 * How a snapshot was produced: the config the run used, and what each side did.
 * Optional because a materialized projection reports the descriptor of a
 * derived profile whose provenance belongs to the run it came from.
 */
export type ReconcileProvenance = {
  config: ReconcileConfig;
  execution?: {
    mode?: string;
    buffered_reason?: string;
    source?: ReconcileSideExecution;
    dest?: ReconcileSideExecution;
    ran_at?: string;
  };
};

export type ReconcileSnapshot = {
  id: string;
  connection: string;
  connection_id: string;
  profile: string;
  surface: string;
  url: string;
  columns: SnapshotColumn[];
  row_count: number;
  stats: ReconcileStats;
  source: string;
  dest: string;
  reconcile?: ReconcileProvenance;
  source_truncated?: boolean;
  dest_truncated?: boolean;
  created_at?: string;
  idle_age: number;
  expires_at: string;
};

/** The `reconcile:` block a profile stores, as `query.ReconcileConfig`. */
export type ReconcileConfig = {
  dest?: string;
  sourceFilters?: Record<string, string>;
  destFilters?: Record<string, string>;
  key?: { columns?: string[]; cel?: string };
  range?: { from?: string; to?: string };
  timeColumn?: string;
};

export type ProfileColumn = { name?: string; label?: string; kind?: string };
export type ProfileField = { name: string; label?: string };
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

/**
 * `/profile-<slug>/reconcile/<snapshot-id>`, where a finished run is read.
 *
 * The bench and the outcome are two pages rather than one scroll: shaping a join
 * and triaging what it found are different jobs, and a run that answers the
 * first question should hand the whole screen to the second.
 *
 * The run is named by its snapshot rather than restated in the query string,
 * because the snapshot already stores the join that produced it. That is what
 * lets this URL be shared, bookmarked, and survive a server restart.
 */
export function reconcileSnapshotRoute(surfaceKey: string, snapshotId: string): string {
  return `${reconcileRoute(surfaceKey)}/${snapshotId}`;
}

/** A snapshot id, as `uuid.NewString()` writes it in cmd/query/snapshots. */
const SNAPSHOT_ID = "[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}";
const RECONCILE_PATH = new RegExp(`^/(profile-[^/]+)/reconcile(?:/(${SNAPSHOT_ID}))?/?$`, "i");

export type ReconcileRouteView =
  | { surfaceKey: string; view: "bench" }
  | { surfaceKey: string; view: "results"; snapshotId: string };

/**
 * Which reconcile page a path addresses, or null when the path is neither.
 *
 * The trailing segment must look like a snapshot id rather than merely be
 * present, so a mistyped or stale sub-route is a non-route instead of a fetch
 * of something that was never an id.
 */
export function reconcileRouteView(pathname: string): ReconcileRouteView | null {
  const match = pathname.match(RECONCILE_PATH);
  if (!match) return null;
  const surfaceKey = match[1]!;
  return match[2] ? { surfaceKey, view: "results", snapshotId: match[2] } : { surfaceKey, view: "bench" };
}

/** Surface key a reconcile route addresses, or null when the path is not one. */
export function reconcileSurfaceKey(pathname: string): string | null {
  return reconcileRouteView(pathname)?.surfaceKey ?? null;
}

/**
 * The bench state carried in the query string, so a reconcile someone is still
 * shaping can be shared or survive a reload before it is saved on the profile.
 */
export type ReconcileQuery = {
  dest?: string;
  cel?: string;
  snapshotAge?: string;
  sourceFilters?: Record<string, string>;
  destFilters?: Record<string, string>;
};

export function parseReconcileQuery(search: string): ReconcileQuery {
  const params = new URLSearchParams(search);
  const query: ReconcileQuery = {};
  const dest = params.get("dest");
  const cel = params.get("cel");
  const snapshotAge = params.get("snapshot-age");
  if (dest) query.dest = dest;
  if (cel) query.cel = cel;
  if (snapshotAge) query.snapshotAge = snapshotAge;
  const sourceFilters = filterQueryValues(params, "source-filter");
  const destFilters = filterQueryValues(params, "dest-filter");
  if (Object.keys(sourceFilters).length > 0) query.sourceFilters = sourceFilters;
  if (Object.keys(destFilters).length > 0) query.destFilters = destFilters;
  return query;
}

export function reconcileQueryString(query: ReconcileQuery): string {
  const params = new URLSearchParams();
  if (query.dest) params.set("dest", query.dest);
  if (query.cel) params.set("cel", query.cel);
  if (query.snapshotAge) params.set("snapshot-age", query.snapshotAge);
  appendFilterQueryValues(params, "source-filter", query.sourceFilters);
  appendFilterQueryValues(params, "dest-filter", query.destFilters);
  const encoded = params.toString();
  return encoded ? `?${encoded}` : "";
}

export function initialReconcileFilters(
  stored: ReconcileConfig | undefined,
  query: ReconcileQuery,
): Pick<Required<ReconcileConfig>, "sourceFilters" | "destFilters"> {
  const changedDestination = query.dest != null && query.dest !== stored?.dest;
  return {
    sourceFilters: { ...stored?.sourceFilters, ...query.sourceFilters },
    destFilters: { ...(changedDestination ? {} : stored?.destFilters), ...query.destFilters },
  };
}

/* ------------------------------------------------------- the results route */

/** Rows per page on the results route, and on the virtual profile it links to. */
export const RESULTS_PAGE_SIZE = 100;

/**
 * What the results page is showing: which outcome lane, where in it, and the
 * order it is read in.
 *
 * The sort lives here rather than in the table because the snapshot is paged by
 * the server: ordering is a property of the whole result, so it travels with the
 * position in the URL and a bookmarked page reopens on the same rows.
 */
export type ResultsView = {
  lane: LaneId;
  page: number;
  pageSize: number;
  sort?: string;
  desc?: boolean;
};

const LANES: LaneId[] = ["matched", "only_source", "only_dest", "ambiguous"];

export function parseLane(value: string | null): LaneId | null {
  return LANES.find((lane) => lane === value) ?? null;
}

/**
 * The lane a finished run opens on: the first one that found something, read in
 * the order triage cares about. A clean run opens on `matched` rather than on an
 * empty lane.
 */
export function initialLane(stats: ReconcileStats): LaneId {
  if (stats.only_source > 0) return "only_source";
  if (stats.only_dest > 0) return "only_dest";
  if (stats.dup_keys > 0) return "ambiguous";
  return "matched";
}

/**
 * Where in a snapshot the reader is. The join itself is no longer carried: the
 * snapshot stores its own provenance, so the URL names the run and nothing else.
 */
export function resultsViewQueryString(view: ResultsView): string {
  const params = new URLSearchParams();
  params.set("outcome", view.lane);
  if (view.page > 0) params.set("page", String(view.page));
  if (view.pageSize !== RESULTS_PAGE_SIZE) params.set("size", String(view.pageSize));
  if (view.sort) {
    params.set("sort", view.sort);
    // Ascending is the server's default, so only the other one is worth naming.
    if (view.desc) params.set("order", "desc");
  }
  return `?${params.toString()}`;
}

/**
 * The results view a URL asks for. `fallback` is the lane a run would open on,
 * so a link with no outcome — or one naming a lane that is not a lane — lands
 * where the reader would have been sent anyway.
 */
export function parseResultsView(search: string, fallback: LaneId = "matched"): ResultsView {
  const params = new URLSearchParams(search);
  const page = Number.parseInt(params.get("page") ?? "", 10);
  const pageSize = Number.parseInt(params.get("size") ?? "", 10);
  const sort = params.get("sort")?.trim();
  return {
    lane: parseLane(params.get("outcome")) ?? fallback,
    page: Number.isFinite(page) && page > 0 ? page : 0,
    pageSize: Number.isFinite(pageSize) && pageSize > 0 ? pageSize : RESULTS_PAGE_SIZE,
    // A direction with no column to apply it to is not a sort, so it is dropped
    // with the column rather than left behind to mean nothing.
    ...(sort ? { sort, desc: params.get("order") === "desc" } : {}),
  };
}

/* --------------------------------------------------- reading a stored run */

export function reconcileSnapshotQueryKey(snapshotId: string): readonly ["reconcile-snapshot", string] {
  return ["reconcile-snapshot", snapshotId] as const;
}

/**
 * The profiles `reconcile-snapshot` action — a GET, unlike its siblings, because
 * reading a stored run is what a bookmarked results URL does.
 */
export function findReconcileSnapshotAction(
  operations: ResolvedOperation[],
): ResolvedOperation | undefined {
  return operations.find((operation) => {
    const metadata = operation.operation["x-clicky"];
    return metadata?.surface === "profiles" && metadata.actionName === "reconcile-snapshot";
  });
}

export async function loadReconcileSnapshot(
  client: Pick<OperationsApiClient, "executeCommand">,
  action: ResolvedOperation,
  profileName: string,
  snapshotId: string,
): Promise<ReconcileSnapshot> {
  const idParam = action.operation["x-clicky"]?.idParam ?? "id";
  const response = await client.executeCommand(
    action.path,
    action.method,
    { [idParam]: profileName, snapshot: snapshotId },
    { Accept: "application/json" },
  );
  if (!response.success) {
    throw new Error(response.error ?? response.message ?? "Loading the reconciliation failed");
  }
  const parsed = response.parsed as Partial<ReconcileSnapshot> | undefined;
  if (!parsed || typeof parsed.id !== "string" || typeof parsed.profile !== "string" || !Array.isArray(parsed.columns)) {
    throw new Error("The reconcile-snapshot action returned no snapshot profile");
  }
  return parsed as ReconcileSnapshot;
}

/**
 * Why a stored run could not be read. Expiring is a lifecycle event and being
 * absent is a broken link, and the page says different things about them.
 */
export type SnapshotFailure = "expired" | "missing" | "failed";

export function classifySnapshotError(error: unknown): SnapshotFailure {
  const status = (error as { status?: number } | null)?.status;
  if (status === 410) return "expired";
  if (status === 404) return "missing";
  // The server names the condition in the error code, which survives even when
  // the status does not — a StatusError renders as "<code>: <message>".
  const message = error instanceof Error ? error.message : String(error ?? "");
  if (/snapshot_expired|has expired/i.test(message)) return "expired";
  if (/snapshot_not_found|not found/i.test(message)) return "missing";
  return "failed";
}

/**
 * The bench query that reopens the join a snapshot ran, so Back lands on that
 * run rather than on whatever the profile happens to store now.
 *
 * A columns key is rendered as the CEL it is equivalent to: `KeySpec.Columns`
 * reads the same named fields on both sides, which is exactly celForPairings
 * with source and dest equal. The bench always runs with CEL anyway, so this is
 * where Back would land regardless.
 *
 * `range` and `timeColumn` are deliberately not carried — the bench cannot edit
 * either, and the engine re-derives the time column from the profile.
 */
export function benchQueryForConfig(
  config: ReconcileConfig | undefined,
  idleAge?: number,
): ReconcileQuery {
  if (!config) return {};
  const columns = config.key?.columns ?? [];
  const cel =
    config.key?.cel ||
    (columns.length > 0 ? celForPairings(columns.map((name) => ({ source: name, dest: name }))) : "");
  const snapshotAge = idleAge != null ? formatIdleAge(idleAge) : "";
  return {
    ...(config.dest ? { dest: config.dest } : {}),
    ...(cel ? { cel } : {}),
    ...(snapshotAge ? { snapshotAge } : {}),
    ...(config.sourceFilters ? { sourceFilters: config.sourceFilters } : {}),
    ...(config.destFilters ? { destFilters: config.destFilters } : {}),
  };
}

/**
 * A Go duration, from the nanoseconds a descriptor reports — the vocabulary the
 * `--snapshot-age` flag takes. An age of zero yields "", so a snapshot that
 * reports none leaves the bench's own default in place rather than blanking it.
 */
export function formatIdleAge(nanoseconds: number): string {
  const seconds = Math.floor(nanoseconds / 1_000_000_000);
  if (seconds <= 0) return "";
  if (seconds % 3600 === 0) return `${seconds / 3600}h`;
  if (seconds % 60 === 0) return `${seconds / 60}m`;
  return `${seconds}s`;
}

/**
 * The virtual profile URL showing what the results page is showing. The explorer
 * seeds a surface's filter state from the query string, so the lane and the page
 * size travel as the snapshot profile's own list parameters — the reader crosses
 * into the generic browser without losing their place.
 *
 * A projection that dropped `outcome` has no lane to filter on, and asking it
 * for one is an error rather than an empty table, so the lane is carried only
 * while the column is still there.
 */
export function virtualProfileHref(snapshot: ReconcileSnapshot, view: ResultsView): string {
  const params = new URLSearchParams();
  if (snapshot.columns.some((column) => column.name === "outcome" && !column.hidden)) {
    params.set("filter.outcome", view.lane);
  }
  params.set("limit", String(view.pageSize));
  return `/${snapshot.surface}?${params.toString()}`;
}

function filterQueryValues(params: URLSearchParams, name: string): Record<string, string> {
  const values: Record<string, string> = {};
  for (const pair of params.getAll(name)) {
    const separator = pair.indexOf("=");
    if (separator > 0) values[pair.slice(0, separator)] = pair.slice(separator + 1);
  }
  return values;
}

function appendFilterQueryValues(
  params: URLSearchParams,
  name: string,
  filters: Record<string, string> | undefined,
) {
  for (const [key, value] of sortedFilterEntries(filters)) params.append(name, `${key}=${value}`);
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

/** Fields a profile document offers as key candidates. */
export function profileFields(document: ProfileDocument | undefined): ProfileField[] {
  const fields: ProfileField[] = [];
  for (const column of document?.columns ?? []) {
    const name = column.name?.trim() ?? "";
    const label = column.label?.trim() ?? "";
    if (name !== "") fields.push(label === "" ? { name } : { name, label });
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

export function findReconcileMaterializeAction(
  operations: ResolvedOperation[],
): ResolvedOperation | undefined {
  return operations.find((operation) => {
    const metadata = operation.operation["x-clicky"];
    return metadata?.surface === "profiles" && metadata.actionName === "reconcile-materialize";
  });
}

export function findProfileRunOperation(
  operations: ResolvedOperation[],
  profileName: string,
): ResolvedOperation | undefined {
  const surface = `profile-${profileSlug(profileName)}`;
  return operations.find((operation) => {
    const metadata = operation.operation["x-clicky"];
    return metadata?.surface === surface && metadata.scope === "collection" && metadata.verb === "list";
  });
}

export function reconcileFilterParameters(operation: ResolvedOperation | undefined): OpenAPIParameter[] {
  const transportRoles = new Set(["limit", "offset", "cursor"]);
  return (operation?.operation.parameters ?? []).filter(
    (parameter) => parameter.in === "query" && !transportRoles.has(parameter["x-clicky"]?.role ?? ""),
  );
}

export function filterFlagValue(filters: Record<string, string>): string {
  return sortedFilterEntries(filters)
    .map(([key, value]) => csvField(`${key}=${value}`))
    .join(",");
}

function sortedFilterEntries(filters: Record<string, string> | undefined): [string, string][] {
  return Object.entries(filters ?? {}).sort(([left], [right]) => left.localeCompare(right));
}

function csvField(value: string): string {
  if (!/[",\r\n]/.test(value)) return value;
  return `"${value.replace(/"/g, '""')}"`;
}

/** The column a side reads its event time from, when it declares one. */
export function timestampField(document: ProfileDocument | undefined): string {
  return (document?.columns ?? []).find((column) => column.kind === "timestamp")?.name ?? "";
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
  sourceFilters: Record<string, string>;
  destFilters: Record<string, string>;
}): ReconcileConfig {
  const config: ReconcileConfig = { dest: state.dest, key: { cel: state.cel } };
  if (Object.keys(state.sourceFilters).length > 0) config.sourceFilters = state.sourceFilters;
  if (Object.keys(state.destFilters).length > 0) config.destFilters = state.destFilters;
  return config;
}
