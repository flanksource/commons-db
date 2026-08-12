import type { QueryBrowserRequest, QueryBrowserResult } from "@flanksource/clicky-ui";
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
import { useMemo, useState } from "react";
import { makeBrowserFilterLookup } from "./browserFilterValues";

export function ConnectionQueryBrowser({
  id,
  baseUrl,
  descriptor,
  onTargetChange,
}: {
  id: string;
  baseUrl: string;
  descriptor: BrowserDescriptor;
  onTargetChange: (target: string) => void;
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
  const lookupFilterValues = useMemo(
    () => makeBrowserFilterLookup(baseUrl),
    [baseUrl],
  );

  return (
    <ConnectionQueryWorkspace
      id={`${descriptor.provider ?? "query"}:${id}`}
      title={`${descriptor.queryLabel ?? "Query"} browser`}
      descriptor={descriptor}
      inspection={inspection}
      onDatabaseChange={setSelectedDatabase}
      query={selection.query ?? descriptor.defaultQuery ?? ""}
      onQueryChange={(next: string) =>
        setSelection((current) => ({ ...current, query: next }))
      }
      options={options}
      onOptionsChange={(next: Record<string, unknown>) => {
        setLiveOptions(next);
        onTargetChange(String(next.index ?? ""));
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
      }}
      {...(lookupFilterValues ? { lookupFilterValues } : {})}
      execute={(request: QueryBrowserRequest) =>
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
