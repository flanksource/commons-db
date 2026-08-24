import { describe, expect, it } from "vitest";
import {
  conditionOperandPatch,
  multipleConditionValues,
} from "./esQueryOperandModel";
import { changeConditionOperator, type EsOperatorInfo } from "./esQueryOperators";

describe("multipleConditionValues", () => {
  it("uses canonical values ahead of a stale singular operand", () => {
    expect(
      multipleConditionValues({
        op: "terms",
        value: "stale",
        values: ["scheme-1", "scheme-2"],
      }),
    ).toEqual(["scheme-1", "scheme-2"]);
  });

  it("shows a valid singular terms operand as one selected value", () => {
    expect(multipleConditionValues({ op: "terms", value: "scheme-1" })).toEqual([
      "scheme-1",
    ]);
    expect(multipleConditionValues({ op: "terms" })).toEqual([]);
  });
});

describe("conditionOperandPatch", () => {
  it("never leaves value beside values after the term to terms editing flow", () => {
    const terms: EsOperatorInfo = {
      op: "terms",
      label: "is one of",
      arity: "multiple",
      needsField: true,
      fieldTypes: ["keyword"],
    };
    const changed = changeConditionOperator(
      { op: "term", field: "tag.scheme@id", value: "scheme-1" },
      terms,
    );

    expect({
      ...changed,
      ...conditionOperandPatch({
        arity: "multiple",
        values: [...multipleConditionValues(changed), "scheme-2"],
      }),
    }).toEqual({
      op: "terms",
      field: "tag.scheme@id",
      value: undefined,
      values: ["scheme-1", "scheme-2"],
      gt: undefined,
      gte: undefined,
      lt: undefined,
      lte: undefined,
      conditions: undefined,
    });
  });

  it("makes a multiple edit the only operand representation", () => {
    expect(
      conditionOperandPatch({
        arity: "multiple",
        values: ["scheme-1", "scheme-2"],
      }),
    ).toEqual({
      value: undefined,
      values: ["scheme-1", "scheme-2"],
      gt: undefined,
      gte: undefined,
      lt: undefined,
      lte: undefined,
      conditions: undefined,
    });
  });

  it("clears list and range operands for a singular edit", () => {
    expect(conditionOperandPatch({ arity: "single", value: "error" })).toEqual({
      value: "error",
      values: undefined,
      gt: undefined,
      gte: undefined,
      lt: undefined,
      lte: undefined,
      conditions: undefined,
    });
  });

  it("keeps sibling bounds while clearing scalar and list operands", () => {
    expect(
      conditionOperandPatch({ arity: "range", bound: "gte", value: "now-1h" }),
    ).toEqual({
      value: undefined,
      values: undefined,
      conditions: undefined,
      gte: "now-1h",
    });
  });
});
