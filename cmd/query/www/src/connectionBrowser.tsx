import {
  CacheBrowser,
  Combobox,
  type EntityDetailBodyRenderContext,
  type EntityDetailHeaderRenderContext,
  type QueryBrowserResult,
} from "@flanksource/clicky-ui";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import {
  browserBaseUrl,
  fetchJSON,
  mergeProviderOptions,
  openSearchIndexOptions,
  queryBrowserOptionsSchema,
  useInspection,
  type BrowserDescriptor,
  type BrowserInspection,
  type ConnectionProfileActionRenderer,
} from "./connectionBrowserModel";
import { ConnectionQueryWorkspace } from "./connectionQueryWorkspace";
import type { EsSearch } from "./esQueryBuilderModel";

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
  const baseUrl = browserBaseUrl(id);
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
  // Shares the browser's inspection cache entry, so selecting an index here
  // costs no extra request.
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
  const [selection, setSelection] = useState<{
    query?: string;
    options?: Record<string, unknown>;
  }>({});
  const [liveOptions, setLiveOptions] = useState<Record<string, unknown>>({});
  const [selectedDatabase, setSelectedDatabase] = useState("");
  // Exploration is not saved anywhere, so the specification lives here for as
  // long as the browser is open. "Build profile" carries the options forward.
  const [search, setSearch] = useState<EsSearch | undefined>(undefined);
  const explicitTargetKind =
    liveOptions.targetKind ?? selection.options?.targetKind;
  const inspection = useInspection({
    cacheKey: "connection-browser-inspection",
    id,
    baseUrl,
    enabled: descriptor.catalog === true,
    database: selectedDatabase,
    target: String(liveOptions.index ?? selection.options?.index ?? ""),
    ...(typeof explicitTargetKind === "string"
      ? { targetKind: explicitTargetKind }
      : {}),
  });
  const options = useMemo(
    () =>
      mergeProviderOptions({
        layers: [descriptor.initialOptions, selection.options],
        keepTargetKind: true,
      }),
    [descriptor.initialOptions, selection.options],
  );

  // The index combobox lives above the browser, so a change there has to reach
  // the options the browser runs with.
  useEffect(() => {
    if (descriptor.provider !== "opensearch") return;
    const currentIndex = String(
      liveOptions.index ?? selection.options?.index ?? "",
    );
    if (currentIndex === selectedOpenSearchIndex) return;
    const target = inspection.data?.targets?.find(
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
    inspection.data?.targets,
    liveOptions,
    selectedOpenSearchIndex,
    selection.options?.index,
  ]);

  return (
    <ConnectionQueryWorkspace
      id={`${descriptor.provider ?? "query"}:${id}`}
      title={`${descriptor.queryLabel ?? "Query"} browser`}
      descriptor={descriptor}
      inspection={inspection}
      onDatabaseChange={setSelectedDatabase}
      query={selection.query ?? descriptor.defaultQuery ?? ""}
      onQueryChange={(next) =>
        setSelection((current) => ({ ...current, query: next }))
      }
      options={options}
      onOptionsChange={setLiveOptions}
      optionsSchema={queryBrowserOptionsSchema(descriptor)}
      search={search}
      onSearchChange={(transition) => {
        setSearch(transition.search);
        setSelection((current) => ({ ...current, query: transition.query }));
      }}
      compileBaseUrl={baseUrl}
      onCatalogSelect={(node) => {
        setSelection({ query: node.query, options: node.options });
        setLiveOptions(node.options ?? {});
        if (descriptor.provider === "opensearch") {
          onOpenSearchIndexChange(String(node.options?.index ?? ""));
        }
      }}
      execute={(request) =>
        fetchJSON<QueryBrowserResult>(`${baseUrl}/query`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            ...request,
            options: mergeProviderOptions({
              layers: [request.options],
              database: inspection.sqlDatabase,
              keepTargetKind: true,
            }),
          }),
        })
      }
    />
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
