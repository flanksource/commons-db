import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import {
  EsQueryConditionRow,
  occurOptions,
  type EsQueryContext,
  type EsQueryTreeActions,
} from "./esQueryConditionRow";
import type { EsCondition } from "./esQueryBuilderModel";
import type {
  EsBuilderVocabulary,
  EsFieldMapping,
  EsOperatorInfo,
} from "./esQueryOperators";

const catalog: EsOperatorInfo[] = [
  { op: "term", label: "is", arity: "single", needsField: true,
    fieldTypes: ["keyword", "text", "date", "number", "boolean", "ip"] },
  { op: "terms", label: "is one of", arity: "multiple", needsField: true,
    fieldTypes: ["keyword", "text", "date", "number", "boolean", "ip"] },
  { op: "match", label: "matches", arity: "single", needsField: true,
    fieldTypes: ["text", "keyword"], analyzed: true },
  { op: "range", label: "is between", arity: "range", needsField: true,
    fieldTypes: ["date", "number", "ip", "keyword"] },
  { op: "exists", label: "exists", arity: "none", needsField: true, fieldTypes: ["any"] },
];

const vocabulary: EsBuilderVocabulary = {
  catalog,
  occurs: ["filter", "must", "should", "must_not"],
  qualifierNames: ["boost", "caseInsensitive", "scoreMode"],
  qualifierRestrictions: { caseInsensitive: ["term"], scoreMode: ["nested"] },
  qualifiers: {
    boost: { type: "number", title: "Boost" },
    caseInsensitive: { type: "boolean", title: "Case insensitive" },
    scoreMode: { type: "string", title: "Score mode", enum: ["avg", "none"] },
  },
  sortOrders: ["asc", "desc"],
};

const fields: EsFieldMapping[] = [
  { name: "level", dataType: "keyword", searchable: true, aggregatable: true },
  { name: "message", dataType: "text", searchable: true, aggregatable: false },
  { name: "@timestamp", dataType: "date", searchable: true, aggregatable: true },
  {
    name: "code",
    types: ["keyword", "long"],
    conflicting: true,
    searchable: true,
    aggregatable: true,
  },
];

const noopActions: EsQueryTreeActions = {
  update: () => undefined,
  insert: () => undefined,
  remove: () => undefined,
};

const render = (condition: EsCondition, params: string[] = []) => {
  const context: EsQueryContext = { fields, vocabulary, params };
  return renderToStaticMarkup(
    <EsQueryConditionRow
      condition={condition}
      path={[0]}
      context={context}
      actions={noopActions}
    />,
  );
};

/** The markup of one labelled <select>, so the clause and operator controls do
 * not read each other's options. */
const selectMarkup = (html: string, ariaLabel: string): string => {
  const start = html.indexOf(`aria-label="${ariaLabel}"`);
  expect(start, `no select labelled ${ariaLabel}`).toBeGreaterThan(-1);
  return html.slice(start, html.indexOf("</select>", start));
};

/** The operator values the rendered <select> offers, in document order. */
const offeredOperators = (html: string): string[] =>
  Array.from(
    selectMarkup(html, "Operator").matchAll(/<option value="([^"]+)"/g),
  ).map((match) => match[1]);

describe("clause labels", () => {
  // filter and must both narrow the hits; only must contributes to the score,
  // which the raw bool clause names do not say out loud.
  it("names each bool clause the way an author reads it", () => {
    expect(occurOptions(["filter", "must", "should", "must_not"])).toEqual([
      { value: "filter", label: "AND" },
      { value: "must", label: "AND (scored)" },
      { value: "should", label: "OR" },
      { value: "must_not", label: "NOT" },
    ]);
  });

  it("passes an unknown clause through rather than dropping it", () => {
    expect(occurOptions(["filter_v2"])).toEqual([
      { value: "filter_v2", label: "filter_v2" },
    ]);
  });
});

describe("operators offered per field", () => {
  it("leads a keyword field with the exact operators", () => {
    expect(offeredOperators(render({ op: "term", field: "level" }))).toEqual([
      "term",
      "terms",
      "range",
      "exists",
      "match",
    ]);
  });

  // A text field is analyzed, so term matches tokens rather than the stored
  // text. It stays offered, but behind the Advanced divider.
  it("leads a text field with match and demotes the exact operators", () => {
    const html = render({ op: "match", field: "message" });
    expect(offeredOperators(html)).toEqual(["match", "exists", "term", "terms"]);
    expect(html).toContain('<optgroup label="Advanced">');
    expect(html.indexOf('value="match"')).toBeLessThan(
      html.indexOf('<optgroup label="Advanced">'),
    );
  });

  // Changing the field must never blank the operator control, so an operator
  // the new field does not suit is still offered — under Advanced.
  it("keeps the current operator when the field does not suit it", () => {
    const html = render({ op: "match", field: "@timestamp" });
    expect(offeredOperators(html)).toContain("match");
    expect(html).toContain('<optgroup label="Advanced">');
  });
});

describe("operand editors", () => {
  it("renders a chip input for a multi-value operator", () => {
    const html = render({ op: "terms", field: "level", values: ["warn", "error"] });
    expect(html).toContain('aria-label="Add value"');
    expect(html).toContain("es-value-chip");
    expect(html).toContain("warn");
    expect(html).toContain("error");
  });

  it("renders both bounds of a range with date math presets", () => {
    const html = render({ op: "range", field: "@timestamp", gte: "now-1h" });
    expect(html).toContain('aria-label="From"');
    expect(html).toContain('aria-label="To"');
    expect(html).toContain('value="now-15m"');
  });

  it("offers no date math on a field that is not a date", () => {
    expect(render({ op: "range", field: "level" })).not.toContain('value="now-15m"');
  });

  it("renders no operand at all for a presence test", () => {
    const html = render({ op: "exists", field: "level" });
    expect(html).not.toContain('aria-label="Value"');
  });

  // A bound operand is substituted structurally at compile time. Showing the
  // parameter rather than an editable box is what keeps that visible.
  it("renders a parameter chip in place of a bound value", () => {
    const html = render({ op: "term", field: "level", value: { param: "severity" } });
    expect(html).toContain("es-param-chip");
    expect(html).toContain("severity");
    expect(html).not.toContain('aria-label="Value"');
  });

  it("offers a parameter binder only where the profile declares parameters", () => {
    expect(render({ op: "term", field: "level" }, ["severity"])).toContain(
      'aria-label="Bind Value to a parameter"',
    );
    expect(render({ op: "term", field: "level" })).not.toContain(
      "to a parameter",
    );
  });
});

describe("field warnings", () => {
  it("warns inline about a field mapped differently across indexes", () => {
    const html = render({ op: "term", field: "code" });
    expect(html).toContain('role="alert"');
    expect(html).toContain("code is mapped as keyword and long across indexes");
  });

  it("stays quiet about an ordinary field", () => {
    expect(render({ op: "term", field: "level" })).not.toContain('role="alert"');
  });
});

describe("advanced qualifiers", () => {
  it("offers only the qualifiers the operator emits", () => {
    const html = render({ op: "term", field: "level" });
    expect(html).toContain("Case insensitive");
    expect(html).toContain('aria-label="Boost"');
    expect(html).not.toContain("Score mode");
  });

  it("drops a restricted qualifier for an operator that does not emit it", () => {
    const html = render({ op: "match", field: "message" });
    expect(html).not.toContain("Case insensitive");
    expect(html).toContain('aria-label="Boost"');
  });
});
