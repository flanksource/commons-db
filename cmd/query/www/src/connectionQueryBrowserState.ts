import type {
  QueryBrowserRequest,
  QueryBrowserResult,
} from "@flanksource/clicky-ui";
import {
  fetchJSON,
  mergeProviderOptions,
  useInspection,
  type BrowserDescriptor,
  type EsSearch,
} from "@flanksource/clicky-ui/profiles";
import { useCallback, useMemo, useState } from "react";
import { makeBrowserFilterLookup } from "./browserFilterValues";
import type { SQLCatalog } from "./sqlCatalogNavigator";

export type QuerySelection = {
  query?: string;
  options?: Record<string, unknown>;
};

type Inspection = ReturnType<typeof useInspection>;

function useBrowserInspection({
  id,
  baseUrl,
  descriptor,
  selection,
  liveOptions,
  selectedDatabase,
}: {
  id: string;
  baseUrl: string;
  descriptor: BrowserDescriptor;
  selection: QuerySelection;
  liveOptions: Record<string, unknown>;
  selectedDatabase: string;
}): Inspection {
  const targetOption =
    descriptor.target?.kind === "index" ? descriptor.target.option : "";
  const selectedTarget = targetOption
    ? (liveOptions[targetOption] ?? selection.options?.[targetOption])
    : undefined;
  const explicitTargetKind =
    liveOptions.targetKind ?? selection.options?.targetKind;
  return useInspection({
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
}

function useBrowserExecute(baseUrl: string, sqlDatabase: string | undefined) {
  return useCallback(
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
}

/** Everything the SQL and generic query workspaces share for one connection. */
export function useConnectionQueryState({
  id,
  baseUrl,
  descriptor,
}: {
  id: string;
  baseUrl: string;
  descriptor: BrowserDescriptor;
}) {
  const [selection, setSelection] = useState<QuerySelection>({});
  const [liveOptions, setLiveOptions] = useState<Record<string, unknown>>({});
  const [selectedDatabase, setSelectedDatabase] = useState("");
  const [runToken, setRunToken] = useState<number>();
  // Exploration is not saved anywhere, so the specification lives here for as
  // long as the browser is open. "Build profile" carries the options forward.
  const [search, setSearch] = useState<EsSearch | undefined>(undefined);
  const inspection = useBrowserInspection({
    id,
    baseUrl,
    descriptor,
    selection,
    liveOptions,
    selectedDatabase,
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
  const execute = useBrowserExecute(baseUrl, sqlDatabase);
  return {
    selection,
    setSelection,
    liveOptions,
    setLiveOptions,
    selectedDatabase,
    setSelectedDatabase,
    runToken,
    setRunToken,
    search,
    setSearch,
    inspection,
    options,
    lookupFilterValues,
    sqlCatalog,
    execute,
  };
}

export type ConnectionQueryState = ReturnType<typeof useConnectionQueryState>;
