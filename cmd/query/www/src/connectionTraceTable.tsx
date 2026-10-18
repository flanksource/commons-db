import {
  formatDuration,
  keyTraceRows,
  type TraceRow,
} from "./connectionTraceApi";

const HEADINGS = [
  "Time",
  "Event",
  "Statement",
  "Duration",
  "CPU",
  "Reads",
  "Writes",
  "Rows",
  "Database",
  "User",
  "Application",
];

function TraceRowCells({ row }: { row: TraceRow }) {
  return (
    <>
      <td className="whitespace-nowrap px-3 py-2">{row.timestamp ?? "—"}</td>
      <td className="px-3 py-2">
        {row.name ?? (row.error_message ? "Warning" : "—")}
      </td>
      <td className="max-w-xl whitespace-pre-wrap px-3 py-2 font-mono">
        {row.error_message || row.statement || "—"}
      </td>
      <td className="whitespace-nowrap px-3 py-2">
        {formatDuration(row.duration)}
      </td>
      <td className="whitespace-nowrap px-3 py-2">
        {formatDuration(row.cpu_time)}
      </td>
      <td className="px-3 py-2">{row.logical_reads ?? "—"}</td>
      <td className="px-3 py-2">{row.writes ?? "—"}</td>
      <td className="px-3 py-2">{row.row_count ?? "—"}</td>
      <td className="px-3 py-2">{row.database_name ?? "—"}</td>
      <td className="px-3 py-2">{row.username ?? "—"}</td>
      <td className="px-3 py-2">{row.client_app_name ?? "—"}</td>
    </>
  );
}

export function TraceTable({ rows, idle }: { rows: TraceRow[]; idle: boolean }) {
  const keyed = keyTraceRows(rows);
  return (
    <div className="min-h-0 flex-1 overflow-auto rounded-md border">
      <table className="w-full min-w-[1100px] text-left text-xs">
        <thead className="sticky top-0 bg-muted">
          <tr>
            {HEADINGS.map((heading) => (
              <th key={heading} className="px-3 py-2 font-medium">
                {heading}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.length ? (
            keyed.map(({ key, row }) => (
              <tr
                key={key}
                className={`border-t align-top ${row.error_message ? "bg-amber-500/10" : ""}`}
              >
                <TraceRowCells row={row} />
              </tr>
            ))
          ) : (
            <tr>
              <td
                colSpan={HEADINGS.length}
                className="px-3 py-12 text-center text-muted-foreground"
              >
                {idle
                  ? "Configure filters and start a trace."
                  : "No trace events yet."}
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}
