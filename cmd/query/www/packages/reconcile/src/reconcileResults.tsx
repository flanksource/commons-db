import { useMemo, useState } from "react";
import {
  Icon,
  SegmentedControl,
  StatStrip,
  type OperationsApiClient,
  type ResolvedOperation,
} from "@flanksource/clicky-ui";
import {
  Clicky,
  type ClickyDownloadOptions,
  type ClickyDownloadScope,
  type ClickyNode,
  type ClickyRemoteFormat,
} from "@flanksource/clicky-ui/clicky";
import { UiCheck, UiCircleX, UiKey, UiWarningTriangle } from "@flanksource/clicky-ui/icons";
import { useMutation, useQueryClient } from "@tanstack/react-query";

import { ReconcileProvenancePanel, ReconcileResultSettings } from "./reconcileExport";
import { snapshotColumnNames } from "./reconcileExportParams";
import { type LaneId, type ReconcileSnapshot, type ResultsView } from "./reconcileModel";
import {
  prepareSnapshotDownload,
  SNAPSHOT_PAGE_QUERY_KEY,
  useMaterialize,
  useSnapshotPage,
  type SnapshotPageResponse,
} from "./reconcileSnapshotQuery";

const DOWNLOAD_FORMATS: ClickyRemoteFormat[] = [
  "json",
  "ndjson",
  "yaml",
  "csv",
  "markdown",
  "html",
  "pdf",
  "excel",
];

type PreparedDownloadOptions = ClickyDownloadOptions & {
  prepare: (request: {
    format: ClickyRemoteFormat;
    scope?: ClickyDownloadScope;
  }) => Promise<{ url: string; label?: string }>;
};

type ReconcileResultsProps = {
  client: OperationsApiClient;
  snapshot: ReconcileSnapshot;
  materializeAction: ResolvedOperation;
  /** Lane and position, owned by the route so both live in the URL. */
  view: ResultsView;
  onView: (view: ResultsView) => void;
  profileHref: (snapshot: ReconcileSnapshot, view: ResultsView) => string;
};

/**
 * A snapshot is immutable for its whole life, so a different id is a different
 * run: the transformation, column choice and CEL typed against the previous one
 * start over with it.
 */
export function ReconcileResults(props: ReconcileResultsProps) {
  return <ReconcileResultsView key={props.snapshot.id} {...props} />;
}

function ReconcileResultsView({
  client,
  snapshot,
  materializeAction,
  view,
  onView,
  profileHref,
}: ReconcileResultsProps) {
  const queryClient = useQueryClient();
  // What the reader changed on top of the snapshot; the snapshot itself stays
  // the source of truth until a transformation replaces it.
  const [transformed, setTransformed] = useState<ReconcileSnapshot>();
  const [chosenColumns, setChosenColumns] = useState<string[]>();
  const active = transformed ?? snapshot;
  const activeColumns = useMemo(() => snapshotColumnNames(active), [active]);
  const selected = chosenColumns ?? activeColumns;
  const [cel, setCEL] = useState("");

  const materialize = useMaterialize(client, materializeAction);
  const result = useSnapshotPage({ client, active, view });

  const transform = useMutation({
    mutationFn: () => materialize(snapshot, active.profile, activeColumns, cel),
    onSuccess: async (projection) => {
      setTransformed(projection);
      setChosenColumns(undefined);
      onView({ ...view, page: 0 });
      setCEL("");
      // The projection is materialized server-side, so any cached page of a
      // profile by that name may predate it. Marked stale rather than
      // refetched: the page on screen is replaced by the projection's own fetch.
      await queryClient.invalidateQueries({
        queryKey: [SNAPSHOT_PAGE_QUERY_KEY],
        refetchType: "none",
      });
    },
  });

  const download = useMemo<PreparedDownloadOptions>(() => ({
    label: "Export",
    formats: DOWNLOAD_FORMATS,
    scopes: ["all"],
    allRowsMode: "streaming",
    prepare: () => prepareSnapshotDownload(materialize, snapshot, active, selected),
  }), [active, materialize, selected, snapshot]);

  return (
    <div className="flex min-h-0 flex-col gap-3">
      <SnapshotSummary
        snapshot={snapshot}
        lane={view.lane}
        onLane={(next) => onView({ ...view, lane: next, page: 0 })}
      />

      <ReconcileResultSettings
        active={active}
        virtualProfileHref={profileHref(active, view)}
        selected={selected}
        onSelected={setChosenColumns}
        cel={cel}
        onCEL={setCEL}
        onApply={() => transform.mutate()}
        applying={transform.isPending}
        error={transform.error instanceof Error ? transform.error.message : ""}
      />

      {/* Reads the run, not `active`: a CEL transform swaps `active` for a
          projection whose provenance is still the root run's. */}
      {snapshot.reconcile && <ReconcileProvenancePanel provenance={snapshot.reconcile} />}

      <div className="min-h-64 flex-1 overflow-auto rounded-lg border border-border">
        {result.isLoading && <ResultMessage>Loading snapshot page…</ResultMessage>}
        {result.error instanceof Error && <ResultMessage error>{result.error.message}</ResultMessage>}
        {result.data && (
          <SnapshotPageTable
            response={result.data}
            fallbackUrl={active.url}
            download={download}
            view={view}
            onView={onView}
          />
        )}
      </div>
    </div>
  );
}

function SnapshotSummary({
  snapshot,
  lane,
  onLane,
}: {
  snapshot: ReconcileSnapshot;
  lane: LaneId;
  onLane: (lane: LaneId) => void;
}) {
  const warning = snapshotWarning(snapshot);
  const stats = snapshot.stats;
  return (
    <>
      <StatStrip
        columns={4}
        items={[
          { label: "Matched", value: stats.matched, sub: "present on both sides", tone: "success", icon: UiCheck, onClick: () => onLane("matched") },
          { label: "Never arrived", value: stats.only_source, sub: snapshot.source, tone: "danger", icon: UiCircleX, onClick: () => onLane("only_source") },
          { label: "No counterpart", value: stats.only_dest, sub: snapshot.dest, tone: "warning", icon: UiWarningTriangle, onClick: () => onLane("only_dest") },
          { label: "Ambiguous keys", value: stats.dup_keys, sub: "identity repeats", tone: "warning", icon: UiKey, onClick: () => onLane("ambiguous") },
        ]}
      />

      {warning && (
        <p role="status" className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 [[data-theme=dark]_&]:text-amber-300">
          <Icon icon={UiWarningTriangle} className="mt-0.5 shrink-0 text-[13px]" />
          {warning}
        </p>
      )}

      <div className="flex flex-wrap items-center justify-between gap-2">
        <SegmentedControl
          value={lane}
          options={[
            { id: "only_source", label: `Never arrived (${stats.only_source})` },
            { id: "only_dest", label: `No counterpart (${stats.only_dest})` },
            { id: "ambiguous", label: `Ambiguous (${stats.dup_keys})` },
            { id: "matched", label: `Matched (${stats.matched})` },
          ]}
          onChange={(next) => onLane(next as LaneId)}
          size="sm"
          wrap
          aria-label="Outcome"
        />
        <span className="text-xs text-muted-foreground">{snapshot.row_count} joined rows in snapshot</span>
      </div>
    </>
  );
}

function SnapshotPageTable({
  response,
  fallbackUrl,
  download,
  view,
  onView,
}: {
  response: SnapshotPageResponse;
  fallbackUrl: string;
  download: PreparedDownloadOptions;
  view: ResultsView;
  onView: (view: ResultsView) => void;
}) {
  const { page, pageSize } = view;
  const paging = response.pagination;
  return (
    <Clicky
      data={response.parsed as ClickyNode}
      url={response.requestUrl ?? fallbackUrl}
      download={download}
      className="h-full min-h-0"
      sort={view.sort ? { key: view.sort, dir: view.desc ? "desc" : "asc" } : null}
      // A new order is a new sequence, so the reader returns to its start
      // rather than to the offset they held in the previous one.
      onSortChange={(next) =>
        onView({
          ...view,
          page: 0,
          ...(next ? { sort: next.key, desc: next.dir === "desc" } : { sort: undefined, desc: undefined }),
        })
      }
      pagination={{
        page,
        pageSize,
        ...(paging?.total !== undefined ? { total: paging.total } : {}),
        ...(paging?.totalRelation !== undefined ? { totalRelation: paging.totalRelation } : {}),
        ...(paging?.hasMore !== undefined ? { hasMore: paging.hasMore } : {}),
        onPageChange: (next) => onView({ ...view, page: next }),
        onPageSizeChange: (next) => onView({ ...view, pageSize: next, page: 0 }),
      }}
    />
  );
}

function snapshotWarning(snapshot: ReconcileSnapshot): string {
  const side = snapshot.source_truncated && snapshot.dest_truncated
    ? "both sides were"
    : snapshot.source_truncated ? "the source was" : snapshot.dest_truncated ? "the destination was" : "";
  return side ? `${side} cut short, so one-sided keys may be outside the reconciliation window.` : "";
}

function ResultMessage({ children, error = false }: { children: React.ReactNode; error?: boolean }) {
  return <div className={`grid min-h-64 place-items-center p-8 text-sm ${error ? "text-destructive" : "text-muted-foreground"}`}>{children}</div>;
}
