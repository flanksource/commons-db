import { Icon, TreeNode } from "@flanksource/clicky-ui";
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
import type { CatalogNode } from "./connectionBrowserModel";

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
