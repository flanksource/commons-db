import {
  Button,
  Icon,
  JsonSchemaForm,
  Modal,
  type JsonSchemaObject,
  type JsonSchemaProperty,
  type QueryBrowserResult,
} from "@flanksource/clicky-ui";
import { UiCheck, UiColumns, UiSqlColumn } from "@flanksource/clicky-ui/icons";
import { useQuery } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import {
  browserBaseUrl,
  fetchJSON,
  mergeProviderOptions,
  useInspection,
  type BrowserDescriptor,
} from "./connectionBrowserModel";
import { ConnectionQueryWorkspace } from "./connectionQueryWorkspace";
import { paramNames, paramRoles } from "./esQueryBuilder";
import type { EsSearch } from "./esQueryBuilderModel";
import {
  ColumnPicker,
  mapTimestampColumn,
  type ProfileColumn,
} from "./profileColumnPicker";

export type ProfileProvider = {
  type?: string;
  role?: string;
  connection?: string;
  options?: Record<string, unknown>;
};

export type ParamDraft = {
  name?: string;
  label?: string;
  type?: string;
  /** filter, limit, offset, time-from or time-to; empty behaves as filter. */
  role?: string;
  default?: unknown;
  options?: string[];
  required?: boolean;
  description?: string;
};

export type ProfileDraft = Record<string, unknown> & {
  profile?: string;
  query?: string;
  provider?: ProfileProvider;
  params?: ParamDraft[];
  columns?: ProfileColumn[];
};

type SampleResult = QueryBrowserResult & {
  columns: ProfileColumn[];
  renderedQuery: string;
};

// Modal's body is a flex child. It must be allowed to shrink and must not own
// scrolling, otherwise QueryBrowser's intrinsic minimum height expands the
// whole workspace and pushes the editor/results below the dialog viewport.
export const profileBuilderModalClassName =
  "profile-builder-workspace-dialog h-[calc(100dvh-2rem)]";

export function ProfileBuilderWorkspace({
  connectionID,
  rootValue,
  onApply,
  onClose,
}: {
  connectionID: string;
  rootValue: ProfileDraft;
  onApply: (next: Record<string, unknown>) => void;
  onClose: () => void;
}) {
  const baseUrl = browserBaseUrl(connectionID);
  const descriptor = useQuery({
    queryKey: ["profile-builder-descriptor", connectionID],
    queryFn: () => fetchJSON<BrowserDescriptor>(baseUrl),
    retry: 0,
  });
  const initialProviderOptions = useMemo(
    () => ({ ...(rootValue.provider?.options ?? {}) }),
    [rootValue.provider?.options],
  );
  const [query, setQuery] = useState(rootValue.query ?? "");
  const [search, setSearch] = useState<EsSearch | undefined>(
    () => initialProviderOptions.search as EsSearch | undefined,
  );
  const [liveOptions, setLiveOptions] = useState<Record<string, unknown>>(
    initialProviderOptions,
  );
  const [catalogOptions, setCatalogOptions] = useState<Record<string, unknown>>(
    {},
  );
  const [sampleParams, setSampleParams] = useState<Record<string, unknown>>(
    () => defaultParamValues(rootValue.params ?? []),
  );
  const [sampleColumns, setSampleColumns] = useState<ProfileColumn[]>([]);
  const [selectedColumns, setSelectedColumns] = useState<Set<string>>(
    () => new Set(),
  );
  const [timestampColumn, setTimestampColumn] = useState(
    () =>
      rootValue.columns?.find((column) => column.kind === "timestamp")?.name ??
      "",
  );
  const [selectedDatabase, setSelectedDatabase] = useState("");

  useEffect(() => {
    if (!query && descriptor.data?.defaultQuery) {
      setQuery(descriptor.data.defaultQuery);
    }
  }, [descriptor.data?.defaultQuery, query]);

  const explicitTargetKind =
    liveOptions.targetKind ?? initialProviderOptions.targetKind;
  const inspection = useInspection({
    cacheKey: "profile-builder-inspection",
    id: connectionID,
    baseUrl,
    enabled: descriptor.data?.catalog === true,
    database: selectedDatabase,
    fallbackDatabase: String(initialProviderOptions.database ?? ""),
    target: String(liveOptions.index ?? initialProviderOptions.index ?? ""),
    ...(typeof explicitTargetKind === "string"
      ? { targetKind: explicitTargetKind }
      : {}),
  });
  const browserOptions = useMemo(
    () =>
      mergeProviderOptions({
        layers: [
          descriptor.data?.initialOptions,
          initialProviderOptions,
          catalogOptions,
        ],
        database: inspection.sqlDatabase,
        keepTargetKind: true,
      }),
    [
      catalogOptions,
      descriptor.data?.initialOptions,
      initialProviderOptions,
      inspection.sqlDatabase,
    ],
  );
  // The specification is authored here, not merged from a layer, so it is
  // stamped on last — including its absence, which a lower layer would
  // otherwise reinstate after the author switched back to raw DSL.
  const effectiveOptions = useCallback(
    (options: Record<string, unknown>) => {
      const merged = mergeProviderOptions({
        layers: [initialProviderOptions, catalogOptions, options],
        database: inspection.sqlDatabase,
      });
      if (search) merged.search = search;
      else delete merged.search;
      return merged;
    },
    [catalogOptions, initialProviderOptions, inspection.sqlDatabase, search],
  );

  const paramSchema = useMemo(
    () => sampleParamSchema(rootValue.params ?? []),
    [rootValue.params],
  );
  const existingColumns = rootValue.columns ?? [];
  const existingNames = useMemo(
    () => new Set(existingColumns.map((column) => column.name)),
    [existingColumns],
  );

  const applyDraft = (mode: "query" | "merge" | "replace") => {
    const chosen = mapTimestampColumn(
      sampleColumns.filter((column) => selectedColumns.has(column.name)),
      timestampColumn,
    );
    let columns = existingColumns;
    if (mode === "merge") {
      columns = mapTimestampColumn(
        [
          ...existingColumns,
          ...chosen.filter((column) => !existingNames.has(column.name)),
        ],
        timestampColumn,
      );
    } else if (mode === "replace") {
      if (
        existingColumns.length > 0 &&
        !window.confirm(
          `Replace ${existingColumns.length} configured column${existingColumns.length === 1 ? "" : "s"}?`,
        )
      ) {
        return;
      }
      columns = chosen;
    }
    onApply({
      ...rootValue,
      query,
      provider: {
        ...(rootValue.provider ?? {}),
        options: effectiveOptions(liveOptions),
      },
      ...(mode === "query" ? {} : { columns }),
    });
    onClose();
  };

  const footer = (
    <div className="flex flex-wrap items-center justify-end gap-2">
      <Button type="button" variant="ghost" onClick={onClose}>
        Cancel
      </Button>
      <Button
        type="button"
        variant="outline"
        disabled={!query.trim() && !search}
        onClick={() => applyDraft("query")}
      >
        <Icon icon={UiCheck} className="size-4" />
        Use query
      </Button>
      <Button
        type="button"
        variant="outline"
        disabled={selectedColumns.size === 0}
        onClick={() => applyDraft("merge")}
      >
        <Icon icon={UiColumns} className="size-4" />
        Merge selected
      </Button>
      <Button
        type="button"
        disabled={selectedColumns.size === 0}
        onClick={() => applyDraft("replace")}
      >
        <Icon icon={UiSqlColumn} className="size-4" />
        Replace columns
      </Button>
    </div>
  );

  return (
    <Modal
      open
      onClose={onClose}
      title={`Build profile from ${connectionID}`}
      size="full"
      className={profileBuilderModalClassName}
      footer={footer}
    >
      <div className="flex h-full min-h-0 flex-col gap-3">
        {Object.keys(paramSchema.properties ?? {}).length > 0 ? (
          <div className="shrink-0 rounded-md border bg-card px-3 py-2">
            <div className="mb-2 text-xs font-medium text-muted-foreground">
              Temporary sample parameters (not saved)
            </div>
            <JsonSchemaForm
              schema={paramSchema}
              value={sampleParams}
              onChange={setSampleParams}
              size="sm"
              inline
              showPreferencesMenu={false}
              persistPreferences={false}
            />
          </div>
        ) : null}
        {descriptor.isLoading ? (
          <WorkspaceMessage>Loading connection browser…</WorkspaceMessage>
        ) : descriptor.isError ? (
          <WorkspaceMessage error>
            {errorMessage(
              descriptor.error,
              "Unable to load this connection browser",
            )}
          </WorkspaceMessage>
        ) : descriptor.data ? (
          <ConnectionQueryWorkspace
            id={`profile-builder:${connectionID}`}
            title="Profile query"
            descriptor={descriptor.data}
            inspection={inspection}
            onDatabaseChange={setSelectedDatabase}
            query={query}
            onQueryChange={setQuery}
            options={browserOptions}
            onOptionsChange={setLiveOptions}
            search={search}
            onSearchChange={(transition) => {
              setSearch(transition.search);
              setQuery(transition.query);
            }}
            params={paramNames(rootValue.params)}
            paramRoles={paramRoles(rootValue.params)}
            compileBaseUrl={baseUrl}
            className="h-full min-h-0"
            onCatalogSelect={(node) => {
              if (node.query) setQuery(node.query);
              const nextOptions = node.options ?? {};
              setCatalogOptions(nextOptions);
              setLiveOptions({ ...browserOptions, ...nextOptions });
            }}
            execute={async (request) => {
              const result = await fetchJSON<SampleResult>(
                "/api/v1/profile/sample",
                {
                  method: "POST",
                  headers: { "Content-Type": "application/json" },
                  body: JSON.stringify({
                    profile: {
                      ...rootValue,
                      profile: rootValue.profile || "sample",
                      query: request.query,
                      provider: {
                        ...(rootValue.provider ?? {}),
                        options: effectiveOptions(request.options),
                      },
                    },
                    params: sampleParams,
                  }),
                },
              );
              setSampleColumns(result.columns ?? []);
              setSelectedColumns(
                new Set((result.columns ?? []).map((column) => column.name)),
              );
              return result;
            }}
            renderResults={({ defaultView }) => (
              <div className="flex min-h-0 flex-1 flex-col gap-3">
                <div className="min-h-0 flex-1">{defaultView}</div>
                {sampleColumns.length > 0 ? (
                  <ColumnPicker
                    columns={sampleColumns}
                    selected={selectedColumns}
                    existing={existingNames}
                    onChange={setSelectedColumns}
                    timestampColumn={timestampColumn}
                    onTimestampColumnChange={setTimestampColumn}
                  />
                ) : null}
              </div>
            )}
          />
        ) : (
          <WorkspaceMessage>
            This saved connection does not expose a query browser.
          </WorkspaceMessage>
        )}
      </div>
    </Modal>
  );
}

function WorkspaceMessage({
  children,
  error = false,
}: {
  children: ReactNode;
  error?: boolean;
}) {
  return (
    <div
      className={`grid min-h-80 flex-1 place-items-center rounded-md border border-dashed p-6 text-sm ${error ? "border-destructive/40 text-destructive" : "text-muted-foreground"}`}
    >
      {children}
    </div>
  );
}

function sampleParamSchema(params: ParamDraft[]): JsonSchemaObject {
  const properties: Record<string, JsonSchemaProperty> = {};
  const required: string[] = [];
  for (const param of params) {
    const name = param.name?.trim();
    if (!name) continue;
    const property: JsonSchemaProperty = {
      title: param.label || name,
      ...(param.description ? { description: param.description } : {}),
      ...(param.default !== undefined ? { default: param.default } : {}),
    };
    switch (param.type) {
      case "number":
        property.type = "number";
        break;
      case "boolean":
        property.type = "boolean";
        break;
      case "date":
        property.type = "string";
        property.format = "date-time";
        break;
      default:
        property.type = "string";
    }
    if (param.options?.length) property.enum = param.options;
    properties[name] = property;
    if (param.required) required.push(name);
  }
  return {
    type: "object",
    properties,
    ...(required.length ? { required } : {}),
  };
}

function defaultParamValues(params: ParamDraft[]): Record<string, unknown> {
  return Object.fromEntries(
    params
      .filter((param) => param.name && param.default !== undefined)
      .map((param) => [param.name as string, param.default]),
  );
}

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message.trim()
    ? error.message.trim()
    : fallback;
}
