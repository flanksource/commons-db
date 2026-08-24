import type { EsCondition, EsValue } from "./esQueryBuilderModel";

type ConditionOperandEdit =
  | { arity: "single"; value: EsValue }
  | { arity: "multiple"; values: EsValue[] }
  | {
      arity: "range";
      bound: "gt" | "gte" | "lt" | "lte";
      value: EsValue;
    };

export function multipleConditionValues(condition: EsCondition): EsValue[] {
  if (condition.values?.length) return condition.values;
  return condition.value === undefined ? [] : [condition.value];
}

export function conditionOperandPatch(
  edit: ConditionOperandEdit,
): Partial<EsCondition> {
  if (edit.arity === "range") {
    return {
      value: undefined,
      values: undefined,
      conditions: undefined,
      [edit.bound]: edit.value,
    };
  }
  const cleared = {
    gt: undefined,
    gte: undefined,
    lt: undefined,
    lte: undefined,
    conditions: undefined,
  };
  return edit.arity === "multiple"
    ? { value: undefined, values: edit.values, ...cleared }
    : { value: edit.value, values: undefined, ...cleared };
}
