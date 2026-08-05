import {
  Select,
  applyPostExtensions,
  type FieldControl,
  type PostExtension,
} from "@flanksource/clicky-ui";
import type { ReactNode } from "react";
import { isParamValue, type EsValue } from "./esQueryBuilderModel";
import type { ParamDraft } from "./profileWizardModel";

const esParamOperandPost: PostExtension = (field, nodes, ctx) => {
  if (field.schema["x-clicky-component"] !== "es-query-operand") return nodes;
  const label = String(field.schema["x-es-operand-label"] ?? field.label);
  const params = operandParams((ctx?.rootValue?.params ?? []) as ParamDraft[]);
  const bound = boundParam(field.value);
  const selector = params.length ? (
    <span className="w-24 shrink-0">
      <Select
        aria-label={`Bind ${label} to a parameter`}
        value=""
        placeholder="param"
        options={params.map((param) => ({ value: param, label: param }))}
        onChange={(event) => {
          if (event.target.value) field.onChange({ param: event.target.value });
        }}
      />
    </span>
  ) : null;
  return {
    label: nodes.label,
    value: bound ? (
      <span className="flex min-w-0 items-center gap-1">
        {Array.isArray(field.value) ? nodes.value : null}
        <ParamMappingPill
          name={bound}
          label={label}
          onClear={() => field.onChange(undefined)}
        />
      </span>
    ) : (
      <span className="flex min-w-0 items-center gap-1">
        {nodes.value}
        {selector}
      </span>
    ),
  };
};

export function extendEsParamOperand(input: {
  label: string;
  value: EsValue | EsValue[];
  onChange: (next: unknown) => void;
  node: ReactNode;
  params: ParamDraft[];
}): ReactNode {
  const field: FieldControl = {
    key: "operand",
    kind: Array.isArray(input.value) ? "array" : "string",
    label: input.label,
    required: false,
    schema: {
      "x-clicky-component": "es-query-operand",
      "x-es-operand-label": input.label,
    },
    value: input.value,
    onChange: input.onChange,
  };
  return applyPostExtensions(
    field,
    { label: null, value: input.node },
    [esParamOperandPost],
    { rootValue: { params: input.params } },
  ).value;
}

export function ParamMappingPill({
  name,
  label,
  onClear,
}: {
  name: string;
  label: string;
  onClear: () => void;
}) {
  return (
    <span className="es-param-chip inline-flex items-center gap-1 rounded border border-primary/40 bg-primary/5 px-1.5 py-0.5 font-mono text-xs">
      {name}
      <button
        type="button"
        aria-label={`Unbind ${label}`}
        className="opacity-60 hover:opacity-100"
        onClick={onClear}
      >
        ×
      </button>
    </span>
  );
}

function operandParams(params: ParamDraft[]): string[] {
  return params.flatMap((param) =>
    param.name && (!param.role || param.role === "filter") ? [param.name] : [],
  );
}

function boundParam(value: EsValue | EsValue[]): string | undefined {
  const values = Array.isArray(value) ? value : [value];
  return values.find(isParamValue)?.param;
}
