import type {
  DataTableGrouping,
  LogsTableRow,
  LogTailError,
} from "@flanksource/clicky-ui";

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
export const BOTTOM_THRESHOLD_PX = 48;

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
