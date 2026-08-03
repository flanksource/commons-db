/**
 * The single query-browser host. The connection browser, the profile wizard and
 * the profile-edit builder all render this: they own where the query and the
 * options are stored and what a run means, and this owns the browser itself —
 * the catalog navigator, the structured filter builder, the completion, and the
 * descriptor's result view.
 */

import {
  LogsTable,
  QueryBrowser,
  Tabs,
  type JsonSchemaObject,
  type QueryBrowserRequest,
  type QueryBrowserResult,
  type QueryBrowserResultContext,
} from "@flanksource/clicky-ui";
import { useState, type ReactNode } from "react";
import { CatalogTree } from "./catalogTree";
import type {
  BrowserDescriptor,
  CatalogNode,
  Inspection,
} from "./connectionBrowserModel";
import { EsQueryBuilder, esQueryFields } from "./esQueryBuilder";
import {
  toBuilderMode,
  toRawMode,
  type EsSearch,
  type QueryModeTransition,
} from "./esQueryBuilderModel";
import {
  esBuilderVocabulary,
  operatorCatalogFromSchema,
} from "./esQueryOperators";
import { useCompiledSearch } from "./esQueryPreview";
import { PrometheusResults } from "./prometheusResults";

export type NavigatorTab = { id: "catalog" | "filters"; label: string };

/**
 * A source supports the builder when the server described a structured search on
 * it. The operator catalog travels with the schema, so no provider name is
 * hardcoded here — adding a structured provider in Go reaches the editor on its
 * own.
 */
export function supportsQueryBuilder(descriptor: BrowserDescriptor): boolean {
  return operatorCatalogFromSchema(descriptor.optionsSchema).length > 0;
}

/**
 * navigatorTabs is what the left pane offers. A source with neither a catalog
 * nor a structured search has no navigator at all.
 */
export function navigatorTabs(input: {
  descriptor: BrowserDescriptor;
  builder: boolean;
}): NavigatorTab[] {
  const tabs: NavigatorTab[] = [];
  if (input.descriptor.catalog) tabs.push({ id: "catalog", label: "Catalog" });
  if (input.builder) tabs.push({ id: "filters", label: "Filters" });
  return tabs;
}

export type ConnectionQueryWorkspaceProps = {
  id: string;
  title: string;
  descriptor: BrowserDescriptor;
  inspection: Inspection;
  onDatabaseChange: (database: string) => void;
  query: string;
  onQueryChange?: (query: string) => void;
  options: Record<string, unknown>;
  onOptionsChange: (options: Record<string, unknown>) => void;
  onCatalogSelect: (node: CatalogNode) => void;
  optionsSchema?: JsonSchemaObject;
  /**
   * The structured specification this host stores, when it stores one.
   * `undefined` means the raw query is the artifact.
   */
  search?: EsSearch;
  /**
   * Every change reports the specification and the raw query together, and one
   * of the two is always empty. The host stores both in one write, so it can
   * never end up holding a specification and a query at once — a state the
   * server rejects.
   */
  onSearchChange?: (transition: QueryModeTransition) => void;
  /** Declared profile parameter names an operand can bind to. */
  params?: string[];
  /** Those parameters' roles, so the compiled preview folds them as a run would. */
  paramRoles?: Record<string, string>;
  /** Where POST /compile lives. Empty leaves the preview unresolved. */
  compileBaseUrl?: string;
  execute: (request: QueryBrowserRequest) => Promise<QueryBrowserResult>;
  renderResults?: (context: QueryBrowserResultContext) => ReactNode;
  className?: string;
};

export function ConnectionQueryWorkspace({
  id,
  title,
  descriptor,
  inspection,
  onDatabaseChange,
  query,
  onQueryChange,
  options,
  onOptionsChange,
  onCatalogSelect,
  optionsSchema,
  search,
  onSearchChange,
  params,
  paramRoles,
  compileBaseUrl = "",
  execute,
  renderResults,
  className,
}: ConnectionQueryWorkspaceProps) {
  const builder = Boolean(onSearchChange) && supportsQueryBuilder(descriptor);
  const tabs = navigatorTabs({ descriptor, builder });
  const [tab, setTab] = useState<string>(
    search ? "filters" : (tabs[0]?.id ?? "catalog"),
  );
  const compilation = useCompiledSearch({
    baseUrl: compileBaseUrl,
    search: search ?? {},
    ...(paramRoles ? { roles: paramRoles } : {}),
    enabled: Boolean(search) && compileBaseUrl !== "",
  });

  // While a specification is active the editor mirrors what it compiles to. It
  // is a preview, not an input: the query is not stored alongside the spec, so
  // there is no keystroke for a compile to overwrite.
  const specMode = search !== undefined;
  const active = tabs.some((entry) => entry.id === tab) ? tab : tabs[0]?.id;

  return (
    <QueryBrowser
      id={id}
      title={title}
      language={descriptor.language ?? "text"}
      queryLabel={descriptor.queryLabel ?? "Query"}
      initialQuery={specMode ? compilation.query : query}
      optionsSchema={optionsSchema}
      initialOptions={options}
      completion={inspection.completion}
      {...(specMode ? {} : onQueryChange ? { onQueryChange } : {})}
      onOptionsChange={onOptionsChange}
      className={className}
      navigator={
        tabs.length === 0 ? undefined : (
          <div className="flex min-h-0 flex-col gap-2">
            {tabs.length > 1 ? (
              <Tabs tabs={tabs} value={active ?? ""} onChange={setTab} />
            ) : null}
            {active === "filters" && onSearchChange ? (
              search ? (
                <EsQueryBuilder
                  search={search}
                  onChange={(next) => onSearchChange({ search: next, query: "" })}
                  fields={esQueryFields(inspection.completion)}
                  vocabulary={esBuilderVocabulary(descriptor.optionsSchema)}
                  {...(params ? { params } : {})}
                  compilation={compilation}
                  onEditRawDsl={() =>
                    confirmSwitch(
                      "Discard these filters and edit the compiled DSL by hand?",
                      () =>
                        onSearchChange(
                          toRawMode(search, compilation.query, query),
                        ),
                    )
                  }
                />
              ) : (
                <StartBuilding
                  onStart={() =>
                    confirmSwitch(
                      "Discard the current query and build filters?",
                      () => onSearchChange(toBuilderMode()),
                    )
                  }
                />
              )
            ) : (
              <CatalogTree
                nodes={inspection.nodes}
                loading={inspection.loading}
                error={inspection.error}
                databases={inspection.databases}
                database={inspection.activeDatabase}
                onDatabaseChange={onDatabaseChange}
                onSelect={onCatalogSelect}
              />
            )}
          </div>
        )
      }
      execute={(request) =>
        execute(
          specMode
            ? { ...request, query: "", options: { ...request.options, search } }
            : request,
        )
      }
      renderResults={renderResults ?? descriptorResultView(descriptor)}
    />
  );
}

/** Switching authoring modes drops the other one's work, so it is confirmed. */
function confirmSwitch(question: string, apply: () => void) {
  if (window.confirm(question)) apply();
}

/**
 * Starting the builder discards the raw query rather than parsing it — the
 * specification is the artifact from here on, and holding both is an authoring
 * error the server rejects.
 */
function StartBuilding({ onStart }: { onStart: () => void }) {
  return (
    <div className="rounded-md border border-dashed p-3 text-xs text-muted-foreground">
      <p>
        Build filters structurally instead of writing Query DSL. The query below
        becomes a compiled preview and is no longer stored.
      </p>
      <button
        type="button"
        className="mt-2 rounded border px-2 py-1 font-medium text-foreground hover:bg-muted"
        onClick={onStart}
      >
        Build filters
      </button>
    </div>
  );
}

/**
 * descriptorResultView honours the view the server nominated for this provider.
 * A host that renders its own results (the profile builder's column picker)
 * passes renderResults and takes over entirely.
 */
function descriptorResultView(
  descriptor: BrowserDescriptor,
): ((context: QueryBrowserResultContext) => ReactNode) | undefined {
  if (descriptor.resultView === "logs") {
    return ({ result, defaultView }) =>
      result.rows?.length ? (
        <LogsTable logs={result.rows} autoFilter={false} fullscreenTitle="Logs" />
      ) : (
        defaultView
      );
  }
  if (descriptor.resultView === "timeseries") {
    return ({ result, defaultView }) => (
      <PrometheusResults result={result} fallback={defaultView} />
    );
  }
  return undefined;
}
