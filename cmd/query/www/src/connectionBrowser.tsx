import {
  CacheBrowser,
  Combobox,
  Icon,
  LogsTable,
  QueryBrowser,
  TimeseriesPanel,
  TreeNode,
  type EntityDetailBodyRenderContext,
  type EntityDetailHeaderRenderContext,
  type JsonSchemaObject,
  type ComboboxOption,
  type QueryBrowserCompletion,
  type QueryBrowserResult,
  type TimeseriesResponse,
  type TimeseriesSeries,
} from "@flanksource/clicky-ui";
import {
  UiActivity,
  UiDatabase,
  UiLink,
  UiNamespace,
  UiSqlColumn,
  UiSqlDatabase,
  UiSqlIndex,
  UiSqlView,
  UiTable,
} from "@flanksource/clicky-ui/icons";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useMemo, useState, type ReactNode } from "react";

export type BrowserDescriptor = {
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

type ConnectionPresence = {
  configured: boolean;
  resolved: boolean;
};

type ConnectionInfo = {
  connection: {
    name: string;
    type: string;
    namespace?: string;
    configuredEndpoint?: string;
    resolvedEndpoint?: string;
    configuredUsername?: string;
    resolvedUsername?: string;
    password: ConnectionPresence;
    certificate: ConnectionPresence;
  };
  server: {
    status: "available" | "unavailable" | "error";
    product?: string;
    version?: string;
    database?: string;
    user?: string;
    cluster?: string;
    node?: string;
    details?: Record<string, string>;
    message?: string;
  };
  discoveredAt: string;
};

export type CatalogNode = {
  id: string;
  label: string;
  kind: string;
  query?: string;
  options?: Record<string, unknown>;
  children?: CatalogNode[];
};

type InspectionField = {
  name: string;
  dataType?: string;
  types?: string[];
  searchable?: boolean;
  aggregatable?: boolean;
  conflicting?: boolean;
};

export type BrowserInspection = {
  kind: "sql" | "opensearch";
  dialect?: "postgresql" | "mysql" | "mssql" | "standard";
  database?: string;
  databases?: string[];
  defaultSchema?: string;
  schemas?: {
    name: string;
    relations: {
      name: string;
      type?: "table" | "view";
      columns: InspectionField[];
    }[];
  }[];
  targets?: { name: string; kind: "index" | "alias" | "data_stream" }[];
  nodes?: CatalogNode[];
  selected?: {
    target: { name: string; kind: "index" | "alias" | "data_stream" };
    fields: InspectionField[];
  };
  truncated?: boolean;
  truncateReason?: string;
};

export type ConnectionProfileActionRenderer = (context: {
  connectionName: string;
  providerType: string;
  providerOptions?: Record<string, unknown>;
}) => ReactNode;

export function openSearchIndexOptions(
  inspection?: BrowserInspection,
): ComboboxOption[] {
  if (inspection?.kind !== "opensearch") return [];
  return (inspection.targets ?? []).map((target) => ({
    value: target.name,
    label: target.name,
    group:
      target.kind === "data_stream"
        ? "Data streams"
        : target.kind === "alias"
          ? "Aliases"
          : "Indexes",
    title: `${target.name} · ${target.kind.replace("_", " ")}`,
  }));
}

export function queryBrowserOptionsSchema(
  descriptor: BrowserDescriptor,
): JsonSchemaObject | undefined {
  if (descriptor.provider !== "opensearch" || !descriptor.optionsSchema) {
    return descriptor.optionsSchema;
  }
  const properties = { ...(descriptor.optionsSchema.properties ?? {}) };
  delete properties.index;
  return { ...descriptor.optionsSchema, properties };
}

export function completionForInspection(
  inspection?: BrowserInspection,
  selectedInspection?: BrowserInspection,
): QueryBrowserCompletion | undefined {
  if (inspection?.kind === "sql" && inspection.dialect) {
    return {
      kind: "sql",
      dialect: inspection.dialect,
      ...(inspection.defaultSchema
        ? { defaultSchema: inspection.defaultSchema }
        : {}),
      schemas: (inspection.schemas ?? []).map((schema) => ({
        name: schema.name,
        relations: schema.relations.map((relation) => ({
          name: relation.name,
          ...(relation.type ? { type: relation.type } : {}),
          columns: relation.columns.map((column) => ({
            name: column.name,
            types: column.dataType ? [column.dataType] : [],
          })),
        })),
      })),
    };
  }
  if (
    selectedInspection?.kind === "opensearch" &&
    selectedInspection.selected
  ) {
    return {
      kind: "json-fields",
      vocabulary: "opensearch",
      fields: selectedInspection.selected.fields,
    };
  }
  return undefined;
}

export async function fetchJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, init);
  if (!response.ok) {
    const body = await response.text();
    throw new Error(body.trim() || `request failed: ${response.status}`);
  }
  return response.json() as Promise<T>;
}

export function connectionDetailBodyRenderer(
  context: EntityDetailBodyRenderContext,
  renderProfileAction?: ConnectionProfileActionRenderer,
): ReactNode {
  if (context.surfaceKey !== "connection") return context.defaultView;
  const connectionName =
    typeof context.entity?.name === "string" ? context.entity.name : context.id;
  return (
    <ConnectionBrowser
      id={context.id}
      connectionName={connectionName}
      fallback={context.defaultView}
      renderProfileAction={renderProfileAction}
    />
  );
}

function ConnectionBrowser({
  id,
  connectionName,
  fallback,
  renderProfileAction,
}: {
  id: string;
  connectionName: string;
  fallback: ReactNode;
  renderProfileAction?: ConnectionProfileActionRenderer;
}) {
  const baseUrl = `/api/v1/connection/${encodeURIComponent(id)}/browser`;
  const descriptor = useQuery({
    queryKey: ["connection-browser", id],
    queryFn: async () => {
      const response = await fetch(baseUrl);
      if (response.status === 404) return null;
      if (!response.ok)
        throw new Error(
          (await response.text()).trim() ||
            `request failed: ${response.status}`,
        );
      return response.json() as Promise<BrowserDescriptor>;
    },
    retry: 0,
  });
  const inspection = useQuery({
    queryKey: ["connection-browser-inspection", id],
    queryFn: () => fetchJSON<BrowserInspection>(`${baseUrl}/inspect`),
    enabled:
      descriptor.data?.provider === "opensearch" &&
      descriptor.data.catalog === true,
    retry: 0,
    staleTime: 5 * 60_000,
  });
  const [selectedOpenSearchIndex, setSelectedOpenSearchIndex] = useState("");
  const profileOptions = useMemo(
    () =>
      selectedOpenSearchIndex
        ? { index: selectedOpenSearchIndex }
        : undefined,
    [selectedOpenSearchIndex],
  );

  if (descriptor.isLoading) {
    return (
      <div className="rounded-xl border bg-card p-6 text-sm text-muted-foreground">
        Loading connection browser…
      </div>
    );
  }
  if (descriptor.isError) {
    return (
      <div className="rounded-xl border border-destructive/40 bg-destructive/5 p-4 text-sm text-destructive">
        {descriptor.error instanceof Error
          ? descriptor.error.message
          : "Failed to load connection browser"}
      </div>
    );
  }
  if (!descriptor.data) return fallback;
  if (descriptor.data.kind === "cache") {
    return (
      <div className="flex min-h-[32rem] flex-col gap-3">
        <div className="h-[calc(100vh-15rem)] min-h-[32rem] overflow-hidden rounded-xl border bg-card">
          <CacheBrowser baseUrl={baseUrl} />
        </div>
      </div>
    );
  }
  const profileAction =
    descriptor.data.provider && renderProfileAction
      ? renderProfileAction({
          connectionName,
          providerType: descriptor.data.provider,
          ...(profileOptions ? { providerOptions: profileOptions } : {}),
        })
      : null;
  return (
    <div className="flex min-w-0 flex-col gap-3">
      {profileAction || descriptor.data.provider === "opensearch" ? (
        <div className="flex flex-wrap items-center gap-2">
          {descriptor.data.provider === "opensearch" ? (
            <Combobox
              ariaLabel="OpenSearch index"
              label="Index"
              value={selectedOpenSearchIndex}
              onChange={setSelectedOpenSearchIndex}
              options={openSearchIndexOptions(inspection.data)}
              placeholder={
                inspection.isError ? "Unable to load indexes" : "Select index…"
              }
              loading={inspection.isLoading}
              invalid={inspection.isError}
              allowCustomValue={false}
              className="min-w-72 flex-1 sm:max-w-xl"
            />
          ) : null}
          {profileAction}
        </div>
      ) : null}
      <ConnectionQueryBrowser
        id={id}
        baseUrl={baseUrl}
        descriptor={descriptor.data}
        selectedOpenSearchIndex={selectedOpenSearchIndex}
        onOpenSearchIndexChange={setSelectedOpenSearchIndex}
      />
    </div>
  );
}

export function connectionDetailHeaderRenderer(
  context: EntityDetailHeaderRenderContext,
): ReactNode {
  if (context.surfaceKey !== "connection") return context.defaultHeader;
  return (
    <ConnectionInfoHeader
      id={context.id}
      icon={context.icon}
      fallbackName={context.title}
    />
  );
}

// ConnectionInfoHeader renders the connection's identity and resolved server on
// a single line for the explorer heading: [icon] name · endpoint · product ·
// status. It shares the ["connection-info", id] query cache with the browser.
function ConnectionInfoHeader({
  id,
  icon,
  fallbackName,
}: {
  id: string;
  icon?: ReactNode;
  fallbackName: string;
}) {
  const info = useQuery({
    queryKey: ["connection-info", id],
    queryFn: () =>
      fetchJSON<ConnectionInfo>(
        `/api/v1/connection/${encodeURIComponent(id)}/info`,
      ),
    retry: 0,
    staleTime: 30_000,
  });
  const data = info.data;
  const name = data?.connection.name ?? fallbackName;
  const endpoint =
    data?.connection.resolvedEndpoint ?? data?.connection.configuredEndpoint;
  const product = data
    ? [data.server.product, data.server.version].filter(Boolean).join(" ")
    : "";
  return (
    <h1 className="flex min-w-0 items-center gap-2 text-2xl font-semibold tracking-tight">
      {icon}
      <span className="shrink-0">{name}</span>
      {info.isLoading ? (
        <span className="text-sm font-normal text-muted-foreground">
          resolving…
        </span>
      ) : info.isError ? (
        <span
          className="truncate text-sm font-normal text-destructive"
          title={info.error instanceof Error ? info.error.message : undefined}
        >
          {info.error instanceof Error ? info.error.message : "unresolved"}
        </span>
      ) : data ? (
        <span className="flex min-w-0 items-center gap-2 text-sm font-normal text-muted-foreground">
          {endpoint ? (
            <>
              <HeaderDot />
              <code className="min-w-0 truncate">{endpoint}</code>
            </>
          ) : null}
          {product ? (
            <>
              <HeaderDot />
              <span className="shrink-0">{product}</span>
            </>
          ) : null}
          <HeaderDot />
          <ServerStatus server={data.server} />
        </span>
      ) : null}
    </h1>
  );
}

function HeaderDot() {
  return <span className="shrink-0 opacity-40">·</span>;
}

function ServerStatus({ server }: { server: ConnectionInfo["server"] }) {
  const tone =
    server.status === "available"
      ? "text-emerald-600 dark:text-emerald-400"
      : server.status === "error"
        ? "text-destructive"
        : "text-muted-foreground";
  const label =
    server.status === "available"
      ? "available"
      : server.status === "error"
        ? "unreachable"
        : "unavailable";
  return (
    <span
      className={`inline-flex shrink-0 items-center gap-1 ${tone}`}
      title={server.message ?? undefined}
    >
      <span className="inline-block h-1.5 w-1.5 rounded-full bg-current" />
      {label}
    </span>
  );
}

function ConnectionQueryBrowser({
  id,
  baseUrl,
  descriptor,
  selectedOpenSearchIndex,
  onOpenSearchIndexChange,
}: {
  id: string;
  baseUrl: string;
  descriptor: BrowserDescriptor;
  selectedOpenSearchIndex: string;
  onOpenSearchIndexChange: (index: string) => void;
}) {
  const baseInspection = useQuery({
    queryKey: ["connection-browser-inspection", id],
    queryFn: () => fetchJSON<BrowserInspection>(`${baseUrl}/inspect`),
    enabled: descriptor.catalog === true,
    retry: 0,
    staleTime: 5 * 60_000,
  });
  const [selection, setSelection] = useState<{
    query?: string;
    options?: Record<string, unknown>;
  }>({});
  const [liveOptions, setLiveOptions] = useState<Record<string, unknown>>({});
  const [selectedDatabase, setSelectedDatabase] = useState("");
  const databaseInspection = useQuery({
    queryKey: ["connection-browser-inspection", id, selectedDatabase],
    queryFn: () => {
      const params = new URLSearchParams({ database: selectedDatabase });
      return fetchJSON<BrowserInspection>(`${baseUrl}/inspect?${params}`);
    },
    enabled:
      baseInspection.data?.kind === "sql" &&
      selectedDatabase !== "" &&
      selectedDatabase !== baseInspection.data.database,
    retry: 0,
    staleTime: 5 * 60_000,
  });
  const inspection =
    selectedDatabase !== "" &&
    selectedDatabase !== baseInspection.data?.database
      ? databaseInspection
      : baseInspection;
  const inspectionData = inspection.data ?? baseInspection.data;
  const activeDatabase = selectedDatabase || inspectionData?.database || "";
  const options = useMemo(
    () => ({
      ...(descriptor.initialOptions ?? {}),
      ...(selection.options ?? {}),
    }),
    [descriptor.initialOptions, selection.options],
  );
  const selectedTargetName = String(
    liveOptions.index ?? selection.options?.index ?? "",
  );
  const selectedTargetKind = useMemo(() => {
    const explicit = liveOptions.targetKind ?? selection.options?.targetKind;
    if (typeof explicit === "string") return explicit;
    return (
      inspectionData?.targets?.find(
        (target) => target.name === selectedTargetName,
      )?.kind ?? ""
    );
  }, [
    inspectionData?.targets,
    liveOptions.targetKind,
    selectedTargetName,
    selection.options?.targetKind,
  ]);
  useEffect(() => {
    if (descriptor.provider !== "opensearch") return;
    const currentIndex = String(
      liveOptions.index ?? selection.options?.index ?? "",
    );
    if (currentIndex === selectedOpenSearchIndex) return;
    const target = inspectionData?.targets?.find(
      (candidate) => candidate.name === selectedOpenSearchIndex,
    );
    const nextOptions = { ...liveOptions };
    if (selectedOpenSearchIndex) nextOptions.index = selectedOpenSearchIndex;
    else delete nextOptions.index;
    if (target?.kind) nextOptions.targetKind = target.kind;
    else delete nextOptions.targetKind;
    setSelection((current) => ({ ...current, options: nextOptions }));
    setLiveOptions(nextOptions);
  }, [
    descriptor.provider,
    inspectionData?.targets,
    liveOptions,
    selectedOpenSearchIndex,
    selection.options?.index,
  ]);
  const selectedInspection = useQuery({
    queryKey: [
      "connection-browser-inspection",
      id,
      selectedTargetKind,
      selectedTargetName,
    ],
    queryFn: () => {
      const params = new URLSearchParams({
        target: selectedTargetName,
        targetKind: selectedTargetKind,
      });
      return fetchJSON<BrowserInspection>(`${baseUrl}/inspect?${params}`);
    },
    enabled:
      inspectionData?.kind === "opensearch" &&
      selectedTargetName !== "" &&
      selectedTargetKind !== "",
    retry: 0,
    staleTime: 5 * 60_000,
  });
  const completion = useMemo<QueryBrowserCompletion | undefined>(() => {
    return completionForInspection(inspectionData, selectedInspection.data);
  }, [inspectionData, selectedInspection.data]);

  return (
    <QueryBrowser
      id={`${descriptor.provider ?? "query"}:${id}`}
      title={`${descriptor.queryLabel ?? "Query"} browser`}
      language={descriptor.language ?? "text"}
      queryLabel={descriptor.queryLabel ?? "Query"}
      initialQuery={selection.query ?? descriptor.defaultQuery ?? ""}
      optionsSchema={queryBrowserOptionsSchema(descriptor)}
      initialOptions={options}
      completion={completion}
      onOptionsChange={setLiveOptions}
      navigator={
        descriptor.catalog ? (
          <CatalogTree
            nodes={inspectionData?.nodes ?? []}
            loading={inspection.isLoading}
            error={inspection.error}
            databases={baseInspection.data?.databases ?? []}
            database={activeDatabase}
            onDatabaseChange={setSelectedDatabase}
            onSelect={(node) => {
              setSelection({ query: node.query, options: node.options });
              setLiveOptions(node.options ?? {});
              if (descriptor.provider === "opensearch") {
                onOpenSearchIndexChange(String(node.options?.index ?? ""));
              }
            }}
          />
        ) : undefined
      }
      execute={(request) => {
        const options =
          descriptor.language === "sql" && activeDatabase
            ? { ...request.options, database: activeDatabase }
            : request.options;
        return fetchJSON<QueryBrowserResult>(`${baseUrl}/query`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ ...request, options }),
        });
      }}
      renderResults={
        descriptor.resultView === "logs"
          ? ({ result, defaultView }) =>
              result.rows?.length ? (
                <LogsTable
                  logs={result.rows}
                  autoFilter={false}
                  fullscreenTitle="Logs"
                />
              ) : (
                defaultView
              )
          : descriptor.resultView === "timeseries"
            ? ({ result, defaultView }) => (
                <PrometheusResults result={result} fallback={defaultView} />
              )
            : undefined
      }
    />
  );
}

export function CatalogTree({
  nodes,
  loading,
  error,
  databases,
  database,
  onDatabaseChange,
  onSelect,
}: {
  nodes: CatalogNode[];
  loading: boolean;
  error: unknown;
  databases: string[];
  database: string;
  onDatabaseChange: (database: string) => void;
  onSelect: (node: CatalogNode) => void;
}) {
  return (
    <div className="flex h-full min-h-0 flex-col overflow-auto border-r bg-card p-2">
      <h3 className="flex items-center gap-1.5 px-2 py-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        <Icon icon={UiSqlDatabase} className="size-3.5" />
        <span>Catalog</span>
      </h3>
      {databases.length > 0 ? (
        <label className="mb-2 block px-2 text-xs text-muted-foreground">
          Database
          <select
            aria-label="Database"
            value={database}
            onChange={(event) => onDatabaseChange(event.target.value)}
            className="mt-1 h-8 w-full rounded-md border bg-background px-2 text-xs text-foreground"
          >
            {databases.map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
          </select>
        </label>
      ) : null}
      {loading && (
        <div className="p-2 text-xs text-muted-foreground">
          Loading catalog…
        </div>
      )}
      {error ? (
        <div
          role="alert"
          className="m-2 rounded-md border border-destructive/30 bg-destructive/5 p-2 text-xs text-destructive"
        >
          <p className="font-medium">Unable to load catalog</p>
          <p className="mt-1 break-words">{catalogErrorMessage(error)}</p>
        </div>
      ) : null}
      {!loading && !error && nodes.length === 0 ? (
        <div className="p-2 text-xs text-muted-foreground">
          No catalog objects found.
        </div>
      ) : null}
      <CatalogNodes
        key={database || "catalog"}
        nodes={nodes}
        onSelect={onSelect}
      />
    </div>
  );
}

function catalogErrorMessage(error: unknown): string {
  if (error instanceof Error && error.message.trim()) {
    return error.message.trim();
  }
  if (typeof error === "string" && error.trim()) {
    return error.trim();
  }
  return "The catalog request failed. Check the connection settings and try again.";
}

function CatalogNodes({
  nodes,
  onSelect,
}: {
  nodes: CatalogNode[];
  onSelect: (node: CatalogNode) => void;
}) {
  return (
    <div role="tree" className="min-w-0">
      {nodes.map((node) => (
        <TreeNode
          key={node.id}
          node={node}
          getKey={(item) => item.id}
          getChildren={(item) => item.children}
          defaultOpen={(item) => item.kind === "schema"}
          isSecondary={(item) => item.kind === "column"}
          onSelect={(item) => {
            if (item.query) onSelect(item);
          }}
          indentPx={14}
          basePaddingPx={8}
          renderRow={({ node: item }) => (
            <div
              className="flex min-w-0 flex-1 items-center gap-1.5 text-xs"
              title={item.query ? `Load ${item.kind}` : item.kind}
            >
              <Icon
                icon={catalogIcon(item.kind)}
                className="size-3.5 shrink-0 text-muted-foreground"
              />
              <span className="truncate">{item.label}</span>
            </div>
          )}
        />
      ))}
    </div>
  );
}

function catalogIcon(kind: string) {
  switch (kind) {
    case "schema":
      return UiNamespace;
    case "table":
      return UiTable;
    case "view":
      return UiSqlView;
    case "column":
      return UiSqlColumn;
    case "index":
      return UiSqlIndex;
    case "alias":
      return UiLink;
    case "data_stream":
      return UiActivity;
    default:
      return UiDatabase;
  }
}

function PrometheusResults({
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
