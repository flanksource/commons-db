import { useMemo, useState, type ReactNode } from "react";
import Prism from "prismjs";
import "prismjs/components/prism-sql";
import "prismjs/themes/prism-tomorrow.css";
import {
  CodeBlock,
  ContextMenu,
  DropdownMenu,
  Icon,
  Modal,
  Tree,
  type DropdownMenuItem,
} from "@flanksource/clicky-ui";
import {
  UiCog,
  UiCopy,
  UiDatabase,
  UiEllipsis,
  UiEye,
  UiFunctionSquare,
  UiKey,
  UiLink,
  UiPlay,
  UiRestart,
  UiTable,
  UiZap,
} from "@flanksource/clicky-ui/icons";

export type SQLCatalog = {
  kind: "sql";
  driver?: string;
  dialect?: "postgresql" | "mysql" | "mssql" | "standard";
  database?: string;
  databases?: string[];
  schemas?: SQLSchema[];
  truncated?: boolean;
  truncateReason?: string;
};

type SQLSchema = {
  name: string;
  relations: SQLRelation[];
  routines?: SQLRoutine[];
};
type SQLRelation = {
  name: string;
  type?: "table" | "view";
  viewDefinition?: string;
  columns: SQLColumn[];
  indexes?: SQLIndex[];
  foreignKeys?: SQLForeignKey[];
  triggers?: SQLTrigger[];
};
type SQLColumn = {
  name: string;
  dataType?: string;
  nullable?: boolean;
  default?: string;
  identity?: boolean;
  primaryKey?: boolean;
  unique?: boolean;
  maxLength?: number;
  numericPrecision?: number;
  numericScale?: number;
  comment?: string;
};
type SQLIndex = {
  name: string;
  unique?: boolean;
  primary?: boolean;
  columns: string[];
  type?: string;
  filter?: string;
};
type SQLForeignKey = {
  name: string;
  columns: string[];
  referencedSchema?: string;
  referencedTable: string;
  referencedColumns: string[];
};
type SQLTrigger = {
  name: string;
  event?: string;
  timing?: string;
  disabled?: boolean;
  sql?: string;
};
type SQLRoutine = {
  id?: string;
  name: string;
  type: string;
  sql?: string;
  returnType?: string;
  parameters?: { name: string; dataType?: string }[];
};
type Source = { label: string; sql: string };
type CatalogItem = {
  key: string;
  kind:
    | "schema"
    | "section"
    | "relation"
    | "column"
    | "index"
    | "foreignKey"
    | "trigger"
    | "routine";
  name: string;
  identifier?: string;
  query?: string;
  source?: string;
  icon?: ReactNode;
  detail?: ReactNode;
  description?: string;
  children?: CatalogItem[];
};

/** OIPA's UIRExplorer tree presentation, adapted to CommonsDB's SQL catalog.
 * Inspection stays read-only; selecting an object only seeds the editor.
 */
export function SQLCatalogNavigator({
  catalog,
  database,
  loading,
  refreshing,
  error,
  cache,
  onDatabaseChange,
  onRefresh,
  onSelect,
  onRun,
}: {
  catalog: SQLCatalog;
  database: string;
  loading: boolean;
  refreshing: boolean;
  error: unknown;
  cache?: { state?: string; ageMs?: number; lastRefreshError?: string };
  onDatabaseChange: (database: string) => void;
  onRefresh: () => void;
  onSelect: (query: string) => void;
  onRun: (query: string) => void;
}) {
  const roots = useMemo(() => catalogItems(catalog), [catalog]);
  const [selected, setSelected] = useState<CatalogItem | null>(null);
  const [source, setSource] = useState<Source>();
  const [notice, setNotice] = useState("");
  const copy = async (identifier: string) => {
    try {
      if (!navigator.clipboard?.writeText) throw new Error("Clipboard unavailable");
      await navigator.clipboard.writeText(identifier);
      setNotice(`Copied ${identifier}`);
    } catch {
      setNotice("Could not copy identifier. Clipboard access was denied.");
    }
  };
  const actionsFor = (node: CatalogItem): DropdownMenuItem[] => [
    ...(node.query
      ? [
          {
            label: "Run query (100 rows)",
            icon: UiPlay,
            onSelect: () => onRun(node.query!),
          },
        ]
      : []),
    ...(node.identifier
      ? [
          {
            label: "Copy identifier",
            icon: UiCopy,
            onSelect: () => void copy(node.identifier!),
          },
        ]
      : []),
    ...(node.source
      ? [
          {
            label: "View SQL",
            icon: UiEye,
            onSelect: () =>
              setSource({
                label: node.identifier ?? node.name,
                sql: node.source!,
              }),
          },
        ]
      : []),
  ];

  return (
    <aside
      className="flex h-full min-h-0 flex-col gap-2 overflow-hidden p-3 text-sm"
      aria-label="SQL catalog"
    >
      <div className="flex items-center gap-2">
        <Icon icon={UiDatabase} className="shrink-0 text-blue-500" />
        <label className="min-w-0 flex-1">
          <span className="sr-only">Database</span>
          <select
            className="h-8 w-full rounded-md border bg-background px-2"
            value={database}
            disabled={catalog.driver === "sqlite"}
            onChange={(event) => onDatabaseChange(event.target.value)}
          >
            {(catalog.databases ?? []).map((name) => (
              <option key={name}>{name}</option>
            ))}
          </select>
        </label>
        <button
          className="flex h-8 items-center gap-1.5 rounded-md border px-2 text-xs hover:bg-accent"
          type="button"
          onClick={onRefresh}
          disabled={refreshing}
        >
          <Icon icon={UiRestart} className={refreshing ? "animate-spin" : ""} />
          {refreshing ? "Refreshing…" : "Refresh"}
        </button>
      </div>
      <p className="text-xs text-muted-foreground">
        Click a table to load a preview query. Right-click for actions.
      </p>
      <CatalogStatus
        loading={loading}
        error={error}
        cache={cache}
        catalog={catalog}
      />
      <Tree<CatalogItem>
        className="min-h-0 flex-1 font-mono text-xs"
        ariaLabel="Database schema"
        roots={roots}
        getKey={(node) => node.key}
        getChildren={(node) => node.children}
        getAriaLabel={(node) => node.name}
        getSearchText={(node) => node.name}
        defaultOpen={(node) =>
          node.kind === "schema" || node.kind === "section"
        }
        isSecondary={(node) =>
          ["column", "index", "foreignKey"].includes(node.kind)
        }
        selected={selected}
        onSelect={(node) => {
          setSelected(node);
          if (node.query) onSelect(node.query);
        }}
        renderRow={({ node }) => (
          <CatalogRow node={node} actions={actionsFor(node)} />
        )}
        empty={
          <p className="rounded-xl border border-dashed p-8 text-center text-muted-foreground">
            {loading ? "Introspecting schema…" : "No schemas found"}
          </p>
        }
      />
      <span role="status" className="sr-only">
        {notice}
      </span>
      <Modal
        open={!!source}
        onClose={() => setSource(undefined)}
        title={source?.label}
        size="xl"
      >
        {source && <SQLSource source={source.sql} />}
      </Modal>
    </aside>
  );
}

/** Use OIPA's Prism SQL grammar/theme without rewriting the database's definition. */
function SQLSource({ source }: { source: string }) {
  const highlightedHtml = useMemo(
    () =>
      `<pre class="language-sql"><code class="language-sql" style="white-space:pre-wrap;word-break:break-word">${Prism.highlight(source, Prism.languages.sql, "sql")}</code></pre>`,
    [source],
  );
  return (
    <CodeBlock
      source={source}
      language="sql"
      highlightedHtml={highlightedHtml}
      copyable
      className="max-h-[70vh] overflow-auto"
    />
  );
}

function CatalogRow({
  node,
  actions,
}: {
  node: CatalogItem;
  actions: DropdownMenuItem[];
}) {
  const [target, setTarget] = useState<HTMLSpanElement | null>(null);
  return (
    <span
      ref={setTarget}
      className="group/catalog-row flex min-w-0 flex-1 items-center gap-1.5 text-xs"
      title={node.description ?? node.identifier}
    >
      {node.icon}
      <span
        className={
          node.kind === "section"
            ? "text-[10px] uppercase tracking-wider text-muted-foreground"
            : `max-w-[55%] shrink-0 truncate ${["schema", "relation", "routine"].includes(node.kind) ? "font-medium" : ""}`
        }
      >
        {node.name}
      </span>
      <span className="flex min-w-0 items-center gap-1 text-muted-foreground">
        {node.detail}
      </span>
      {actions.length > 0 && (
        <span
          className="ml-auto shrink-0 opacity-0 focus-within:opacity-100 group-hover/catalog-row:opacity-100"
          onClick={(event) => event.stopPropagation()}
        >
          <DropdownMenu
            trigger={
              <button
                type="button"
                aria-label={`Actions for ${node.name}`}
                className="flex h-5 w-5 items-center justify-center rounded hover:bg-accent"
              >
                <UiEllipsis className="h-3.5 w-3.5" />
              </button>
            }
            items={actions}
          />
        </span>
      )}
      <ContextMenu
        contextTarget={target}
        menuLabel={`Actions for ${node.name}`}
        menuItems={actions}
      />
    </span>
  );
}

/** Match OIPA's schema/section/object hierarchy; column and key rows stay flat under their table. */
function catalogItems(catalog: SQLCatalog): CatalogItem[] {
  const dialect = catalog.driver === "clickhouse" ? "mysql" : catalog.dialect;
  return (catalog.schemas ?? []).map((schema) => {
    const key = JSON.stringify([schema.name]);
    const sections: CatalogItem[] = [];
    const section = (
      name: string,
      icon: ReactNode,
      children: CatalogItem[],
    ) => {
      if (children.length)
        sections.push({
          key: `${key}:${name}`,
          kind: "section",
          name,
          icon,
          detail: `(${children.length})`,
          children,
        });
    };
    const relationItems = (view: boolean) =>
      schema.relations
        .filter((relation) => (relation.type === "view") === view)
        .map((relation) =>
          relationItem(dialect, schema.name, relation),
        );
    section(
      "Tables",
      <UiTable className="h-3 w-3 shrink-0" />,
      relationItems(false),
    );
    section(
      "Views",
      <UiEye className="h-3 w-3 shrink-0" />,
      relationItems(true),
    );
    for (const procedure of [true, false]) {
      const routines = (schema.routines ?? []).filter(
        (routine) => (routine.type === "procedure") === procedure,
      );
      section(
        procedure ? "Procedures" : "Functions",
        procedure ? (
          <UiCog className="h-3 w-3" />
        ) : (
          <UiFunctionSquare className="h-3 w-3" />
        ),
        routines.map((routine) => ({
          key: JSON.stringify([
            schema.name,
            routine.type,
            routine.id ?? routineSignature(routine),
          ]),
          kind: "routine",
          name: routine.name,
          identifier: qualifiedIdentifier(
            dialect,
            schema.name,
            routine.name,
          ),
          source: routine.sql,
          icon: procedure ? (
            <UiCog className="h-3 w-3 shrink-0 text-purple-500" />
          ) : (
            <UiFunctionSquare className="h-3 w-3 shrink-0 text-teal-500" />
          ),
          detail: `(${routine.parameters?.length ?? 0})`,
          description: `${routine.name}(${(routine.parameters ?? []).map((p) => `${p.name} ${p.dataType ?? ""}`).join(", ")})${routine.returnType ? ` → ${routine.returnType}` : ""}`,
        })),
      );
    }
    return {
      key,
      kind: "schema",
      name: schema.name,
      identifier: schema.name,
      icon: <UiDatabase className="h-3.5 w-3.5 shrink-0 text-blue-500" />,
      detail: `${schema.relations.length} objects, ${schema.routines?.length ?? 0} routines`,
      children: sections,
    };
  });
}

function relationItem(
  dialect: SQLCatalog["dialect"],
  schema: string,
  relation: SQLRelation,
): CatalogItem {
  const key = JSON.stringify([schema, relation.name]);
  const identifier = qualifiedIdentifier(dialect, schema, relation.name);
  const child = (kind: CatalogItem["kind"], name: string): CatalogItem => ({
    key: `${key}:${kind}:${name}`,
    kind,
    name,
    identifier: `${identifier}.${quoteIdentifier(dialect, name)}`,
  });
  const children: CatalogItem[] = relation.columns.map((column) => {
    const type = columnType(column);
    return {
      ...child("column", column.name),
      description: [
        column.name,
        type,
        column.primaryKey && "primary key",
        column.identity && "identity",
        column.nullable === false && "not null",
        column.default != null && `default ${column.default}`,
        column.comment,
      ]
        .filter(Boolean)
        .join(" · "),
      detail: (
        <>
          <span className="truncate">{type}</span>
          {column.primaryKey && (
            <UiKey
              className="h-3 w-3 shrink-0 text-amber-500"
              aria-label="primary key"
            />
          )}
          {column.identity && <SmallBadge>identity</SmallBadge>}
          {column.nullable === false && !column.primaryKey && (
            <SmallBadge>not null</SmallBadge>
          )}
          {column.unique && <SmallBadge>unique</SmallBadge>}
          {column.default != null && (
            <span className="truncate" title={column.default}>
              default {column.default}
            </span>
          )}
        </>
      ),
    };
  });
  for (const index of relation.indexes ?? [])
    children.push({
      ...child("index", index.name),
      identifier:
        dialect === "postgresql"
          ? qualifiedIdentifier(dialect, schema, index.name)
          : quoteIdentifier(dialect, index.name),
      icon: <UiKey className="h-3 w-3 shrink-0 text-purple-400" />,
      description: `${index.name} ON ${identifier} (${index.columns.join(", ")}) ${index.type ?? ""}${index.filter ? ` WHERE ${index.filter}` : ""}`,
      detail: (
        <>
          <span className="truncate">({index.columns.join(", ")})</span>
          {index.primary ? (
            <span className="rounded bg-amber-500/10 px-1 text-amber-600 dark:text-amber-400">
              primary
            </span>
          ) : index.unique ? (
            <span className="rounded bg-blue-500/10 px-1 text-blue-600 dark:text-blue-400">
              unique
            </span>
          ) : null}
          {index.type && <SmallBadge>{index.type.toLowerCase()}</SmallBadge>}
        </>
      ),
    });
  for (const fk of relation.foreignKeys ?? []) {
    const referencedRelation = fk.referencedSchema
      ? `${fk.referencedSchema}.${fk.referencedTable}`
      : fk.referencedTable;
    children.push({
      ...child("foreignKey", fk.name),
      identifier: quoteIdentifier(dialect, fk.name),
      icon: <UiLink className="h-3 w-3 shrink-0 text-teal-400" />,
      detail: (
        <span className="truncate">
          → {referencedRelation} ({fk.referencedColumns.join(", ")})
        </span>
      ),
      description: `${fk.name} ON ${identifier}: ${fk.columns.join(", ")} → ${referencedRelation} (${fk.referencedColumns.join(", ")})`,
    });
  }
  for (const trigger of relation.triggers ?? [])
    children.push({
      ...child("trigger", trigger.name),
      identifier:
        dialect === "mssql"
          ? qualifiedIdentifier(dialect, schema, trigger.name)
          : quoteIdentifier(dialect, trigger.name),
      description: `${trigger.name} ON ${identifier}`,
      source: trigger.sql,
      icon: <UiZap className="h-3 w-3 shrink-0 text-amber-500" />,
      detail: (
        <span className="rounded bg-amber-500/10 px-1 text-amber-600 dark:text-amber-400">
          {[trigger.timing, trigger.event, trigger.disabled && "disabled"]
            .filter(Boolean)
            .join(" ")}
        </span>
      ),
    });
  return {
    key,
    kind: "relation",
    name: relation.name,
    identifier,
    source: relation.viewDefinition,
    query: selectPreview(dialect, identifier),
    children,
    icon:
      relation.type === "view" ? (
        <UiEye className="h-3 w-3 shrink-0 text-slate-500" />
      ) : (
        <UiTable className="h-3 w-3 shrink-0 text-emerald-500" />
      ),
    detail: (
      <>
        {relation.type === "view" && (
          <span className="rounded bg-slate-500/10 px-1">view</span>
        )}
        <span className="whitespace-nowrap">
          {relation.columns.length} cols
        </span>
        {!!relation.indexes?.length && (
          <span className="whitespace-nowrap">
            {relation.indexes.length} idx
          </span>
        )}
        {!!relation.foreignKeys?.length && (
          <span className="whitespace-nowrap">
            {relation.foreignKeys.length} FK
          </span>
        )}
      </>
    ),
  };
}

function SmallBadge({ children }: { children: ReactNode }) {
  return (
    <span className="whitespace-nowrap text-[9px] uppercase text-muted-foreground">
      {children}
    </span>
  );
}

function columnType(column: SQLColumn) {
  const dimensions =
    /^(decimal|numeric)$/i.test(column.dataType ?? "") &&
    column.numericPrecision != null
      ? `(${column.numericPrecision}${column.numericScale != null ? `,${column.numericScale}` : ""})`
      : column.maxLength != null
        ? `(${column.maxLength === -1 ? "max" : column.maxLength})`
        : "";
  return `${column.dataType ?? ""}${dimensions}`;
}

function routineSignature(routine: SQLRoutine) {
  return `${routine.name}(${(routine.parameters ?? []).map((parameter) => parameter.dataType ?? "").join(",")})`;
}

function CatalogStatus({
  loading,
  error,
  cache,
  catalog,
}: {
  loading: boolean;
  error: unknown;
  cache?: { state?: string; ageMs?: number; lastRefreshError?: string };
  catalog: SQLCatalog;
}) {
  if (loading)
    return <p className="text-xs text-muted-foreground">Loading catalog…</p>;
  if (error)
    return (
      <p role="alert" className="text-xs text-destructive">
        {error instanceof Error ? error.message : "Catalog inspection failed"}
      </p>
    );
  return (
    <div className="text-xs text-muted-foreground">
      {cache
        ? `${cache.state ?? "cached"}${typeof cache.ageMs === "number" ? ` · ${Math.round(cache.ageMs / 1000)}s old` : ""}`
        : "Live catalog"}
      {catalog.truncated
        ? ` · Truncated${catalog.truncateReason ? `: ${catalog.truncateReason}` : ""}`
        : ""}
      {cache?.lastRefreshError && (
        <span className="block text-destructive">
          Refresh failed: {cache.lastRefreshError}
        </span>
      )}
    </div>
  );
}

function quoteIdentifier(dialect: SQLCatalog["dialect"], value: string) {
  if (dialect === "mysql") return `\`${value.replace(/`/g, "``")}\``;
  return dialect === "mssql"
    ? `[${value.replace(/]/g, "]]")}]`
    : `"${value.split('"').join('""')}"`;
}

function qualifiedIdentifier(
  dialect: SQLCatalog["dialect"],
  schema: string,
  relation: string,
) {
  return `${quoteIdentifier(dialect, schema)}.${quoteIdentifier(dialect, relation)}`;
}

function selectPreview(dialect: SQLCatalog["dialect"], identifier: string) {
  return dialect === "mssql"
    ? `SELECT TOP 100 *\nFROM ${identifier}`
    : `SELECT *\nFROM ${identifier}\nLIMIT 100`;
}
