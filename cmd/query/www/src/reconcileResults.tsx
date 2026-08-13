import { useCallback, useEffect, useMemo, useState } from "react";
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
import { useMutation, useQuery } from "@tanstack/react-query";

import {
  materializeParams,
  ReconcileProvenancePanel,
  ReconcileResultSettings,
  snapshotColumnNames,
} from "./reconcileExport";
import { virtualProfileHref, type LaneId, type ReconcileSnapshot, type ResultsView } from "./reconcileModel";

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

type Materialize = (
  root: ReconcileSnapshot,
  profile: string,
  columns: string[],
  cel: string,
) => Promise<ReconcileSnapshot>;

type PreparedDownloadOptions = ClickyDownloadOptions & {
  prepare: (request: {
    format: ClickyRemoteFormat;
    scope?: ClickyDownloadScope;
  }) => Promise<{ url: string; label?: string }>;
};

export function snapshotPageParams(lane: LaneId, page: number, pageSize: number): Record<string, string> {
  return {
    "filter.outcome": lane,
    limit: String(pageSize),
    offset: String(page * pageSize),
  };
}

export async function prepareSnapshotDownload(
  materialize: Materialize,
  root: ReconcileSnapshot,
  active: ReconcileSnapshot,
  columns: string[],
): Promise<{ url: string; label: string }> {
  const projected = await materialize(root, active.profile, columns, "");
  return { url: projected.url, label: `${root.source} to ${root.dest} reconciliation` };
}

export function ReconcileResults({
  client,
  snapshot,
  materializeAction,
  view,
  onView,
}: {
  client: OperationsApiClient;
  snapshot: ReconcileSnapshot;
  materializeAction: ResolvedOperation;
  /** Lane and position, owned by the route so both live in the URL. */
  view: ResultsView;
  onView: (view: ResultsView) => void;
}) {
  const [active, setActive] = useState(snapshot);
  const [selected, setSelected] = useState(() => snapshotColumnNames(snapshot));
  const [cel, setCEL] = useState("");
  const { lane, page, pageSize } = view;

  useEffect(() => {
    setActive(snapshot);
    setSelected(snapshotColumnNames(snapshot));
    setCEL("");
  }, [snapshot]);

  const materialize: Materialize = useCallback(async (root, profile, columns, expression) => {
    const idParam = materializeAction.operation["x-clicky"]?.idParam ?? "id";
    const response = await client.executeCommand(
      materializeAction.path,
      materializeAction.method,
      { [idParam]: profile, ...materializeParams(root, profile, columns, expression) },
      { Accept: "application/json" },
    );
    if (!response.success) {
      throw new Error(response.error ?? response.message ?? "Materializing reconciliation results failed");
    }
    if (!isSnapshot(response.parsed)) throw new Error("The materialize action returned no snapshot profile");
    return response.parsed;
  }, [client, materializeAction]);

  const result = useQuery({
    // Distinct from ["reconcile-snapshot", <id>], which holds the descriptor:
    // one invalidateQueries prefix would otherwise hit both.
    queryKey: ["reconcile-snapshot-page", active.profile, lane, page, pageSize],
    queryFn: async () => {
      const response = await client.executeCommand(
        active.url,
        "get",
        snapshotPageParams(lane, page, pageSize),
        { Accept: "application/json+clicky" },
      );
      if (!response.success) {
        throw new Error(response.error ?? response.message ?? "Loading reconciliation results failed");
      }
      if (!response.parsed || typeof response.parsed !== "object") {
        throw new Error("The reconciliation profile returned no Clicky result");
      }
      return response;
    },
    retry: false,
  });

  const transform = useMutation({
    mutationFn: () => materialize(snapshot, active.profile, snapshotColumnNames(active), cel),
    onSuccess: (transformed) => {
      setActive(transformed);
      setSelected(snapshotColumnNames(transformed));
      onView({ ...view, page: 0 });
      setCEL("");
    },
  });

  const download = useMemo<PreparedDownloadOptions>(() => ({
    label: "Export",
    formats: DOWNLOAD_FORMATS,
    scopes: ["all"],
    allRowsMode: "streaming",
    prepare: () => prepareSnapshotDownload(materialize, snapshot, active, selected),
  }), [active, materialize, selected, snapshot]);

  const selectLane = (next: LaneId) => onView({ ...view, lane: next, page: 0 });
  const warning = snapshotWarning(snapshot);
  const stats = snapshot.stats;

  return (
    <div className="flex min-h-0 flex-col gap-3">
      <StatStrip
        columns={4}
        items={[
          { label: "Matched", value: stats.matched, sub: "present on both sides", tone: "success", icon: UiCheck, onClick: () => selectLane("matched") },
          { label: "Never arrived", value: stats.only_source, sub: snapshot.source, tone: "danger", icon: UiCircleX, onClick: () => selectLane("only_source") },
          { label: "No counterpart", value: stats.only_dest, sub: snapshot.dest, tone: "warning", icon: UiWarningTriangle, onClick: () => selectLane("only_dest") },
          { label: "Ambiguous keys", value: stats.dup_keys, sub: "identity repeats", tone: "warning", icon: UiKey, onClick: () => selectLane("ambiguous") },
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
          onChange={(next) => selectLane(next as LaneId)}
          size="sm"
          wrap
          aria-label="Outcome"
        />
        <span className="text-xs text-muted-foreground">{snapshot.row_count} joined rows in snapshot</span>
      </div>

      <ReconcileResultSettings
        active={active}
        virtualProfileHref={virtualProfileHref(active, view)}
        selected={selected}
        onSelected={setSelected}
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
          <Clicky
            data={result.data.parsed as ClickyNode}
            url={result.data.requestUrl ?? active.url}
            download={download}
            className="h-full min-h-0"
            pagination={{
              page,
              pageSize,
              ...(result.data.pagination?.total !== undefined ? { total: result.data.pagination.total } : {}),
              ...(result.data.pagination?.totalRelation !== undefined ? { totalRelation: result.data.pagination.totalRelation } : {}),
              ...(result.data.pagination?.hasMore !== undefined ? { hasMore: result.data.pagination.hasMore } : {}),
              onPageChange: (next) => onView({ ...view, page: next }),
              onPageSizeChange: (next) => onView({ ...view, pageSize: next, page: 0 }),
            }}
          />
        )}
      </div>
    </div>
  );
}

function snapshotWarning(snapshot: ReconcileSnapshot): string {
  const side = snapshot.source_truncated && snapshot.dest_truncated
    ? "both sides were"
    : snapshot.source_truncated ? "the source was" : snapshot.dest_truncated ? "the destination was" : "";
  return side ? `${side} cut short, so one-sided keys may be outside the reconciliation window.` : "";
}

function isSnapshot(value: unknown): value is ReconcileSnapshot {
  if (!value || typeof value !== "object") return false;
  const candidate = value as Partial<ReconcileSnapshot>;
  return typeof candidate.id === "string" && typeof candidate.profile === "string" &&
    typeof candidate.url === "string" && Array.isArray(candidate.columns);
}

function ResultMessage({ children, error = false }: { children: React.ReactNode; error?: boolean }) {
  return <div className={`grid min-h-64 place-items-center p-8 text-sm ${error ? "text-destructive" : "text-muted-foreground"}`}>{children}</div>;
}
