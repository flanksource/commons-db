import { Button } from "@flanksource/clicky-ui";

import type { ReconcileSnapshot } from "./reconcileModel";

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
  selected,
  onSelected,
  cel,
  onCEL,
  onApply,
  applying,
  error,
}: {
  active: ReconcileSnapshot;
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
          <a className="text-primary underline underline-offset-2" href={`/${active.surface}`}>Open virtual profile</a>
          <span className="font-mono">{active.connection}</span>
        </div>
      </div>
    </details>
  );
}

function csvRecord(values: string[]): string {
  return values.map((value) => /[",\r\n]/.test(value) ? `"${value.replace(/"/g, '""')}"` : value).join(",");
}

function formatIdleAge(nanoseconds: number): string {
  const seconds = Math.floor(nanoseconds / 1_000_000_000);
  if (seconds % 3600 === 0) return `${seconds / 3600}h`;
  if (seconds % 60 === 0) return `${seconds / 60}m`;
  return `${seconds}s`;
}
