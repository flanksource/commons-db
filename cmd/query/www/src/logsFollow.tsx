import {
  Button,
  Icon,
  isPinnedToBottom,
  LogsTable,
  Switch,
  useLogTail,
  type ClickyDownloadOptions,
  type DataTableGrouping,
  type DataTableInfinite,
  type DataTablePagination,
  type LogsTableRow,
  type LogTailError,
  type OperationResultFilterConfig,
} from "@flanksource/clicky-ui";
import { UiArrowDown, UiWarningTriangle } from "@flanksource/clicky-ui/icons";
import { useEffect, useLayoutEffect, useRef, useState } from "react";

// The logs surface reads two runs of the same log at once: the cursor walk,
// which is history and ends wherever the reader stopped fetching, and a follow
// session, which starts at the moment it is opened and never reaches backwards.
//
// They are rendered as one list because that is what a log is, but they are not
// one run, and the join between them is the whole difficulty of this file. If
// the walk has not been paged up to the present there is a hole between its last
// row and the tail's first, and — this being an ordered log — the rows either
// side of the hole still look consecutive. That is the same failure
// clickyLogsWalk refuses a partial walk over, so it gets the same answer here:
// the seam is named on screen rather than smoothed over.

/** Which of the two runs a rendered row came from. */
export type FollowRun = "history" | "tail";

// LOGS_SORT orders the table oldest first.
//
// It is not a preference. A log profile is walked forward by cursor, and the one
// provider that can only ever be walked that way says why: the Kubernetes log API
// resumes from a timestamp and nothing else, so `assertAscendingLogOrder` refuses
// any other order outright. Rendering that data newest-first would put a
// descending indicator over an ascending run — and would put a live tail at the
// top, where nothing that has ever been tailed puts it.
export const LOGS_SORT = { key: "timestamp", dir: "asc" } as const;

/**
 * Rows the tail holds before it starts evicting.
 *
 * It is declared here rather than left to the hook's default because the number
 * is quoted to the reader: a shorter list is a claim about the run as much as
 * about memory, and the notice that says so has to say how much is kept.
 */
export const TAIL_MAX_ROWS = 5_000;

/** Distance from the bottom still counted as following the newest line. */
const BOTTOM_THRESHOLD_PX = 48;

const HISTORY_GROUP_LABEL = "Loaded pages";
const TAIL_GROUP_LABEL = "Live tail starts here — earlier lines may not be loaded";

/**
 * Records which run each row came from, keyed by the record itself.
 *
 * The rows arrive back from LogsTable normalized — flattened into columns, with
 * a content digest for an id — but each one still carries the record it was
 * built from on `raw`, and that is the identity the two runs can be told apart
 * by. Nothing in the rendered row says where it came from, and nothing derivable
 * from its content could: the same line may legitimately appear in both.
 */
export function followRunIndex(
  history: readonly Record<string, unknown>[],
  tail: readonly Record<string, unknown>[],
): WeakMap<object, FollowRun> {
  const runs = new WeakMap<object, FollowRun>();
  for (const row of history) runs.set(row, "history");
  for (const row of tail) runs.set(row, "tail");
  return runs;
}

// A row the index cannot place is refused rather than filed under the walk.
// Defaulting it would move the seam without moving what sits above it, which is
// precisely the misreading the seam is drawn to prevent.
function runOf(runs: WeakMap<object, FollowRun>, row: LogsTableRow): FollowRun {
  const run = row.raw && typeof row.raw === "object" ? runs.get(row.raw as object) : undefined;
  if (!run) {
    throw new Error(
      `log row ${row.id} came from neither the loaded pages nor the live tail — the two runs can no longer be told apart`,
    );
  }
  return run;
}

/**
 * Splits the rendered rows into the walk and the tail, so the boundary between
 * them is a header row the reader meets on the way past rather than an invisible
 * change of source.
 */
export function followGrouping(
  runs: WeakMap<object, FollowRun>,
  tail: { droppedRows: number; startedAt: string | undefined },
): DataTableGrouping<LogsTableRow> {
  return {
    getGroupKey: (row) => runOf(runs, row),
    getGroupLabel: (key) => (key === "tail" ? TAIL_GROUP_LABEL : HISTORY_GROUP_LABEL),
    getGroupMeta: (key) => (key === "tail" ? tailGroupMeta(tail) : undefined),
    // Beside the label, because both halves describe the same boundary.
    metaAlign: "start",
    metaClassName: "text-amber-600 dark:text-amber-500",
  };
}

/**
 * What the tail's header says about itself: when it opened, and how much of it
 * the cap has already thrown away.
 *
 * The eviction notice sits here rather than at the top of the scroll — where the
 * clicky-ui playground puts it — because there the tail is the whole list, and
 * here it is the bottom half of one. The lines the cap dropped are missing from
 * the start of the tail, so that is where saying so means anything.
 */
function tailGroupMeta(tail: { droppedRows: number; startedAt: string | undefined }): string | undefined {
  const parts: string[] = [];
  if (tail.startedAt) parts.push(`started ${tail.startedAt}`);
  if (tail.droppedRows > 0) {
    parts.push(
      tail.droppedRows === 1
        ? `1 earlier line is no longer held — the tail keeps the newest ${TAIL_MAX_ROWS}`
        : `${tail.droppedRows} earlier lines are no longer held — the tail keeps the newest ${TAIL_MAX_ROWS}`,
    );
  }
  return parts.length > 0 ? parts.join(" · ") : undefined;
}

/**
 * Keys a row by its run as well as its content.
 *
 * The normalized id is already a digest of the line rather than its position, so
 * appending never re-keys what is on screen. Qualifying it by run is what keeps
 * the overlap honest in the other direction: a line the walk already loaded and
 * the tail then delivers again is two deliveries of one line, and collapsing
 * them onto one key would hand a row that is still on screen to a different run.
 */
export function followRowId(runs: WeakMap<object, FollowRun>): (row: LogsTableRow) => string {
  return (row) => `${runOf(runs, row)}:${row.id}`;
}

const FOLLOW_ERROR_HEADINGS: Record<LogTailError["scope"], string> = {
  start: "Follow could not start",
  stream: "The live tail reported an error",
  session: "The follow session ended",
};

/**
 * The sentence shown when a tail fails.
 *
 * The three scopes are not interchangeable to a reader — a session that never
 * opened (the server's concurrency cap answers 409 here) is a different thing
 * from one that opened and broke — and any of them leaves the toggle looking on
 * while nothing arrives, which is why none of them is allowed to be quiet.
 */
export function followErrorText(error: LogTailError): string {
  return `${FOLLOW_ERROR_HEADINGS[error.scope]}: ${error.message}`;
}

export interface LogsSurfaceProps {
  /** The cursor walk so far, oldest first. */
  history: Record<string, unknown>[];
  /** Stored profile name — what POST /profile/{name}/sessions resolves through the store. */
  profile: string;
  /** Whether this profile's provider can tail its source at all. */
  canFollow: boolean;
  /** The walk's own parameters — see followParams for what still stands between them and the session. */
  params?: Record<string, unknown>;
  loading: boolean;
  columnFilterKeys: Record<string, string>;
  filterConfig?: OperationResultFilterConfig;
  pagination?: DataTablePagination;
  download?: ClickyDownloadOptions;
  infinite?: DataTableInfinite;
}

export function LogsSurface({
  history,
  profile,
  canFollow,
  params,
  loading,
  columnFilterKeys,
  filterConfig,
  pagination,
  download,
  infinite,
}: LogsSurfaceProps) {
  const [following, setFollowing] = useState(false);
  const [pinned, setPinned] = useState(true);
  const scrollRef = useRef<HTMLDivElement | null>(null);

  const tail = useLogTail({
    profile,
    params,
    following: canFollow && following,
    maxRows: TAIL_MAX_ROWS,
  });

  // The buffer survives switching Follow off, so what was tailed stays readable
  // and the seam above it stays drawn; only new rows stop arriving.
  const tailed = tail.rows.length > 0;
  const logs = tailed ? [...history, ...tail.rows] : history;
  const runs = followRunIndex(history, tail.rows);

  useEffect(() => {
    const element = scrollRef.current;
    if (!element) return;
    const onScroll = () => setPinned(isPinnedToBottom(element, BOTTOM_THRESHOLD_PX));
    onScroll();
    element.addEventListener("scroll", onScroll, { passive: true });
    return () => element.removeEventListener("scroll", onScroll);
  }, []);

  // Reading wins over chasing: rows keep arriving while the reader is scrolled
  // up, they just stop moving the viewport until the tail is re-pinned.
  useLayoutEffect(() => {
    const element = scrollRef.current;
    if (element && following && pinned) element.scrollTop = element.scrollHeight;
  }, [tail.rows, following, pinned]);

  const scrollToLatest = () => {
    const element = scrollRef.current;
    if (!element) return;
    setPinned(true);
    element.scrollTo({ top: element.scrollHeight, behavior: "smooth" });
  };

  return (
    <div className="flex h-full min-h-0 flex-col gap-2">
      {tail.error && (
        <p
          role="alert"
          className="flex items-center gap-1.5 text-xs text-amber-600 dark:text-amber-500"
        >
          <Icon icon={UiWarningTriangle} className="size-3.5 shrink-0" />
          {followErrorText(tail.error)}
        </p>
      )}

      <div className="relative flex min-h-0 flex-1 flex-col">
        <LogsTable
          logs={logs}
          loading={loading}
          autoFilter={false}
          fullscreenTitle="Logs"
          defaultSort={LOGS_SORT}
          // The rows came back in the order the profile declared, and only some of
          // them are here. Re-sorting a partial walk in the browser would order the
          // pages fetched so far against each other and present the result as the
          // order of the whole.
          manualSort
          columnFilterKeys={columnFilterKeys}
          scrollContainerRef={scrollRef}
          getRowId={followRowId(runs)}
          // Fullscreen renders a second copy of the table inside a modal, and the
          // tail pins through a scroll handle only one of them can hold. DataTable
          // hands it to whichever copy is on screen, so following survives opening
          // and closing the modal and a log surface keeps its fullscreen control.
          showFullscreenControl
          cellFilters={filterConfig?.cellFilters}
          onCellFilterChange={filterConfig?.onCellFilterChange}
          {...(tailed
            ? {
                grouping: followGrouping(runs, {
                  droppedRows: tail.droppedRows,
                  startedAt: tail.session?.startedAt,
                }),
              }
            : {})}
          {...(canFollow
            ? {
                filterBarProps: {
                  trailing: (
                    <div className="flex items-center gap-3">
                      <Switch checked={following} onChange={setFollowing} label="Follow" />
                      <span className="text-xs tabular-nums text-muted-foreground">
                        {tail.status}
                        {tailed ? ` · ${tail.rows.length} tailed` : ""}
                      </span>
                    </div>
                  ),
                },
              }
            : {})}
          {...(infinite ? { infinite } : {})}
          {...(filterConfig?.filters && filterConfig.filters.length > 0
            ? { externalFilters: filterConfig.filters }
            : {})}
          {...(filterConfig?.search ? { externalSearch: filterConfig.search } : {})}
          {...(filterConfig?.timeRange ? { externalTimeRange: filterConfig.timeRange } : {})}
          {...(pagination ? { pagination } : {})}
          {...(download ? { download } : {})}
        />

        {following && !pinned && logs.length > 0 && (
          <Button
            type="button"
            size="sm"
            variant="secondary"
            aria-label="Scroll to latest"
            onClick={scrollToLatest}
            className="absolute bottom-4 left-1/2 -translate-x-1/2 gap-1.5 rounded-full shadow-md"
          >
            <Icon icon={UiArrowDown} className="size-3.5" />
            Latest
          </Button>
        )}
      </div>
    </div>
  );
}
