import { Button } from "@flanksource/clicky-ui";

import {
  formatIdleAge,
  type ReconcileProvenance,
  type ReconcileSideExecution,
  type ReconcileSnapshot,
} from "./reconcileModel";

export function snapshotColumnNames(snapshot: ReconcileSnapshot): string[] {
  return snapshot.columns.filter((column) => !column.hidden).map((column) => column.name);
}

export function materializeParams(
  snapshot: ReconcileSnapshot,
  profile: string,
  columns: string[],
  cel: string,
): Record<string, string> {
  if (columns.length === 0) throw new Error("Choose at least one export column");
  return {
    snapshot: snapshot.id,
    profile,
    column: csvRecord(columns),
    ...(cel.trim() ? { cel: cel.trim() } : {}),
  };
}

export function ReconcileResultSettings({
  active,
  virtualProfileHref,
  selected,
  onSelected,
  cel,
  onCEL,
  onApply,
  applying,
  error,
}: {
  active: ReconcileSnapshot;
  /** The snapshot's own surface, carrying the lane and page size being read. */
  virtualProfileHref: string;
  selected: string[];
  onSelected: (columns: string[]) => void;
  cel: string;
  onCEL: (cel: string) => void;
  onApply: () => void;
  applying: boolean;
  error: string;
}) {
  const columns = snapshotColumnNames(active);
  const selectedSet = new Set(selected);
  const toggle = (name: string) => {
    onSelected(
      selectedSet.has(name)
        ? selected.filter((column) => column !== name)
        : columns.filter((column) => selectedSet.has(column) || column === name),
    );
  };

  return (
    <details className="rounded-lg border border-border bg-card">
      <summary className="cursor-pointer px-3 py-2 text-sm font-medium">Results</summary>
      <div className="grid gap-4 border-t border-border p-3 lg:grid-cols-2">
        <section className="space-y-2">
          <div className="flex items-center justify-between gap-2">
            <div>
              <h3 className="text-xs font-semibold">Export columns</h3>
              <p className="text-[11px] text-muted-foreground">Selected order is the snapshot column order.</p>
            </div>
            <div className="flex gap-1">
              <Button size="sm" variant="ghost" onClick={() => onSelected(columns)}>All</Button>
              <Button size="sm" variant="ghost" onClick={() => onSelected([])}>None</Button>
            </div>
          </div>
          <div className="grid max-h-48 gap-1 overflow-auto sm:grid-cols-2">
            {active.columns.filter((column) => !column.hidden).map((column) => (
              <label key={column.name} className="flex items-center gap-2 rounded px-2 py-1 text-xs hover:bg-muted">
                <input
                  type="checkbox"
                  checked={selectedSet.has(column.name)}
                  onChange={() => toggle(column.name)}
                />
                <span className="truncate" title={column.name}>{column.label || column.name}</span>
              </label>
            ))}
          </div>
        </section>

        <section className="space-y-2">
          <div>
            <h3 className="text-xs font-semibold">Transform rows with CEL</h3>
            <p className="text-[11px] text-muted-foreground">
              Return a list of objects from <code className="font-mono">rows</code>. Stats, source and dest are also bound.
            </p>
          </div>
          <textarea
            className="h-24 w-full resize-y rounded border border-border bg-background p-2 font-mono text-[11px]"
            value={cel}
            onChange={(event) => onCEL(event.target.value)}
            placeholder={'dyn(rows).map(row, {"key": row.key, "outcome": row.outcome})'}
            aria-label="Results CEL transformation"
          />
          <div className="flex flex-wrap items-center gap-2">
            <Button size="sm" disabled={applying || !cel.trim()} onClick={onApply}>
              {applying ? "Applying…" : "Apply transformation"}
            </Button>
          </div>
          {error && <p role="alert" className="text-xs text-destructive">{error}</p>}
        </section>

        <div className="flex flex-wrap gap-x-4 gap-y-1 border-t border-border pt-3 text-xs text-muted-foreground lg:col-span-2">
          <span>{active.row_count} rows</span>
          <span>Expires after {formatIdleAge(active.idle_age)} idle</span>
          <a className="text-primary underline underline-offset-2" href={virtualProfileHref}>Open virtual profile</a>
          <span className="font-mono">{active.connection}</span>
        </div>
      </div>
    </details>
  );
}

/**
 * What produced these rows.
 *
 * A reconciliation's findings are only as trustworthy as the two reads behind
 * them, and neither read is visible in the joined rows — "this key never
 * arrived" is a claim about a query that ran once, with filters, at a moment.
 * It sits beside the export panel rather than inside it because that panel is
 * about what to do with the rows, and because this must still render when the
 * row read itself fails.
 */
export function ReconcileProvenancePanel({ provenance }: { provenance: ReconcileProvenance }) {
  const { config, execution } = provenance;
  const key = config.key?.cel || (config.key?.columns ?? []).join(", ");
  return (
    <details className="rounded-lg border border-border bg-card">
      <summary className="cursor-pointer px-3 py-2 text-sm font-medium">Queries</summary>
      <div className="space-y-4 border-t border-border p-3">
        <dl className="grid gap-x-4 gap-y-1 text-xs sm:grid-cols-[auto_minmax(0,1fr)]">
          <Fact label="Destination" value={config.dest} />
          <Fact label="Key" value={key} mono />
          <Fact label="Source filters" value={filterSummary(config.sourceFilters)} mono />
          <Fact label="Destination filters" value={filterSummary(config.destFilters)} mono />
          <Fact label="Key range" value={rangeSummary(config.range)} mono />
          <Fact label="Time column" value={config.timeColumn} mono />
          <Fact label="Join" value={execution?.mode} />
          <Fact label="Buffered because" value={execution?.buffered_reason} />
        </dl>
        <div className="grid gap-3 lg:grid-cols-2">
          <SideQuery label="Source" side={execution?.source} />
          <SideQuery label="Destination" side={execution?.dest} />
        </div>
      </div>
    </details>
  );
}

function SideQuery({ label, side }: { label: string; side: ReconcileSideExecution | undefined }) {
  if (!side) return <p className="text-xs text-muted-foreground">{label}: not recorded</p>;
  const request = side.diagnostics?.request;
  const response = side.diagnostics?.response;
  return (
    <section className="space-y-2 rounded border border-border p-2">
      <h3 className="text-xs font-semibold">
        {label} · <span className="font-mono font-normal">{side.profile}</span>
      </h3>
      <dl className="grid gap-x-3 gap-y-0.5 text-[11px] sm:grid-cols-[auto_minmax(0,1fr)]">
        <Fact label="Provider" value={side.provider} />
        <Fact label="Connection" value={request?.connection} mono />
        <Fact label="Rows" value={String(side.rows)} />
        <Fact label="Pages" value={side.pages ? String(side.pages) : ""} />
        <Fact label="Backend" value={side.backendMs ? `${Math.round(side.backendMs)}ms` : ""} />
        <Fact label="Truncated" value={side.truncated ? "yes" : ""} />
      </dl>
      {/* The statement the backend was sent, not the template it came from — a
          bad parameter and a bad template look identical until you see both. */}
      <QueryText label="Sent" value={request?.query} />
      {request?.rendered && request.rendered !== request.query && (
        <QueryText label="Rendered" value={request.rendered} />
      )}
      {side.query && side.query !== request?.rendered && <QueryText label="Stored" value={side.query} />}
      {request?.arguments && request.arguments.length > 0 && (
        <QueryText label="Arguments" value={JSON.stringify(request.arguments)} />
      )}
      {side.diagnostics?.error && <p className="text-[11px] text-destructive">{side.diagnostics.error}</p>}
      {response?.truncated && (
        <p className="text-[11px] text-amber-700 [[data-theme=dark]_&]:text-amber-300">
          The backend cut this read short.
        </p>
      )}
    </section>
  );
}

function QueryText({ label, value }: { label: string; value: string | undefined }) {
  if (!value) return null;
  return (
    <div className="space-y-0.5">
      <div className="text-[10px] uppercase tracking-wide text-muted-foreground">{label}</div>
      <pre className="max-h-40 overflow-auto rounded border border-border bg-background p-2 font-mono text-[11px]">
        {value}
      </pre>
    </div>
  );
}

function Fact({ label, value, mono = false }: { label: string; value?: string; mono?: boolean }) {
  if (!value) return null;
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className={mono ? "truncate font-mono" : "truncate"} title={value}>{value}</dd>
    </>
  );
}

function filterSummary(filters: Record<string, string> | undefined): string {
  return Object.entries(filters ?? {})
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([key, value]) => `${key}=${value}`)
    .join(", ");
}

function rangeSummary(range: { from?: string; to?: string } | undefined): string {
  if (!range?.from && !range?.to) return "";
  return `${range.from ?? "…"} → ${range.to ?? "…"}`;
}

function csvRecord(values: string[]): string {
  return values.map((value) => /[",\r\n]/.test(value) ? `"${value.replace(/"/g, '""')}"` : value).join(",");
}
