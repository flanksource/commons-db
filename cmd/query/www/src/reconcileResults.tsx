/**
 * The reconcile result, read as triage rather than as one wide join.
 *
 * A single table has to carry every column of both profiles and pad the missing
 * side with blanks, which makes a record that never arrived look like a
 * half-empty match. Here the totals come first and each outcome gets a lane
 * showing only the columns that outcome can actually have.
 */

import { useState } from "react";
import { Badge, Icon, SegmentedControl, StatStrip, cn } from "@flanksource/clicky-ui";
import { UiCheck, UiCircleX, UiKey, UiWarningTriangle } from "@flanksource/clicky-ui/icons";

import {
  boundWarning,
  cellText,
  deriveStats,
  displayKey,
  formatTime,
  groupByKey,
  humanizeDuration,
  isDuplicated,
  laneGroups,
  lanePage,
  LANE_PAGE_SIZE,
  profileFields,
  timestampField,
  type LaneId,
  type ProfileDocument,
  type ReconcileResult,
} from "./reconcileModel";
import { ReconcileExport } from "./reconcileExport";

const TH =
  "whitespace-nowrap px-2 py-1.5 text-left text-[11px] font-medium uppercase tracking-wide text-muted-foreground";
const TD = "whitespace-nowrap px-2 py-1.5 text-xs";
const MONO = "font-mono text-[11px] tabular-nums";

/** The engine's display threshold for a late arrival, in nanoseconds. */
const LATE_THRESHOLD_NS = 5_000_000_000;

const LANE_BLURB: Record<LaneId, string> = {
  only_source: "Left the source with no matching record downstream.",
  only_dest: "Arrived without a matching source record.",
  ambiguous:
    "One identity, several rows on a side — the join emits every pair, because picking one would hide the ambiguity.",
  matched: "Present on both sides.",
};

export function ReconcileResults({
  result,
  source,
  dest,
  exportRequest,
}: {
  result: ReconcileResult;
  source: ProfileDocument | undefined;
  dest: ProfileDocument | undefined;
  exportRequest?: { requestUrl: string; formats: string[]; label: string };
}) {
  const stats = deriveStats(result.rows);
  const groups = groupByKey(result.rows);
  const [lane, setLane] = useState<LaneId>(
    stats.only_source > 0 ? "only_source" : stats.only_dest > 0 ? "only_dest" : "matched",
  );
  const warning = boundWarning(result);
  // The page resets with the lane: page 4 of "Never arrived" names nothing in
  // "Matched", and landing on an empty table reads as an empty lane.
  const [page, setPage] = useState(0);
  const shown = lanePage(laneGroups(groups, lane), page);
  const selectLane = (next: LaneId) => {
    setLane(next);
    setPage(0);
  };

  const lanes: { id: LaneId; label: string; count: number }[] = [
    { id: "only_source", label: "Never arrived", count: stats.only_source },
    { id: "only_dest", label: "No counterpart", count: stats.only_dest },
    { id: "ambiguous", label: "Ambiguous", count: stats.dup_keys },
    { id: "matched", label: "Matched", count: stats.matched },
  ];

  return (
    <div className="flex min-h-0 flex-col gap-3">
      <StatStrip
        columns={4}
        items={[
          {
            label: "Matched",
            value: stats.matched,
            sub: "present on both sides",
            tone: "success",
            icon: UiCheck,
            onClick: () => selectLane("matched"),
          },
          {
            label: "Never arrived",
            value: stats.only_source,
            sub: result.source,
            tone: "danger",
            icon: UiCircleX,
            onClick: () => selectLane("only_source"),
          },
          {
            label: "No counterpart",
            value: stats.only_dest,
            sub: result.dest,
            tone: "warning",
            icon: UiWarningTriangle,
            onClick: () => selectLane("only_dest"),
          },
          {
            label: "Ambiguous keys",
            value: stats.dup_keys,
            sub: "identity repeats",
            tone: "warning",
            icon: UiKey,
            onClick: () => selectLane("ambiguous"),
          },
        ]}
      />

      {warning && (
        <p
          role="status"
          className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 [[data-theme=dark]_&]:text-amber-300"
        >
          <Icon icon={UiWarningTriangle} className="mt-0.5 shrink-0 text-[13px]" />
          {warning}
        </p>
      )}

      <div className="flex flex-wrap items-center justify-between gap-2">
        <SegmentedControl
          value={lane}
          options={lanes.map((entry) => ({ id: entry.id, label: `${entry.label} (${entry.count})` }))}
          onChange={(next) => selectLane(next as LaneId)}
          size="sm"
          wrap
          aria-label="Outcome"
        />
        {exportRequest && <ReconcileExport {...exportRequest} outcome={lane} />}
      </div>

      <div className="min-h-0 flex-1 overflow-auto rounded-lg border border-border">
        <LaneTable lane={lane} groups={shown.groups} source={source} dest={dest} />
      </div>

      <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
        <p>{LANE_BLURB[lane]}</p>
        {shown.pages > 1 && (
          <div className="flex items-center gap-2">
            <span aria-live="polite">
              Keys {shown.page * LANE_PAGE_SIZE + 1}–{shown.page * LANE_PAGE_SIZE + shown.groups.length}{" "}
              of {shown.total}
            </span>
            <button
              type="button"
              className="rounded-md border border-border px-2 py-1 disabled:opacity-50"
              disabled={shown.page === 0}
              onClick={() => setPage(shown.page - 1)}
            >
              Previous
            </button>
            <button
              type="button"
              className="rounded-md border border-border px-2 py-1 disabled:opacity-50"
              disabled={shown.page >= shown.pages - 1}
              onClick={() => setPage(shown.page + 1)}
            >
              Next
            </button>
          </div>
        )}
      </div>
    </div>
  );
}

/** Columns a one-sided lane shows: that side's own fields, minus its clock. */
function sideFields(document: ProfileDocument | undefined): string[] {
  const clock = timestampField(document);
  return profileFields(document).filter((field) => field !== clock);
}

function LaneTable({
  lane,
  groups,
  source,
  dest,
}: {
  lane: LaneId;
  groups: ReturnType<typeof groupByKey>;
  source: ProfileDocument | undefined;
  dest: ProfileDocument | undefined;
}) {
  const oneSided = lane === "only_source" || lane === "only_dest";
  const fields = oneSided ? sideFields(lane === "only_source" ? source : dest) : [];

  return (
    <table className="w-full border-collapse">
      <thead className="sticky top-0 bg-muted/70 backdrop-blur">
        <tr>
          <th className={TH}>Key</th>
          {oneSided ? (
            <>
              <th className={TH}>{lane === "only_source" ? "Sent" : "Seen"}</th>
              {fields.map((field) => (
                <th key={field} className={TH}>
                  {field}
                </th>
              ))}
            </>
          ) : (
            <>
              <th className={TH}>Sent</th>
              <th className={TH}>Seen</th>
              <th className={TH}>Lag</th>
              <th className={TH}>Note</th>
            </>
          )}
        </tr>
      </thead>
      <tbody>
        {groups.map((group) => {
          const row = group.primary;
          const side = lane === "only_source" ? row.source : row.dest;
          return (
            <tr key={group.key} className="border-t border-border/60 hover:bg-muted/30">
              <td className={cn(TD, MONO, group.key === "" && "text-muted-foreground")}>
                {displayKey(group.key)}
              </td>
              {oneSided ? (
                <>
                  <td className={cn(TD, MONO)}>
                    {formatTime(lane === "only_source" ? row.source_time : row.dest_time) || "—"}
                  </td>
                  {fields.map((field) => (
                    <td key={field} className={TD} title={cellText(side, field)}>
                      <span className="block max-w-[16rem] truncate">{cellText(side, field) || "—"}</span>
                    </td>
                  ))}
                </>
              ) : (
                <>
                  <td className={cn(TD, MONO)}>{formatTime(row.source_time) || "—"}</td>
                  <td className={cn(TD, MONO)}>{formatTime(row.dest_time) || "—"}</td>
                  <td className={TD}>
                    <Lag ns={row.time_diff} />
                  </td>
                  <td className={TD}>
                    {isDuplicated(group) ? (
                      <Badge tone="warning" variant="soft" size="md">
                        {group.sourceDupCount}×{group.destDupCount} ambiguous
                      </Badge>
                    ) : (
                      <span className="text-muted-foreground">—</span>
                    )}
                  </td>
                </>
              )}
            </tr>
          );
        })}
        {groups.length === 0 && (
          <tr>
            <td className="px-2 py-8 text-center text-xs text-muted-foreground" colSpan={6}>
              Nothing in this lane.
            </td>
          </tr>
        )}
      </tbody>
    </table>
  );
}

/** The lag, red past the threshold the CLI renderer paints red. */
function Lag({ ns }: { ns: number | undefined }) {
  if (ns == null) return <span className="text-muted-foreground">—</span>;
  const late = Math.abs(ns) >= LATE_THRESHOLD_NS;
  return (
    <span
      className={cn(MONO, late && "font-semibold text-destructive", ns < 0 && !late && "text-blue-600")}
      title={ns < 0 ? "The destination timestamp precedes the source's — the two clocks disagree" : undefined}
    >
      {humanizeDuration(ns)}
    </span>
  );
}
