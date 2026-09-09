import type {
  QueryBrowserRequest,
  QueryBrowserResult,
} from "@flanksource/clicky-ui";
import { QueryBrowser } from "@flanksource/clicky-ui";
import {
  ConnectionQueryWorkspace,
  fetchJSON,
  mergeProviderOptions,
  queryBrowserOptionsSchema,
  useInspection,
  type BrowserDescriptor,
  type CatalogNode,
  type EsSearch,
  type QueryModeTransition,
} from "@flanksource/clicky-ui/profiles";
import { useCallback, useMemo, useState } from "react";
import { makeBrowserFilterLookup } from "./browserFilterValues";
import { SQLCatalogNavigator, type SQLCatalog } from "./sqlCatalogNavigator";

export function ConnectionQueryBrowser({
  id,
  baseUrl,
  descriptor,
  onOptionsChange,
  onQueryChange,
}: {
  id: string;
  baseUrl: string;
  descriptor: BrowserDescriptor;
  onOptionsChange: (options: Record<string, unknown>) => void;
  onQueryChange?: (query: string) => void;
}) {
  const [selection, setSelection] = useState<{
    query?: string;
    options?: Record<string, unknown>;
  }>({});
  const [liveOptions, setLiveOptions] = useState<Record<string, unknown>>({});
  const [selectedDatabase, setSelectedDatabase] = useState("");
  const [runToken, setRunToken] = useState<number>();
  // Exploration is not saved anywhere, so the specification lives here for as
  // long as the browser is open. "Build profile" carries the options forward.
  const [search, setSearch] = useState<EsSearch | undefined>(undefined);
  const targetOption =
    descriptor.target?.kind === "index" ? descriptor.target.option : "";
  const selectedTarget = targetOption
    ? (liveOptions[targetOption] ?? selection.options?.[targetOption])
    : undefined;
  const explicitTargetKind =
    liveOptions.targetKind ?? selection.options?.targetKind;
  const inspection = useInspection({
    cacheKey: "connection-browser-inspection",
    id,
    baseUrl,
    enabled: descriptor.catalog === true,
    database: selectedDatabase,
    fallbackDatabase: String(descriptor.initialOptions?.database ?? ""),
    target: String(selectedTarget ?? ""),
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
  const lookupFilterValues = useMemo(
    () => makeBrowserFilterLookup(baseUrl),
    [baseUrl],
  );
  const sqlCatalog =
    inspection.data?.kind === "sql"
      ? (inspection.data as SQLCatalog)
      : undefined;
  // SQLite's "snapshot" name is a display label, not a database override.
  const sqlDatabase =
    sqlCatalog?.driver === "sqlite" ? "" : inspection.sqlDatabase;
  const execute = useCallback(
    (request: QueryBrowserRequest) =>
      fetchJSON<QueryBrowserResult>(`${baseUrl}/query`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          ...request,
          options: mergeProviderOptions({
            layers: [request.options],
            database: sqlDatabase,
            keepTargetKind: true,
          }),
        }),
      }),
    [baseUrl, sqlDatabase],
  );

  if (sqlCatalog) {
    const activeDatabase =
      inspection.sqlDatabase || sqlCatalog.database || selectedDatabase;
    const setQuery = (query: string) => {
      setSelection((current) => ({ ...current, query }));
      onQueryChange?.(query);
    };
    return (
      <QueryBrowser
        id={`${descriptor.provider ?? "sql"}:${id}:${activeDatabase}`}
        title={`${descriptor.queryLabel ?? "Query"} browser`}
        language="sql"
        initialQuery={selection.query ?? descriptor.defaultQuery ?? ""}
        runToken={runToken}
        queryLabel={descriptor.queryLabel}
        allowEmptyQuery={descriptor.allowEmptyQuery}
        optionsSchema={queryBrowserOptionsSchema(descriptor)}
        initialOptions={options}
        completion={inspection.completion}
        navigatorSplit={35}
        onQueryChange={setQuery}
        onOptionsChange={(next) => {
          setLiveOptions(next);
          onOptionsChange(next);
        }}
        navigator={
          <SQLCatalogNavigator
            key={sqlCatalog.database}
            catalog={sqlCatalog}
            database={activeDatabase}
            loading={inspection.loading}
            refreshing={inspection.refreshing}
            error={inspection.error}
            cache={inspection.cache}
            onDatabaseChange={(database) => {
              setSelectedDatabase(database);
              setSelection({});
              setLiveOptions({});
              setRunToken(undefined);
              onQueryChange?.(descriptor.defaultQuery ?? "");
            }}
            onRefresh={inspection.refresh}
            onSelect={setQuery}
            onRun={(query) => {
              setQuery(query);
              setRunToken((previous) => (previous ?? 0) + 1);
            }}
          />
        }
        execute={execute}
        {...(lookupFilterValues ? { lookupFilterValues } : {})}
        className="h-[calc(100vh-15rem)] min-h-[32rem]"
      />
    );
  }

  return (
    <ConnectionQueryWorkspace
      id={`${descriptor.provider ?? "query"}:${id}`}
      title={`${descriptor.queryLabel ?? "Query"} browser`}
      descriptor={descriptor}
      inspection={inspection}
      onDatabaseChange={setSelectedDatabase}
      query={selection.query ?? descriptor.defaultQuery ?? ""}
      onQueryChange={(next: string) => {
        setSelection((current) => ({ ...current, query: next }));
        onQueryChange?.(next);
      }}
      options={options}
      onOptionsChange={(next: Record<string, unknown>) => {
        setLiveOptions(next);
        onOptionsChange(next);
      }}
      optionsSchema={queryBrowserOptionsSchema(descriptor)}
      search={search}
      onSearchChange={(transition: QueryModeTransition) => {
        setSearch(transition.search);
        setSelection((current) => ({ ...current, query: transition.query }));
      }}
      compileBaseUrl={baseUrl}
      onCatalogSelect={(node: CatalogNode) => {
        setSelection({ query: node.query, options: node.options });
        setLiveOptions(node.options ?? {});
        onQueryChange?.(node.query ?? "");
      }}
      {...(lookupFilterValues ? { lookupFilterValues } : {})}
      execute={execute}
    />
  );
}
