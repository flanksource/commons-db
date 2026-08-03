import {
  TimeseriesPanel,
  type QueryBrowserResult,
  type TimeseriesResponse,
  type TimeseriesSeries,
} from "@flanksource/clicky-ui";
import { useMemo, type ReactNode } from "react";

export function PrometheusResults({
  result,
  fallback,
}: {
  result: QueryBrowserResult;
  fallback: ReactNode;
}) {
  const chart = useMemo(
    () => prometheusSeries(result.rows ?? []),
    [result.rows],
  );
  if (!chart) return fallback;
  return (
    <div className="space-y-3">
      <TimeseriesPanel
        title="Prometheus query"
        baseUrl="/query-browser/"
        series={chart.series}
        refreshMs={0}
        height={240}
        fetcher={async (url) => {
          const id = url.split("?")[0]?.split("/").filter(Boolean).pop() ?? "";
          return chart.responses[id] ?? { id, points: [] };
        }}
      />
      {fallback}
    </div>
  );
}

function prometheusSeries(rows: Record<string, unknown>[]): {
  series: TimeseriesSeries[];
  responses: Record<string, TimeseriesResponse>;
} | null {
  const withTime = rows.filter(
    (row) => row.timestamp != null && typeof row.value === "number",
  );
  if (withTime.length < 2) return null;
  const groups = new Map<
    string,
    { label: string; points: { at: string; value: number }[] }
  >();
  for (const row of withTime) {
    const labels = Object.entries(row)
      .filter(([key]) => key !== "timestamp" && key !== "value")
      .sort(([a], [b]) => a.localeCompare(b));
    const label =
      labels.map(([key, value]) => `${key}=${String(value)}`).join(", ") ||
      "value";
    const group = groups.get(label) ?? { label, points: [] };
    group.points.push({
      at: new Date(String(row.timestamp)).toISOString(),
      value: Number(row.value),
    });
    groups.set(label, group);
  }
  const series: TimeseriesSeries[] = [];
  const responses: Record<string, TimeseriesResponse> = {};
  [...groups.values()].forEach((group, index) => {
    const id = `series-${index}`;
    series.push({ id, label: group.label });
    responses[id] = { id, points: group.points };
  });
  return { series, responses };
}
