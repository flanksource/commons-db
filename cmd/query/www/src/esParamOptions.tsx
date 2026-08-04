/**
 * An enum parameter's options, taken from the index rather than typed. The
 * parameter itself says nothing about a field — the search specification does,
 * by binding the parameter to a condition — so the field is read back from
 * there and the same lookup the builder's operands use answers the values.
 *
 * This mounts on the parameter object rather than on its `options` array: a
 * form extension is handed a control and the form's root value, never the
 * instance path, so an extension on the array alone could not tell which
 * parameter it belongs to.
 */

import type { PostExtension } from "@flanksource/clicky-ui";
import {
  browserBaseUrl,
  savedConnectionID,
  useInspection,
} from "./connectionBrowserModel";
import { makeFieldValueLookup, valueLookupField } from "./esFieldValues";
import { esQueryFields, paramRoles } from "./esQueryBuilder";
import { fieldForParam, type EsSearch } from "./esQueryBuilderModel";
import { ValuesCombobox } from "./esValueCombobox";
import type { ProfileDraft } from "./profileBuilderWorkspace";
import type { ParamDraft } from "./profileWizardModel";

const esParamPost: PostExtension = (field, nodes, ctx) => {
  if (field.schema["x-clicky-component"] !== "es-param") return nodes;
  const param = (field.value ?? {}) as ParamDraft;
  if (param.type !== "enum") return nodes;
  return {
    label: nodes.label,
    value: (
      <>
        {nodes.value}
        <EsParamOptionsField
          param={param}
          rootValue={(ctx?.rootValue ?? {}) as ProfileDraft}
          onChange={(options) => field.onChange({ ...param, options })}
        />
      </>
    ),
  };
};

export const esParamOptionsFormExtensions = { post: [esParamPost] };

/**
 * The options picker as the profile form mounts it. It renders only where the
 * parameter is actually answerable from the index: a saved connection, an
 * index, a condition binding the parameter, and a field that can be aggregated.
 */
function EsParamOptionsField({
  param,
  rootValue,
  onChange,
}: {
  param: ParamDraft;
  rootValue: ProfileDraft;
  onChange: (options: string[]) => void;
}) {
  const connectionID = savedConnectionID(rootValue.provider?.connection);
  const baseUrl = connectionID ? browserBaseUrl(connectionID) : "";
  const target = String(rootValue.provider?.options?.index ?? "");
  const search = rootValue.provider?.options?.search as EsSearch | undefined;
  const inspection = useInspection({
    cacheKey: "es-param-options",
    id: connectionID ?? "",
    baseUrl,
    enabled: baseUrl !== "" && target !== "",
    database: "",
    target,
  });

  const bound = fieldForParam(search, param.name ?? "");
  const lookupField = bound
    ? valueLookupField(esQueryFields(inspection.completion), bound)
    : undefined;
  const source = makeFieldValueLookup({
    baseUrl,
    index: target,
    roles: paramRoles(rootValue.params),
  });
  if (!lookupField || !source) return null;

  return (
    <div className="mt-1 flex flex-col gap-1">
      <span className="text-xs text-muted-foreground">
        Options from {lookupField}
      </span>
      <ValuesCombobox
        label="Options"
        lookup={source({ field: lookupField })}
        values={param.options ?? []}
        onChange={onChange}
      />
    </div>
  );
}
