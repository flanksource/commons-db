/**
 * The operand side of a condition row: the value an operator takes, and the
 * advanced qualifiers it emits alongside it. Which operand shape applies comes
 * from the operator's arity, and which qualifiers exist comes from the schema —
 * neither is decided here.
 */

import { InputField, Select } from "@flanksource/clicky-ui";
import { useState, type ReactNode } from "react";
import {
  isParamValue,
  type EsCondition,
  type EsValue,
} from "./esQueryBuilderModel";
import type { FieldValuesQuery } from "./esFieldValues";
import type { ParamOperand } from "./esParamMappingModel";
import { extendEsParamOperand } from "./esParamOperandExtension";
import {
  conditionOperandPatch,
  multipleConditionValues,
} from "./esQueryOperandModel";
import type { EsQualifierSchema } from "./esQueryOperators";
import { ValueCombobox, ValuesCombobox } from "./esValueCombobox";
import type { ParamDraft } from "./profileWizardModel";

// Date math a range over a date field commonly starts from. They are ordinary
// operand values that the backend resolves — this only saves the typing.
const dateMathPresets = [
  "now-15m",
  "now-1h",
  "now-24h",
  "now-7d",
  "now/d",
  "now",
];

export function ConditionOperand({
  condition,
  arity,
  rowId,
  params,
  dateMath,
  values,
  set,
  onBindParam,
  onUnbindParam,
}: {
  condition: EsCondition;
  arity: string;
  rowId: string;
  params: ParamDraft[];
  dateMath: boolean;
  /** The field's own values, when the mapping allows aggregating them. */
  values?: FieldValuesQuery;
  set: (patch: Partial<EsCondition>) => void;
  onBindParam: (operand: ParamOperand, name: string) => void;
  onUnbindParam: (name: string) => void;
}) {
  if (arity === "none" || arity === "group") return null;
  if (arity === "multiple") {
    return (
      <ValuesInput
        values={multipleConditionValues(condition)}
        params={params}
        {...(values ? { lookup: values } : {})}
        onChange={(next) => {
          if (isParamValue(next)) return onBindParam("values", next.param);
          const bound = boundParam(multipleConditionValues(condition));
          if (next === undefined && bound) return onUnbindParam(bound);
          if (!Array.isArray(next))
            throw new Error("multiple operand requires a value list");
          set(conditionOperandPatch({ arity: "multiple", values: next }));
        }}
      />
    );
  }
  if (arity === "range") {
    return (
      <span className="flex flex-wrap items-center gap-1">
        {(["gte", "lte"] as const).map((bound) => (
          <ValueInput
            key={bound}
            id={`${rowId}-${bound}`}
            label={bound === "gte" ? "From" : "To"}
            value={condition[bound]}
            params={params}
            presets={dateMath ? dateMathPresets : []}
            onChange={(next) => {
              if (isParamValue(next)) return onBindParam(bound, next.param);
              const mapped = boundParam(condition[bound]);
              if (next === undefined && mapped) return onUnbindParam(mapped);
              set(
                conditionOperandPatch({ arity: "range", bound, value: next }),
              );
            }}
          />
        ))}
      </span>
    );
  }
  return (
    <ValueInput
      id={`${rowId}-value`}
      label="Value"
      value={condition.value}
      params={params}
      presets={dateMath ? dateMathPresets : []}
      {...(values ? { lookup: values } : {})}
      onChange={(next) => {
        if (isParamValue(next)) return onBindParam("value", next.param);
        const bound = boundParam(condition.value);
        if (next === undefined && bound) return onUnbindParam(bound);
        set(conditionOperandPatch({ arity: "single", value: next }));
      }}
    />
  );
}

function ValueInput({
  id,
  label,
  value,
  params,
  presets,
  lookup,
  onChange,
}: {
  id: string;
  label: string;
  value: EsValue;
  params: ParamDraft[];
  presets: string[];
  lookup?: FieldValuesQuery;
  onChange: (next: EsValue) => void;
}) {
  let node: ReactNode;
  if (lookup) {
    node = (
      <ValueCombobox
        id={id}
        label={label}
        lookup={lookup}
        value={
          isParamValue(value) || value === undefined || value === null
            ? ""
            : String(value)
        }
        onChange={(next) => onChange(next === "" ? undefined : next)}
      />
    );
  } else {
    const listId = presets.length ? `es-presets-${id}` : undefined;
    node = (
      <>
        <InputField
          aria-label={label}
          className="min-w-36"
          placeholder={label}
          value={
            isParamValue(value) || value === undefined || value === null
              ? ""
              : String(value)
          }
          {...(listId ? { list: listId } : {})}
          onChange={(next) => onChange(next === "" ? undefined : next)}
        />
        {listId ? (
          <datalist id={listId}>
            {presets.map((preset) => (
              <option key={preset} value={preset} />
            ))}
          </datalist>
        ) : null}
      </>
    );
  }
  return extendEsParamOperand({
    label,
    value,
    onChange,
    node,
    params,
  });
}

function ValuesInput({
  values,
  params,
  lookup,
  onChange,
}: {
  values: EsValue[];
  params: ParamDraft[];
  lookup?: FieldValuesQuery;
  onChange: (values: EsValue[] | EsValue | undefined) => void;
}) {
  const [draft, setDraft] = useState("");
  const bound = values.filter(isParamValue);
  const literals = values.filter((value) => !isParamValue(value));
  let node: ReactNode;
  if (lookup) {
    node = (
      <ValuesCombobox
        label="Values"
        lookup={lookup}
        values={literals.map(String)}
        onChange={(next) => onChange([...next, ...bound])}
      />
    );
  } else {
    const commit = () => {
      const parsed = draft
        .split(",")
        .map((entry) => entry.trim())
        .filter(Boolean);
      if (!parsed.length) return;
      onChange([...literals, ...parsed, ...bound]);
      setDraft("");
    };
    const removeAt = (index: number) =>
      onChange([
        ...literals.filter((_value, position) => position !== index),
        ...bound,
      ]);
    node = (
      <span className="flex min-w-56 flex-1 flex-wrap items-center gap-1">
        {literals.map((value, index) => (
          <span
            key={index}
            className="es-value-chip inline-flex items-center gap-1 rounded border bg-muted px-1.5 py-0.5 text-xs"
          >
            {String(value)}
            <button
              type="button"
              aria-label={`Remove ${String(value)}`}
              className="opacity-60 hover:opacity-100"
              onClick={() => removeAt(index)}
            >
              ×
            </button>
          </span>
        ))}
        <InputField
          aria-label="Add value"
          className="min-w-32"
          placeholder="Add value…"
          value={draft}
          onChange={setDraft}
          onKeyDown={(event) => {
            if (event.key !== "Enter" && event.key !== ",") return;
            event.preventDefault();
            commit();
          }}
          onBlur={commit}
        />
      </span>
    );
  }
  return extendEsParamOperand({
    label: "value",
    value: values,
    onChange,
    node,
    params,
  });
}

function boundParam(value: EsValue | EsValue[]): string | undefined {
  const values = Array.isArray(value) ? value : [value];
  return values.find(isParamValue)?.param;
}

export function QualifierInput({
  name,
  schema,
  value,
  onChange,
}: {
  name: string;
  schema: EsQualifierSchema;
  value: unknown;
  onChange: (next: unknown) => void;
}) {
  const label = schema.title ?? name;
  if (schema.type === "boolean") {
    const checked =
      value === undefined ? schema.default === true : value === true;
    return (
      <label
        className="flex items-center gap-1 text-xs text-muted-foreground"
        title={schema.description ?? undefined}
      >
        <input
          type="checkbox"
          checked={checked}
          onChange={(event) => onChange(event.target.checked)}
        />
        {label}
      </label>
    );
  }
  if (schema.enum?.length) {
    // Select renders `placeholder` as a disabled option, which would trap the
    // qualifier once set. An explicit empty entry is what clears it again.
    return (
      <span className="w-40 shrink-0">
        <Select
          aria-label={label}
          value={value === undefined ? "" : String(value)}
          options={[
            { value: "", label: label },
            ...schema.enum.map((entry) => ({ value: entry, label: entry })),
          ]}
          onChange={(event) =>
            onChange(event.target.value === "" ? undefined : event.target.value)
          }
        />
      </span>
    );
  }
  const numeric = schema.type === "integer" || schema.type === "number";
  return (
    <InputField
      aria-label={label}
      className="w-36"
      placeholder={label}
      title={schema.description ?? undefined}
      type={numeric ? "number" : "text"}
      value={value === undefined || value === null ? "" : String(value)}
      onChange={(next) => {
        if (next === "") return onChange(undefined);
        onChange(numeric ? Number(next) : next);
      }}
    />
  );
}
