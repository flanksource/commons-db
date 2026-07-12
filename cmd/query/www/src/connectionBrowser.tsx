import {
  CacheBrowser,
  LogsTable,
  QueryBrowser,
  TimeseriesPanel,
  type EntityDetailBodyRenderContext,
  type JsonSchemaObject,
  type QueryBrowserResult,
  type TimeseriesResponse,
  type TimeseriesSeries,
} from "@flanksource/clicky-ui";
import { useQuery } from "@tanstack/react-query";
import { useMemo, useState, type ReactNode } from "react";

type BrowserDescriptor = {
  kind: "query" | "cache";
  provider?: string;
  language?: "sql" | "json" | "text";
  queryLabel?: string;
  defaultQuery?: string;
  resultView?: "table" | "logs" | "timeseries";
  optionsSchema?: JsonSchemaObject;
  initialOptions?: Record<string, unknown>;
  catalog?: boolean;
};

type CatalogNode = {
  id: string;
  label: string;
  kind: string;
  query?: string;
  options?: Record<string, unknown>;
  children?: CatalogNode[];
};

async function fetchJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, init);
  if (!response.ok) {
    const body = await response.text();
    throw new Error(body.trim() || `request failed: ${response.status}`);
  }
  return response.json() as Promise<T>;
}

export function connectionDetailBodyRenderer(context: EntityDetailBodyRenderContext): ReactNode {
  if (context.surfaceKey !== "connection") return context.defaultView;
  return <ConnectionBrowser id={context.id} fallback={context.defaultView} />;
}

function ConnectionBrowser({ id, fallback }: { id: string; fallback: ReactNode }) {
  const baseUrl = `/api/v1/connection/${encodeURIComponent(id)}/browser`;
  const descriptor = useQuery({
    queryKey: ["connection-browser", id],
    queryFn: async () => {
      const response = await fetch(baseUrl);
      if (response.status === 404) return null;
      if (!response.ok) throw new Error((await response.text()).trim() || `request failed: ${response.status}`);
      return response.json() as Promise<BrowserDescriptor>;
    },
    retry: 0,
  });

  if (descriptor.isLoading) {
    return <div className="rounded-xl border bg-card p-6 text-sm text-muted-foreground">Loading connection browser…</div>;
  }
  if (descriptor.isError) {
    return (
      <div className="rounded-xl border border-destructive/40 bg-destructive/5 p-4 text-sm text-destructive">
        {descriptor.error instanceof Error ? descriptor.error.message : "Failed to load connection browser"}
      </div>
    );
  }
  if (!descriptor.data) return fallback;
  if (descriptor.data.kind === "cache") {
    return (
      <div className="h-[calc(100vh-15rem)] min-h-[32rem] overflow-hidden rounded-xl border bg-card">
        <CacheBrowser baseUrl={baseUrl} />
      </div>
    );
  }
  return <ConnectionQueryBrowser id={id} baseUrl={baseUrl} descriptor={descriptor.data} />;
}

function ConnectionQueryBrowser({
  id,
  baseUrl,
  descriptor,
}: {
  id: string;
  baseUrl: string;
  descriptor: BrowserDescriptor;
}) {
  const catalog = useQuery({
    queryKey: ["connection-browser-catalog", id],
    queryFn: () => fetchJSON<{ nodes: CatalogNode[] }>(`${baseUrl}/catalog`),
    enabled: descriptor.catalog === true,
    retry: 0,
  });
  const [selection, setSelection] = useState<{
    query?: string;
    options?: Record<string, unknown>;
  }>({});
  const options = useMemo(
    () => ({ ...(descriptor.initialOptions ?? {}), ...(selection.options ?? {}) }),
    [descriptor.initialOptions, selection.options],
  );

  return (
    <QueryBrowser
      id={`${descriptor.provider ?? "query"}:${id}`}
      title={`${descriptor.queryLabel ?? "Query"} browser`}
      language={descriptor.language ?? "text"}
      queryLabel={descriptor.queryLabel ?? "Query"}
      initialQuery={selection.query ?? descriptor.defaultQuery ?? ""}
      optionsSchema={descriptor.optionsSchema}
      initialOptions={options}
      navigator={
        descriptor.catalog ? (
          <CatalogTree
            nodes={catalog.data?.nodes ?? []}
            loading={catalog.isLoading}
            error={catalog.error}
            onSelect={(node) => setSelection({ query: node.query, options: node.options })}
          />
        ) : undefined
      }
      execute={(request) =>
        fetchJSON<QueryBrowserResult>(`${baseUrl}/query`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(request),
        })
      }
      renderResults={
        descriptor.resultView === "logs"
          ? ({ result, defaultView }) =>
              result.rows?.length ? (
                <LogsTable logs={result.rows} autoFilter={false} fullscreenTitle="Logs" />
              ) : (
                defaultView
              )
          : descriptor.resultView === "timeseries"
            ? ({ result, defaultView }) => <PrometheusResults result={result} fallback={defaultView} />
            : undefined
      }
    />
  );
}

function CatalogTree({
  nodes,
  loading,
  error,
  onSelect,
}: {
  nodes: CatalogNode[];
  loading: boolean;
  error: unknown;
  onSelect: (node: CatalogNode) => void;
}) {
  return (
    <div className="flex h-full min-h-0 flex-col overflow-auto border-r bg-card p-2">
      <h3 className="px-2 py-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">Catalog</h3>
      {loading && <div className="p-2 text-xs text-muted-foreground">Loading catalog…</div>}
      {error ? <div className="p-2 text-xs text-destructive">Catalog unavailable</div> : null}
      <CatalogNodes nodes={nodes} depth={0} onSelect={onSelect} />
    </div>
  );
}

function CatalogNodes({ nodes, depth, onSelect }: { nodes: CatalogNode[]; depth: number; onSelect: (node: CatalogNode) => void }) {
  return (
    <div>
      {nodes.map((node) => (
        <div key={node.id}>
          <button
            type="button"
            disabled={!node.query}
            onClick={() => onSelect(node)}
            className="flex w-full items-center rounded px-2 py-1 text-left text-xs hover:bg-accent disabled:cursor-default disabled:text-muted-foreground"
            style={{ paddingLeft: `${8 + depth * 14}px` }}
            title={node.query ? `Load ${node.kind}` : node.kind}
          >
            <span className="truncate">{node.label}</span>
          </button>
          {node.children?.length ? <CatalogNodes nodes={node.children} depth={depth + 1} onSelect={onSelect} /> : null}
        </div>
      ))}
    </div>
  );
}

function PrometheusResults({ result, fallback }: { result: QueryBrowserResult; fallback: ReactNode }) {
  const chart = useMemo(() => prometheusSeries(result.rows ?? []), [result.rows]);
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
  const withTime = rows.filter((row) => row.timestamp != null && typeof row.value === "number");
  if (withTime.length < 2) return null;
  const groups = new Map<string, { label: string; points: { at: string; value: number }[] }>();
  for (const row of withTime) {
    const labels = Object.entries(row)
      .filter(([key]) => key !== "timestamp" && key !== "value")
      .sort(([a], [b]) => a.localeCompare(b));
    const label = labels.map(([key, value]) => `${key}=${String(value)}`).join(", ") || "value";
    const group = groups.get(label) ?? { label, points: [] };
    group.points.push({ at: new Date(String(row.timestamp)).toISOString(), value: Number(row.value) });
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
