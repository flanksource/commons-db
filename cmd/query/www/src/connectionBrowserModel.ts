/**
 * The data layer behind every query browser: the descriptor and inspection
 * shapes the server serves, and the hook that turns a selection (database,
 * index) into the catalog and completion a browser renders. All three hosts —
 * the connection browser, the profile wizard and the profile builder — drive
 * the same plumbing from here so the surfaces cannot drift apart.
 */

import type {
  ComboboxOption,
  JsonSchemaObject,
  QueryBrowserCompletion,
} from "@flanksource/clicky-ui";
import { useQuery } from "@tanstack/react-query";
import { useMemo, type ReactNode } from "react";

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

export type CatalogNode = {
  id: string;
  label: string;
  kind: string;
  query?: string;
  options?: Record<string, unknown>;
  children?: CatalogNode[];
};

export type InspectionField = {
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

/**
 * savedConnectionID reads the id out of a `connection://<id>` reference. An
 * inline URL has no id, and so no catalog to browse — hence null rather than a
 * guess.
 */
export function savedConnectionID(value: string | undefined): string | null {
  const prefix = "connection://";
  if (!value?.startsWith(prefix)) return null;
  return value.slice(prefix.length).trim() || null;
}

export function browserBaseUrl(connectionID: string): string {
  return `/api/v1/connection/${encodeURIComponent(connectionID)}/browser`;
}

export async function fetchJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, init);
  if (!response.ok) {
    const body = await response.text();
    throw new Error(body.trim() || `request failed: ${response.status}`);
  }
  return response.json() as Promise<T>;
}

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

/**
 * queryBrowserOptionsSchema is what the inline options form edits. The index has
 * its own combobox above the browser and the structured search has the filter
 * builder, so neither is rendered a second time as a generic field.
 */
export function queryBrowserOptionsSchema(
  descriptor: BrowserDescriptor,
): JsonSchemaObject | undefined {
  if (!descriptor.optionsSchema) return undefined;
  const properties = { ...(descriptor.optionsSchema.properties ?? {}) };
  delete properties.search;
  if (descriptor.provider === "opensearch") delete properties.index;
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

/**
 * mergeProviderOptions layers the option sources a browser draws on, in
 * increasing precedence, and pins the active database when there is one.
 * `targetKind` only tells the inspection endpoint which field mappings to
 * fetch, so it is dropped unless the caller is feeding the browser itself.
 */
export function mergeProviderOptions(input: {
  layers: Array<Record<string, unknown> | undefined>;
  database?: string;
  keepTargetKind?: boolean;
}): Record<string, unknown> {
  const merged: Record<string, unknown> = {};
  for (const layer of input.layers) Object.assign(merged, layer ?? {});
  if (input.database) merged.database = input.database;
  if (!input.keepTargetKind) delete merged.targetKind;
  return merged;
}

export type InspectionScope = {
  /** Query-cache namespace, so each host keeps its own inspection cache. */
  cacheKey: string;
  id: string;
  baseUrl: string;
  enabled: boolean;
  /** The database the author picked; empty means the connection's default. */
  database: string;
  /** A database carried by the stored provider options, tried before the default. */
  fallbackDatabase?: string;
  /** The selected index, alias or data stream. */
  target: string;
  /** An explicit target kind; resolved from the catalog when absent. */
  targetKind?: string;
};

export type Inspection = {
  data?: BrowserInspection;
  nodes: CatalogNode[];
  databases: string[];
  activeDatabase: string;
  /** The database to send with a query — empty unless the source is SQL. */
  sqlDatabase: string;
  targetKind: string;
  loading: boolean;
  error: unknown;
  completion?: QueryBrowserCompletion;
};

/**
 * useInspection resolves the catalog for a browser: the base inspection, the
 * per-database one a SQL author switched to, and the per-target field mappings
 * an OpenSearch author needs for completion.
 */
export function useInspection(scope: InspectionScope): Inspection {
  const { cacheKey, id, baseUrl } = scope;
  const base = useQuery({
    queryKey: [cacheKey, id],
    queryFn: () => fetchJSON<BrowserInspection>(`${baseUrl}/inspect`),
    enabled: scope.enabled,
    retry: 0,
    staleTime: 5 * 60_000,
  });
  const switchedDatabase =
    scope.database !== "" && scope.database !== base.data?.database;
  const database = useQuery({
    queryKey: [cacheKey, id, scope.database],
    queryFn: () => {
      const params = new URLSearchParams({ database: scope.database });
      return fetchJSON<BrowserInspection>(`${baseUrl}/inspect?${params}`);
    },
    enabled: base.data?.kind === "sql" && switchedDatabase,
    retry: 0,
    staleTime: 5 * 60_000,
  });
  const active = switchedDatabase ? database : base;
  const data = active.data ?? base.data;

  const targetKind =
    scope.targetKind ??
    data?.targets?.find((target) => target.name === scope.target)?.kind ??
    "";
  const target = useQuery({
    queryKey: [cacheKey, id, targetKind, scope.target],
    queryFn: () => {
      const params = new URLSearchParams({
        target: scope.target,
        targetKind,
      });
      return fetchJSON<BrowserInspection>(`${baseUrl}/inspect?${params}`);
    },
    enabled: data?.kind === "opensearch" && scope.target !== "" && targetKind !== "",
    retry: 0,
    staleTime: 5 * 60_000,
  });

  const activeDatabase =
    scope.database || scope.fallbackDatabase || data?.database || "";
  const completion = useMemo(
    () => completionForInspection(data, target.data),
    [data, target.data],
  );
  return {
    data,
    nodes: data?.nodes ?? [],
    databases: base.data?.databases ?? [],
    activeDatabase,
    sqlDatabase: data?.kind === "sql" ? activeDatabase : "",
    targetKind,
    loading: active.isLoading,
    error: active.error,
    completion,
  };
}
