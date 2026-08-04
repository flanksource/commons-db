/**
 * The query builder panel and the form extension that mounts it. The panel is
 * pure: it takes a specification and the vocabulary the schema described, and
 * reports edits. Everything that needs a connection — field mappings and the
 * compiled preview — is wired by the host that has one.
 */

import {
  Combobox,
  type JsonSchemaProperty,
  type PostExtension,
} from "@flanksource/clicky-ui";
import {
  browserBaseUrl,
  savedConnectionID,
  useInspection,
} from "./connectionBrowserModel";
import {
  conditionAt,
  emptyGroup,
  insertAt,
  removeAt,
  updateAt,
  type EsSearch,
} from "./esQueryBuilderModel";
import {
  makeFieldValueLookup,
  valueLookupField,
  type FieldValuesSource,
} from "./esFieldValues";
import { EsQueryClauseGroup } from "./esQueryClauseGroup";
import type {
  EsQueryContext,
  EsQueryTreeActions,
} from "./esQueryConditionRow";
import {
  esBuilderVocabulary,
  fieldFamily,
  type EsBuilderVocabulary,
  type EsFieldMapping,
} from "./esQueryOperators";
import { EsQueryOutputEditor } from "./esQueryOutputEditor";
import {
  EsQueryPreview,
  useCompiledSearch,
  type EsCompilation,
} from "./esQueryPreview";
import { EsQuerySortEditor } from "./esQuerySortEditor";
import type { ProfileDraft } from "./profileBuilderWorkspace";
import type { ParamDraft } from "./profileWizardModel";

export type EsQueryBuilderProps = {
  search: EsSearch;
  onChange: (search: EsSearch) => void;
  fields: EsFieldMapping[];
  vocabulary: EsBuilderVocabulary;
  /** Declared profile parameters an operand can bind to. */
  params?: string[];
  /** Where a field's own values come from; absent without a connection. */
  values?: FieldValuesSource;
  compilation?: EsCompilation;
  className?: string;
};

export function EsQueryBuilder({
  search,
  onChange,
  fields,
  vocabulary,
  params = [],
  values,
  compilation,
  className,
}: EsQueryBuilderProps) {
  const root = search.query ?? emptyGroup();
  const context: EsQueryContext = {
    fields,
    vocabulary,
    params,
    // The builder owns the tree, so it — not the host — decides what a lookup is
    // scoped by: the query without the row being edited. Leaving that row in
    // would filter the suggestions by the half-typed value they are meant to
    // complete.
    ...(values
      ? {
          values: ({ path, field }) => {
            const target = valueLookupField(fields, field);
            if (!target) return undefined;
            return values({ field: target, search: { ...search, query: removeAt(root, path) } });
          },
        }
      : {}),
  };
  const actions: EsQueryTreeActions = {
    update: (path, update) =>
      onChange({ ...search, query: updateAt(root, path, update) }),
    insert: (groupPath, condition) =>
      onChange({
        ...search,
        query: insertAt(
          root,
          groupPath,
          conditionAt(root, groupPath)?.conditions?.length ?? 0,
          condition,
        ),
      }),
    remove: (path) => onChange({ ...search, query: removeAt(root, path) }),
  };

  return (
    <div className={className ?? "flex min-w-0 flex-col gap-3"}>
      <EsQueryClauseGroup
        condition={root}
        path={[]}
        context={context}
        actions={actions}
        root
      />
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-xs text-muted-foreground">Time field</span>
        <Combobox
          ariaLabel="Time field"
          className="min-w-48"
          value={search.timeField ?? ""}
          onChange={(next) => onChange({ ...search, timeField: next || undefined })}
          options={fields
            .filter((field) => fieldFamily(field) === "date")
            .map((field) => ({ value: field.name, label: field.name }))}
          placeholder="Date field…"
          allowCustomValue
        />
        <span className="text-xs text-muted-foreground">
          Where time-from and time-to parameters apply
        </span>
      </div>
      <EsQuerySortEditor
        sort={search.sort ?? []}
        fields={fields}
        orders={vocabulary.sortOrders}
        onChange={(sort) => onChange({ ...search, sort: sort.length ? sort : undefined })}
      />
      <EsQueryOutputEditor
        search={search}
        onChange={(patch) => onChange({ ...search, ...patch })}
      />
      {compilation ? <EsQueryPreview compilation={compilation} /> : null}
    </div>
  );
}

/** paramNames lists the parameters an operand may bind, in declared order. */
export function paramNames(params: ParamDraft[] | undefined): string[] {
  return (params ?? [])
    .map((param) => param.name ?? "")
    .filter((name) => name !== "");
}

/** paramRoles is the name-to-role table the compiler folds roles from. */
export function paramRoles(
  params: ParamDraft[] | undefined,
): Record<string, string> {
  const roles: Record<string, string> = {};
  for (const param of params ?? []) {
    if (param.name && param.role) roles[param.name] = param.role;
  }
  return roles;
}

/**
 * defaultParamValues is what the declared parameters resolve to before anyone
 * filters. The compiler needs them to bind a {param:…} operand and to
 * interpolate a {{.params.…}} one, so the preview shows the DSL a run produces.
 */
export function defaultParamValues(
  params: ParamDraft[] | undefined,
): Record<string, unknown> {
  return Object.fromEntries(
    (params ?? [])
      .filter((param) => param.name && param.default !== undefined)
      .map((param) => [param.name as string, param.default]),
  );
}

/**
 * esQueryFields reads the field mappings off a browser inspection. Only an
 * OpenSearch target carries them, so anything else builds against free text.
 */
export function esQueryFields(completion: unknown): EsFieldMapping[] {
  const typed = completion as { kind?: string; fields?: EsFieldMapping[] };
  return typed?.kind === "json-fields" ? (typed.fields ?? []) : [];
}

const esQueryBuilderPost: PostExtension = (field, nodes, ctx) => {
  if (field.schema["x-clicky-component"] !== "es-query-builder") return nodes;
  return {
    label: nodes.label,
    value: (
      <EsQueryBuilderField
        search={(field.value ?? {}) as EsSearch}
        onChange={field.onChange}
        schema={field.schema}
        rootValue={(ctx?.rootValue ?? {}) as ProfileDraft}
      />
    ),
  };
};

export const esQueryBuilderFormExtensions = { post: [esQueryBuilderPost] };

/**
 * The builder as the profile form mounts it. The form knows the connection and
 * the index, so the field mappings and the compiled preview come from the same
 * browser endpoints the connection browser uses.
 */
function EsQueryBuilderField({
  search,
  onChange,
  schema,
  rootValue,
}: {
  search: EsSearch;
  onChange: (next: unknown) => void;
  schema: JsonSchemaProperty;
  rootValue: ProfileDraft;
}) {
  const connectionID = savedConnectionID(rootValue.provider?.connection);
  const baseUrl = connectionID ? browserBaseUrl(connectionID) : "";
  const target = String(rootValue.provider?.options?.index ?? "");
  const inspection = useInspection({
    cacheKey: "es-query-builder",
    id: connectionID ?? "",
    baseUrl,
    enabled: baseUrl !== "",
    database: "",
    target,
  });
  const roles = paramRoles(rootValue.params);
  const params = defaultParamValues(rootValue.params);
  const compilation = useCompiledSearch({
    baseUrl,
    search,
    params,
    roles,
    enabled: baseUrl !== "",
  });
  const values = makeFieldValueLookup({ baseUrl, index: target, params, roles });

  return (
    <EsQueryBuilder
      search={search}
      onChange={(next) => onChange(next)}
      fields={esQueryFields(inspection.completion)}
      vocabulary={esBuilderVocabulary({ properties: { search: schema } })}
      params={paramNames(rootValue.params)}
      {...(values ? { values } : {})}
      compilation={compilation}
    />
  );
}
