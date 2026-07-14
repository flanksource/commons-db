import {
  Button,
  Icon,
  JsonSchemaForm,
  Modal,
  QueryBrowser,
  type JsonSchemaObject,
  type JsonSchemaProperty,
  type PostExtension,
  type QueryBrowserCompletion,
  type QueryBrowserResult,
} from "@flanksource/clicky-ui";
import {
  UiCheck,
  UiColumns,
  UiDatabase,
  UiSqlColumn,
} from "@flanksource/clicky-ui/icons";
import { useQuery } from "@tanstack/react-query";
import {
  useCallback,
  createContext,
  useEffect,
  useContext,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import {
  CatalogTree,
  completionForInspection,
  fetchJSON,
  type BrowserDescriptor,
  type BrowserInspection,
  type CatalogNode,
} from "./connectionBrowser";
import "./profileBuilder.css";

type ProfileColumn = {
  name: string;
  type?: string;
  kind?: string;
  [key: string]: unknown;
};

type ProfileProvider = {
  type?: string;
  role?: string;
  connection?: string;
  options?: Record<string, unknown>;
};

const PROFILE_COLUMN_TYPE_LABELS: Record<string, string> = {
  key_value: "KeyValue{}",
  key_values: "[]KeyValue",
  json: "JSON",
};

export function profileColumnTypeLabel(type?: string) {
  return type ? (PROFILE_COLUMN_TYPE_LABELS[type] ?? type) : "string";
}

type ProfileDraft = Record<string, unknown> & {
  profile?: string;
  query?: string;
  provider?: ProfileProvider;
  params?: ParamDraft[];
  columns?: ProfileColumn[];
};

type ParamDraft = {
  name?: string;
  label?: string;
  type?: string;
  default?: unknown;
  options?: string[];
  required?: boolean;
  description?: string;
};

type SampleResult = QueryBrowserResult & {
  columns: ProfileColumn[];
  renderedQuery: string;
};

const profileQueryBuilderPost: PostExtension = (field, nodes, ctx) => {
  if (field.schema["x-clicky-component"] !== "profile-query-builder") {
    return nodes;
  }
  return {
    label: nodes.label,
    value: (
      <ProfileQueryBuilderField
        input={nodes.value}
        rootValue={(ctx?.rootValue ?? {}) as ProfileDraft}
        onRootChange={ctx?.onRootChange}
      />
    ),
  };
};

export const profileBuilderFormExtensions = {
  post: [profileQueryBuilderPost],
};

const ProfileBuilderAutoOpenContext = createContext(false);

// Modal's body is a flex child. It must be allowed to shrink and must not own
// scrolling, otherwise QueryBrowser's intrinsic minimum height expands the
// whole workspace and pushes the editor/results below the dialog viewport.
export const profileBuilderModalClassName =
  "profile-builder-workspace-dialog h-[calc(100dvh-2rem)]";

export function ProfileBuilderAutoOpen({ children }: { children: ReactNode }) {
  return (
    <ProfileBuilderAutoOpenContext.Provider value>
      {children}
    </ProfileBuilderAutoOpenContext.Provider>
  );
}

function ProfileQueryBuilderField({
  input,
  rootValue,
  onRootChange,
}: {
  input: ReactNode;
  rootValue: ProfileDraft;
  onRootChange?: (next: Record<string, unknown>) => void;
}) {
  const [open, setOpen] = useState(false);
  const autoOpen = useContext(ProfileBuilderAutoOpenContext);
  const autoOpened = useRef(false);
  const connection = rootValue.provider?.connection ?? "";
  const connectionID = savedConnectionID(connection);

  useEffect(() => {
    if (!autoOpen || autoOpened.current || !connectionID || !onRootChange) {
      return;
    }
    autoOpened.current = true;
    setOpen(true);
  }, [autoOpen, connectionID, onRootChange]);

  return (
    <div className="min-w-0 space-y-2">
      {input}
      <div className="flex flex-wrap items-center gap-2">
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={!connectionID || !onRootChange}
          title={
            connectionID
              ? "Browse the saved connection and sample rows"
              : "Choose a saved connection before opening the builder"
          }
          onClick={() => setOpen(true)}
        >
          <Icon icon={UiDatabase} className="size-4" />
          Build from connection
        </Button>
        {!connectionID ? (
          <span className="text-xs text-muted-foreground">
            Choose a saved connection to browse its catalog and sample rows.
            Inline URLs can still be configured manually.
          </span>
        ) : null}
      </div>
      {open && connectionID && onRootChange ? (
        <ProfileBuilderWorkspace
          connectionID={connectionID}
          rootValue={rootValue}
          onApply={onRootChange}
          onClose={() => setOpen(false)}
        />
      ) : null}
    </div>
  );
}

function ProfileBuilderWorkspace({
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
  const baseUrl = `/api/v1/connection/${encodeURIComponent(connectionID)}/browser`;
  const descriptor = useQuery({
    queryKey: ["profile-builder-descriptor", connectionID],
    queryFn: () => fetchJSON<BrowserDescriptor>(baseUrl),
    retry: 0,
  });
  const baseInspection = useQuery({
    queryKey: ["profile-builder-inspection", connectionID],
    queryFn: () => fetchJSON<BrowserInspection>(`${baseUrl}/inspect`),
    enabled: descriptor.data?.catalog === true,
    retry: 0,
    staleTime: 5 * 60_000,
  });
  const initialProviderOptions = useMemo(
    () => ({ ...(rootValue.provider?.options ?? {}) }),
    [rootValue.provider?.options],
  );
  const [query, setQuery] = useState(rootValue.query ?? "");
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

  const databaseInspection = useQuery({
    queryKey: ["profile-builder-inspection", connectionID, selectedDatabase],
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
  const activeDatabase =
    selectedDatabase ||
    String(initialProviderOptions.database ?? "") ||
    inspectionData?.database ||
    "";
  const browserOptions = useMemo<Record<string, unknown>>(
    () => ({
      ...(descriptor.data?.initialOptions ?? {}),
      ...initialProviderOptions,
      ...catalogOptions,
      ...(activeDatabase && inspectionData?.kind === "sql"
        ? { database: activeDatabase }
        : {}),
    }),
    [
      activeDatabase,
      catalogOptions,
      descriptor.data?.initialOptions,
      initialProviderOptions,
      inspectionData?.kind,
    ],
  );
  const selectedTargetName = String(
    liveOptions.index ?? browserOptions.index ?? "",
  );
  const selectedTargetKind = useMemo(() => {
    const explicit = liveOptions.targetKind ?? browserOptions.targetKind;
    if (typeof explicit === "string") return explicit;
    return (
      inspectionData?.targets?.find(
        (target) => target.name === selectedTargetName,
      )?.kind ?? ""
    );
  }, [
    browserOptions.targetKind,
    inspectionData?.targets,
    liveOptions.targetKind,
    selectedTargetName,
  ]);
  const selectedInspection = useQuery({
    queryKey: [
      "profile-builder-inspection",
      connectionID,
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
  const completion = useMemo<QueryBrowserCompletion | undefined>(
    () => completionForInspection(inspectionData, selectedInspection.data),
    [inspectionData, selectedInspection.data],
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

  const effectiveOptions = useCallback(
    (options: Record<string, unknown>) => {
      const next: Record<string, unknown> = {
        ...initialProviderOptions,
        ...catalogOptions,
        ...options,
        ...(activeDatabase && inspectionData?.kind === "sql"
          ? { database: activeDatabase }
          : {}),
      };
      delete next.targetKind;
      return next;
    },
    [
      activeDatabase,
      catalogOptions,
      initialProviderOptions,
      inspectionData?.kind,
    ],
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
        disabled={!query.trim()}
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
          <QueryBrowser
            id={`profile-builder:${connectionID}`}
            title="Profile query"
            language={descriptor.data.language ?? "text"}
            queryLabel={descriptor.data.queryLabel ?? "Query"}
            initialQuery={query}
            initialOptions={browserOptions}
            completion={completion}
            onQueryChange={setQuery}
            onOptionsChange={setLiveOptions}
            className="h-full min-h-0"
            navigator={
              descriptor.data.catalog ? (
                <CatalogTree
                  nodes={inspectionData?.nodes ?? []}
                  loading={inspection.isLoading}
                  error={inspection.error}
                  databases={baseInspection.data?.databases ?? []}
                  database={activeDatabase}
                  onDatabaseChange={setSelectedDatabase}
                  onSelect={(node: CatalogNode) => {
                    if (node.query) setQuery(node.query);
                    const nextOptions = node.options ?? {};
                    setCatalogOptions(nextOptions);
                    setLiveOptions({ ...browserOptions, ...nextOptions });
                  }}
                />
              ) : undefined
            }
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

function ColumnPicker({
  columns,
  selected,
  existing,
  onChange,
  timestampColumn,
  onTimestampColumnChange,
}: {
  columns: ProfileColumn[];
  selected: Set<string>;
  existing: Set<string>;
  onChange: (next: Set<string>) => void;
  timestampColumn: string;
  onTimestampColumnChange: (next: string) => void;
}) {
  return (
    <div className="shrink-0 rounded-md border bg-card p-3">
      <div className="mb-2 flex items-center gap-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        <Icon icon={UiSqlColumn} className="size-4" />
        Columns from sample
      </div>
      <div className="flex max-h-32 flex-wrap gap-2 overflow-auto">
        {columns.map((column) => (
          <div
            key={column.name}
            className="flex cursor-pointer items-center gap-2 rounded-md border bg-background px-2 py-1.5 text-xs"
          >
            <label className="flex cursor-pointer items-center gap-2">
              <input
                type="checkbox"
                checked={selected.has(column.name)}
                onChange={(event) => {
                  const next = new Set(selected);
                  if (event.target.checked) next.add(column.name);
                  else {
                    next.delete(column.name);
                    if (timestampColumn === column.name)
                      onTimestampColumnChange("");
                  }
                  onChange(next);
                }}
              />
              <span className="font-medium">{column.name}</span>
              <span className="text-muted-foreground">
                {profileColumnTypeLabel(column.type)}
              </span>
            </label>
            <label className="ml-1 flex cursor-pointer items-center gap-1 border-l pl-2 text-muted-foreground">
              <input
                type="radio"
                name="profile-timestamp-column"
                aria-label={`Use ${column.name} for time ranges`}
                checked={timestampColumn === column.name}
                disabled={!selected.has(column.name)}
                onChange={() => onTimestampColumnChange(column.name)}
              />
              time range
            </label>
            {existing.has(column.name) ? (
              <span className="rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
                configured
              </span>
            ) : null}
          </div>
        ))}
      </div>
    </div>
  );
}

export function mapTimestampColumn(
  columns: ProfileColumn[],
  timestampColumn: string,
): ProfileColumn[] {
  return columns.map((column) => {
    if (column.name === timestampColumn) {
      return { ...column, type: "datetime", kind: "timestamp" };
    }
    if (column.kind !== "timestamp") return column;
    const { kind: _kind, ...rest } = column;
    return rest;
  });
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

function savedConnectionID(value: string): string | null {
  const prefix = "connection://";
  if (!value.startsWith(prefix)) return null;
  const id = value.slice(prefix.length).trim();
  return id || null;
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
