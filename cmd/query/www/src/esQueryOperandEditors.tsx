/**
 * The operand side of a condition row: the value an operator takes, and the
 * advanced qualifiers it emits alongside it. Which operand shape applies comes
 * from the operator's arity, and which qualifiers exist comes from the schema —
 * neither is decided here.
 */

import { InputField, Select } from "@flanksource/clicky-ui";
import { useState } from "react";
import {
  paramName,
  type EsCondition,
  type EsValue,
} from "./esQueryBuilderModel";
import type { EsQualifierSchema } from "./esQueryOperators";

// Date math a range over a date field commonly starts from. They are ordinary
// operand values that the backend resolves — this only saves the typing.
const dateMathPresets = ["now-15m", "now-1h", "now-24h", "now-7d", "now/d", "now"];

export function ConditionOperand({
  condition,
  arity,
  rowId,
  params,
  dateMath,
  set,
}: {
  condition: EsCondition;
  arity: string;
  rowId: string;
  params: string[];
  dateMath: boolean;
  set: (patch: Partial<EsCondition>) => void;
}) {
  if (arity === "none" || arity === "group") return null;
  if (arity === "multiple") {
    return (
      <ValuesInput
        values={condition.values ?? []}
        params={params}
        onChange={(values) => set({ values })}
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
            onChange={(next) => set({ [bound]: next })}
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
      onChange={(next) => set({ value: next })}
    />
  );
}

function ValueInput({
  id,
  label,
  value,
  params,
  presets,
  onChange,
}: {
  id: string;
  label: string;
  value: EsValue;
  params: string[];
  presets: string[];
  onChange: (next: EsValue) => void;
}) {
  const bound = paramName(value);
  if (bound !== undefined) {
    return <ParamChip name={bound} label={label} onClear={() => onChange(undefined)} />;
  }
  const listId = presets.length ? `es-presets-${id}` : undefined;
  return (
    <span className="flex items-center gap-1">
      <InputField
        aria-label={label}
        className="min-w-36"
        placeholder={label}
        value={value === undefined || value === null ? "" : String(value)}
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
      <ParamBinder
        label={label}
        params={params}
        onBind={(param) => onChange({ param })}
      />
    </span>
  );
}

function ValuesInput({
  values,
  params,
  onChange,
}: {
  values: EsValue[];
  params: string[];
  onChange: (values: EsValue[]) => void;
}) {
  const [draft, setDraft] = useState("");
  const commit = () => {
    const parsed = draft
      .split(",")
      .map((entry) => entry.trim())
      .filter(Boolean);
    if (!parsed.length) return;
    onChange([...values, ...parsed]);
    setDraft("");
  };
  const removeAt = (index: number) =>
    onChange(values.filter((_value, position) => position !== index));
  return (
    <span className="flex min-w-56 flex-1 flex-wrap items-center gap-1">
      {values.map((value, index) => {
        const bound = paramName(value);
        return bound !== undefined ? (
          <ParamChip
            key={index}
            name={bound}
            label="value"
            onClear={() => removeAt(index)}
          />
        ) : (
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
        );
      })}
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
      <ParamBinder
        label="value"
        params={params}
        onBind={(param) => onChange([...values, { param }])}
      />
    </span>
  );
}

/**
 * ParamChip shows an operand bound to a profile parameter. The value is
 * substituted structurally at compile time, never interpolated into the DSL,
 * which is what the chip stands for.
 */
function ParamChip({
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

function ParamBinder({
  label,
  params,
  onBind,
}: {
  label: string;
  params: string[];
  onBind: (param: string) => void;
}) {
  if (!params.length) return null;
  return (
    <span className="w-24 shrink-0">
      <Select
        aria-label={`Bind ${label} to a parameter`}
        value=""
        placeholder="param"
        options={params.map((param) => ({ value: param, label: param }))}
        onChange={(event) => {
          if (event.target.value) onBind(event.target.value);
        }}
      />
    </span>
  );
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
    const checked = value === undefined ? schema.default === true : value === true;
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
