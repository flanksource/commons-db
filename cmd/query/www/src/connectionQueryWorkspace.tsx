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
  type QueryBrowserFilterLookup,
  type QueryBrowserRequest,
  type QueryBrowserResult,
  type QueryBrowserResultContext,
} from "@flanksource/clicky-ui";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { CatalogTree } from "./catalogTree";
import {
  withTarget,
  type BrowserDescriptor,
  type CatalogNode,
  type Inspection,
  type ProfileRowLimits,
} from "./connectionBrowserModel";
import { makeFieldValueLookup } from "./esFieldValues";
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
import { QueryRowLimits } from "./queryRowLimits";
import { QueryTargetPicker } from "./queryTargetPicker";
import type { ParamMappingEdit } from "./esParamMappingModel";
import type { ParamDraft } from "./profileWizardModel";

export type NavigatorTab = { id: "catalog" | "form" | "json"; label: string };

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
 * navigatorTabs is what the left pane offers. Where the source has a structured
 * search, the two ways of authoring one — the form and the raw DSL — are tabs
 * rather than a one-way door: they are the same query, and the tab says which
 * of the two is stored. A source that picks one flat target has a combobox
 * pinned above the tabs instead of a catalog tree — its targets are a list of
 * index names, and a list is not worth navigating.
 */
export function navigatorTabs(input: {
  descriptor: BrowserDescriptor;
  builder: boolean;
}): NavigatorTab[] {
  const tabs: NavigatorTab[] = [];
  if (input.descriptor.catalog && !input.descriptor.targetLabel) {
    tabs.push({ id: "catalog", label: "Catalog" });
  }
  if (input.builder) {
    tabs.push({ id: "form", label: "Form" }, { id: "json", label: "JSON" });
  }
  return tabs;
}

/**
 * initialNavigatorTab opens on the form: filters are what the builder is for,
 * so it is where authoring starts rather than something to opt into. The one
 * thing that overrides it is a raw query already worth preserving — and the
 * starter query the descriptor supplies is not one, since nobody wrote it.
 */
export function initialNavigatorTab(input: {
  tabs: NavigatorTab[];
  search: EsSearch | undefined;
  query: string;
  defaultQuery?: string;
}): string | undefined {
  const has = (id: NavigatorTab["id"]) =>
    input.tabs.some((tab) => tab.id === id);
  if (!has("form")) return input.tabs[0]?.id;
  if (input.search) return "form";
  const authored = input.query.trim();
  return authored && authored !== (input.defaultQuery ?? "").trim()
    ? "json"
    : "form";
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
  /**
   * The row caps the edited profile sets for itself. They are profile settings,
   * not provider options, so they are stored beside the query rather than
   * through `onOptionsChange`. A host that edits no profile — the connection
   * browser — passes neither, and the caps are then not offered at all.
   */
  limits?: ProfileRowLimits;
  onLimitsChange?: (limits: ProfileRowLimits | undefined) => void;
  /** Declared profile parameters an operand can bind to. */
  params?: ParamDraft[];
  onParamMappingChange?: (edit: ParamMappingEdit) => void;
  /**
   * What those parameters currently resolve to. The server binds a {param:…}
   * operand from them and interpolates a {{.params.…}} one, so without values
   * the preview shows template text — or the compiler's refusal to guess.
   */
  paramValues?: Record<string, unknown>;
  /** Those parameters' roles, so the compiled preview folds them as a run would. */
  paramRoles?: Record<string, string>;
  /** Where POST /compile lives. Empty leaves the preview unresolved. */
  compileBaseUrl?: string;
  execute: (request: QueryBrowserRequest) => Promise<QueryBrowserResult>;
  /**
   * Answers a filter's value type-ahead. Absent leaves every filter the source
   * described showing only the values the result itself carried.
   */
  lookupFilterValues?: QueryBrowserFilterLookup;
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
  limits,
  onLimitsChange,
  params,
  onParamMappingChange,
  paramValues,
  paramRoles,
  compileBaseUrl = "",
  execute,
  lookupFilterValues,
  renderResults,
  className,
}: ConnectionQueryWorkspaceProps) {
  const builder = Boolean(onSearchChange) && supportsQueryBuilder(descriptor);
  const tabs = navigatorTabs({ descriptor, builder });
  const [tab, setTab] = useState<string | undefined>(() =>
    initialNavigatorTab({
      tabs,
      search,
      query,
      ...(descriptor.defaultQuery ? { defaultQuery: descriptor.defaultQuery } : {}),
    }),
  );
  // Picking a target has to reach the browser, which only resyncs its options
  // when `initialOptions` changes identity. The pick is layered here rather than
  // round-tripped through the host, so an options-form keystroke — which the
  // host also stores — cannot resync the browser out from under the author.
  const [picked, setPicked] = useState<Record<string, unknown>>();
  // What the browser last reported, so a pick keeps the author's other edits.
  const edited = useRef(options);
  const seed = useRef(options);
  if (seed.current !== options) {
    seed.current = options;
    edited.current = options;
    if (picked) setPicked(undefined);
  }
  const browserOptions = picked ?? options;
  const applyOptions = (next: Record<string, unknown>) => {
    edited.current = next;
    setPicked(next);
    onOptionsChange(next);
  };
  const compilation = useCompiledSearch({
    baseUrl: compileBaseUrl,
    search: search ?? {},
    ...(paramValues ? { params: paramValues } : {}),
    ...(paramRoles ? { roles: paramRoles } : {}),
    enabled: Boolean(search) && compileBaseUrl !== "",
  });
  const values = makeFieldValueLookup({
    baseUrl: compileBaseUrl,
    index: String(browserOptions.index ?? ""),
    ...(paramValues ? { params: paramValues } : {}),
    ...(paramRoles ? { roles: paramRoles } : {}),
  });

  // While a specification is active the editor mirrors what it compiles to. It
  // is a preview, not an input: the query is not stored alongside the spec, so
  // there is no keystroke for a compile to overwrite.
  const specMode = search !== undefined;
  const active = tabs.some((entry) => entry.id === tab) ? tab : tabs[0]?.id;

  // The form tab is the specification, so being on it means holding one. A tab
  // that stores nothing would leave the builder rendering a query it cannot
  // edit, and this is also what makes filters the default rather than an opt-in.
  useEffect(() => {
    if (active === "form" && onSearchChange && search === undefined) {
      onSearchChange(toBuilderMode());
    }
  }, [active, onSearchChange, search]);

  // Switching tabs is the mode switch. Each mode stores its own artifact and the
  // server rejects holding both, so leaving the form hands the raw editor the
  // DSL the specification last compiled to and drops the specification.
  const selectTab = (next: string) => {
    if (next === "json" && search && onSearchChange) {
      onSearchChange(toRawMode(search, compilation.query, query));
    }
    setTab(next);
  };

  return (
    <QueryBrowser
      id={id}
      title={title}
      language={descriptor.language ?? "text"}
      queryLabel={descriptor.queryLabel ?? "Query"}
      initialQuery={specMode ? compilation.query : query}
      optionsSchema={optionsSchema}
      initialOptions={browserOptions}
      completion={inspection.completion}
      {...(specMode ? {} : onQueryChange ? { onQueryChange } : {})}
      onOptionsChange={(next) => {
        edited.current = next;
        onOptionsChange(next);
      }}
      className={className}
      navigator={
        tabs.length === 0 && !descriptor.targetLabel ? undefined : (
          <div className="flex min-h-0 flex-col gap-2">
            {descriptor.targetLabel ? (
              <QueryTargetPicker
                label={descriptor.targetLabel}
                inspection={inspection}
                value={String(browserOptions.index ?? "")}
                onChange={(index, targetKind) =>
                  applyOptions(withTarget(edited.current, { index, targetKind }))
                }
              />
            ) : null}
            {builder ? (
              <QueryRowLimits
                value={String(browserOptions.limit ?? "")}
                onChange={(limit) =>
                  applyOptions({ ...edited.current, limit })
                }
                {...(descriptor.rowLimits
                  ? { defaults: descriptor.rowLimits }
                  : {})}
                {...(limits ? { limits } : {})}
                {...(onLimitsChange ? { onLimitsChange } : {})}
              />
            ) : null}
            {tabs.length > 1 ? (
              <Tabs tabs={tabs} value={active ?? ""} onChange={selectTab} />
            ) : null}
            {active === "form" && onSearchChange && search ? (
              <EsQueryBuilder
                search={search}
                onChange={(next) => onSearchChange({ search: next, query: "" })}
                fields={esQueryFields(inspection.completion)}
                vocabulary={esBuilderVocabulary(descriptor.optionsSchema)}
                {...(params ? { params } : {})}
                {...(onParamMappingChange
                  ? { onMappingChange: onParamMappingChange }
                  : {})}
                {...(values ? { values } : {})}
                compilation={compilation}
              />
            ) : active === "json" ? (
              <p className="rounded-md border border-dashed p-3 text-xs text-muted-foreground">
                The {descriptor.queryLabel ?? "query"} editor holds the query.
                Switch back to Form to build it from filters — the raw query is
                dropped then, since only one of the two is stored.
              </p>
            ) : active === "catalog" ? (
              <CatalogTree
                nodes={inspection.nodes}
                loading={inspection.loading}
                error={inspection.error}
                databases={inspection.databases}
                database={inspection.activeDatabase}
                onDatabaseChange={onDatabaseChange}
                onSelect={onCatalogSelect}
              />
            ) : null}
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
      {...(lookupFilterValues ? { lookupFilterValues } : {})}
      renderResults={renderResults ?? descriptorResultView(descriptor)}
    />
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
