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

function csvRecord(values: string[]): string {
  return values.map((value) => /[",\r\n]/.test(value) ? `"${value.replace(/"/g, '""')}"` : value).join(",");
}
