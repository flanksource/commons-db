import { QueryBrowser } from "@flanksource/clicky-ui";
import {
  ConnectionQueryWorkspace,
  queryBrowserOptionsSchema,
  type BrowserDescriptor,
  type CatalogNode,
  type QueryModeTransition,
} from "@flanksource/clicky-ui/profiles";
import {
  useConnectionQueryState,
  type ConnectionQueryState,
} from "./connectionQueryBrowserState";
import { SQLCatalogNavigator, type SQLCatalog } from "./sqlCatalogNavigator";

type QueryBrowserProps = {
  id: string;
  baseUrl: string;
  descriptor: BrowserDescriptor;
  onOptionsChange: (options: Record<string, unknown>) => void;
  onQueryChange?: (query: string) => void;
};

export function ConnectionQueryBrowser(props: QueryBrowserProps) {
  const { id, baseUrl, descriptor } = props;
  const state = useConnectionQueryState({ id, baseUrl, descriptor });
  if (state.sqlCatalog) {
    return (
      <SqlQueryBrowser {...props} state={state} sqlCatalog={state.sqlCatalog} />
    );
  }
  return <GenericQueryBrowser {...props} state={state} />;
}

function SqlQueryBrowser({
  id,
  descriptor,
  onOptionsChange,
  onQueryChange,
  state,
  sqlCatalog,
}: QueryBrowserProps & { state: ConnectionQueryState; sqlCatalog: SQLCatalog }) {
  const {
    selection,
    setSelection,
    setLiveOptions,
    selectedDatabase,
    setSelectedDatabase,
    runToken,
    setRunToken,
    inspection,
    options,
    lookupFilterValues,
    execute,
  } = state;
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

function GenericQueryBrowser({
  id,
  baseUrl,
  descriptor,
  onOptionsChange,
  onQueryChange,
  state,
}: QueryBrowserProps & { state: ConnectionQueryState }) {
  const {
    selection,
    setSelection,
    setLiveOptions,
    setSelectedDatabase,
    search,
    setSearch,
    inspection,
    options,
    lookupFilterValues,
    execute,
  } = state;
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
