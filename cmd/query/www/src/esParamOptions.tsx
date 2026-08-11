import {
  Combobox,
  type PostExtension,
  type PreExtension,
} from "@flanksource/clicky-ui";
import {
  addParamMapping,
  browserBaseUrl,
  esQueryFields,
  makeFieldValueLookup,
  paramHasOptions,
  paramMappings,
  paramRoles,
  reconcileParamMappings,
  removeParamMapping,
  savedConnectionID,
  useInspection,
  ValuesCombobox,
  valueLookupField,
  type EsSearch,
  type ParamDraft,
  type ParamMapping,
  type ParamMappingEdit,
  type ProfileDraft,
} from "@flanksource/clicky-ui/profiles";
import { ListValueFileButton } from "./listValuePicker";

const esParamsPre: PreExtension = (field, ctx) => {
  if (field.schema["x-clicky-component"] === "es-param-field") return null;
  if (field.schema["x-clicky-component"] !== "es-params") return field;
  return {
    ...field,
    onChange: (next) => {
      const root = (ctx.rootValue ?? {}) as ProfileDraft;
      const params = next as ParamDraft[];
      const search = structuredSearch(root);
      if (!search) return field.onChange(params);
      if (!ctx.onRootChange) {
        throw new Error(
          "structured parameter edits require an atomic root form update",
        );
      }
      commitMapping(
        root,
        reconcileParamMappings({
          search,
          previous: (field.value ?? []) as ParamDraft[],
          next: params,
        }),
        ctx.onRootChange,
      );
    },
  };
};

const esParamPost: PostExtension = (field, nodes, ctx) => {
  if (field.schema["x-clicky-component"] !== "es-param") return nodes;
  const param = (field.value ?? {}) as ParamDraft;
  return {
    label: nodes.label,
    value: (
      <>
        {nodes.value}
        <EsParamFields
          param={param}
          rootValue={(ctx?.rootValue ?? {}) as ProfileDraft}
          onOptionsChange={(options) => field.onChange({ ...param, options })}
          {...(ctx?.onRootChange ? { onRootChange: ctx.onRootChange } : {})}
        />
      </>
    ),
  };
};

export const esParamOptionsFormExtensions = {
  pre: [esParamsPre],
  post: [esParamPost],
};

function EsParamFields({
  param,
  rootValue,
  onOptionsChange,
  onRootChange,
}: {
  param: ParamDraft;
  rootValue: ProfileDraft;
  onOptionsChange: (options: string[]) => void;
  onRootChange?: (next: Record<string, unknown>) => void;
}) {
  const connectionID = savedConnectionID(rootValue.provider?.connection);
  const baseUrl = connectionID ? browserBaseUrl(connectionID) : "";
  const target = String(rootValue.provider?.options?.index ?? "");
  const search = structuredSearch(rootValue);
  const inspection = useInspection({
    cacheKey: "es-param-options",
    id: connectionID ?? "",
    baseUrl,
    enabled: baseUrl !== "" && target !== "",
    database: "",
    target,
  });
  const fields = esQueryFields(inspection.completion);
  const mappings = mappingRows(search, param);
  const field = mappings[0]?.field ?? param.field;
  const lookupField = field ? valueLookupField(fields, field) : undefined;
  const source = makeFieldValueLookup({
    baseUrl,
    index: target,
    roles: paramRoles(rootValue.params),
  });
  const name = param.name ?? "";
  const automatic = param.role === "limit" || param.role === "offset";

  return (
    <div className="mt-1 flex flex-col gap-1">
      {automatic ? (
        <span className="text-xs text-muted-foreground">
          Mapped automatically to the result {param.role}.
        </span>
      ) : (
        <>
          {mappings.length ? (
            <div className="flex flex-wrap gap-1">
              {mappings.map((mapping) => (
                <FieldMappingPill
                  key={`${mapping.path.join(".")}:${mapping.field}`}
                  field={mapping.field}
                  onClear={() => {
                    if (!search || !onRootChange || !name) return;
                    commitMapping(
                      rootValue,
                      removeParamMapping({
                        search,
                        params: rootValue.params ?? [],
                        name,
                        ...(mapping.structural ? { path: mapping.path } : {}),
                      }),
                      onRootChange,
                    );
                  }}
                />
              ))}
            </div>
          ) : null}
          {search ? (
            <Combobox
              ariaLabel={`Map ${name || "parameter"} to a field`}
              className="min-w-56"
              value=""
              options={fields.map((entry) => ({
                value: entry.name,
                label: entry.name,
              }))}
              placeholder="Map to field…"
              allowCustomValue
              disabled={!name || !onRootChange}
              onChange={(next) => {
                if (!next || !name || !onRootChange) return;
                commitMapping(
                  rootValue,
                  addParamMapping({
                    search,
                    params: rootValue.params ?? [],
                    name,
                    field: next,
                  }),
                  onRootChange,
                );
              }}
            />
          ) : (
            <span className="text-xs text-muted-foreground">
              Switch Source &amp; Query to Form to map this parameter to a
              field.
            </span>
          )}
        </>
      )}
      {paramHasOptions(param) ? (
        <>
          {lookupField && source ? (
            <>
              <span className="text-xs text-muted-foreground">
                Options from {lookupField}
              </span>
              <ValuesCombobox
                label="Options"
                lookup={source({ field: lookupField })}
                values={param.options ?? []}
                onChange={onOptionsChange}
              />
            </>
          ) : null}
          <ListValueFileButton
            title="Load this parameter's options from a CSV, JSON or text file"
            onValues={(values) => {
              const merged = [...(param.options ?? [])];
              for (const value of values) {
                if (!merged.includes(value)) merged.push(value);
              }
              onOptionsChange(merged);
              return merged.length - (param.options?.length ?? 0);
            }}
          />
        </>
      ) : null}
    </div>
  );
}

type MappingRow = ParamMapping & { structural: boolean };

function mappingRows(
  search: EsSearch | undefined,
  param: ParamDraft,
): MappingRow[] {
  if (!search || !param.name)
    return param.field
      ? [{ path: [], field: param.field, operand: "value", structural: false }]
      : [];
  if (param.role === "time-from" || param.role === "time-to") {
    return search.timeField
      ? [
          {
            path: [],
            field: search.timeField,
            operand: "value",
            structural: false,
          },
        ]
      : [];
  }
  const mapped = paramMappings(search, param.name);
  return mapped.length || !param.field
    ? mapped.map((mapping) => ({ ...mapping, structural: true }))
    : [{ path: [], field: param.field, operand: "value", structural: false }];
}

function FieldMappingPill({
  field,
  onClear,
}: {
  field: string;
  onClear: () => void;
}) {
  return (
    <span className="inline-flex items-center gap-1 rounded-full border border-primary/30 bg-primary/5 px-2 py-0.5 font-mono text-xs">
      {field}
      <button
        type="button"
        aria-label={`Remove mapping to ${field}`}
        onClick={onClear}
      >
        ×
      </button>
    </span>
  );
}

function structuredSearch(root: ProfileDraft): EsSearch | undefined {
  return root.provider?.options?.search as EsSearch | undefined;
}

function commitMapping(
  root: ProfileDraft,
  edit: ParamMappingEdit,
  onRootChange: (next: Record<string, unknown>) => void,
) {
  onRootChange({
    ...root,
    params: edit.params,
    provider: {
      ...(root.provider ?? {}),
      options: { ...(root.provider?.options ?? {}), search: edit.search },
    },
  });
}
