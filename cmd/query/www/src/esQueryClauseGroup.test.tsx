import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import {
  EsQueryClauseGroup,
  groupOperatorOptions,
  isGroupOperator,
} from "./esQueryClauseGroup";
import type { EsQueryContext, EsQueryTreeActions } from "./esQueryConditionRow";
import type { EsCondition } from "./esQueryBuilderModel";
import type {
  EsBuilderVocabulary,
  EsFieldMapping,
  EsOperatorInfo,
} from "./esQueryOperators";

const catalog: EsOperatorInfo[] = [
  { op: "term", label: "is", arity: "single", needsField: true,
    fieldTypes: ["keyword", "text", "date", "number", "boolean", "ip"] },
  { op: "match", label: "matches", arity: "single", needsField: true,
    fieldTypes: ["text", "keyword"], analyzed: true },
  { op: "exists", label: "exists", arity: "none", needsField: true, fieldTypes: ["any"] },
  { op: "nested", label: "nested", arity: "group", fieldTypes: ["nested"], group: true },
  { op: "bool", label: "group", arity: "group", fieldTypes: ["any"], group: true },
];

const vocabulary: EsBuilderVocabulary = {
  catalog,
  occurs: ["filter", "must", "should", "must_not"],
  qualifierNames: ["boost"],
  qualifiers: { boost: { type: "number", title: "Boost" } },
  sortOrders: ["asc", "desc"],
};

const fields: EsFieldMapping[] = [
  { name: "level", dataType: "keyword", searchable: true, aggregatable: true },
  { name: "message", dataType: "text", searchable: true, aggregatable: false },
  { name: "spans", dataType: "nested", searchable: true, aggregatable: false },
];

const noopActions: EsQueryTreeActions = {
  update: () => undefined,
  insert: () => undefined,
  remove: () => undefined,
};

const render = (condition: EsCondition, root = true) => {
  const context: EsQueryContext = { fields, vocabulary, params: [] };
  return renderToStaticMarkup(
    <EsQueryClauseGroup
      condition={condition}
      path={[]}
      context={context}
      actions={noopActions}
      root={root}
    />,
  );
};

/** How many groups the rendered markup contains, of any kind. */
const groupCount = (html: string): number =>
  html.match(/data-es-group="/g)?.length ?? 0;

describe("group operators", () => {
  it("recognises only the operators that hold other conditions", () => {
    expect(isGroupOperator(catalog, "bool")).toBe(true);
    expect(isGroupOperator(catalog, "nested")).toBe(true);
    expect(isGroupOperator(catalog, "term")).toBe(false);
    expect(isGroupOperator(catalog, "unheard_of")).toBe(false);
  });

  it("offers exactly the catalog's group operators as group kinds", () => {
    expect(groupOperatorOptions(catalog)).toEqual([
      { value: "nested", label: "nested" },
      { value: "bool", label: "group" },
    ]);
  });
});

describe("the root group", () => {
  // The root is the whole query, so it has no clause to contribute to and
  // nothing to be removed from.
  it("offers neither a clause nor a remove control", () => {
    const html = render({ op: "bool", conditions: [] });
    expect(html).not.toContain('aria-label="Clause"');
    expect(html).not.toContain('aria-label="Remove group"');
    expect(html).not.toContain('aria-label="Group type"');
  });

  it("says plainly that an empty query matches everything", () => {
    expect(render({ op: "bool", conditions: [] })).toContain(
      "No conditions — every document matches.",
    );
  });
});

describe("children", () => {
  it("renders a leaf as a condition row and a group as a nested group", () => {
    const html = render({
      op: "bool",
      conditions: [
        { op: "term", field: "level", value: "error" },
        { op: "bool", conditions: [{ op: "exists", field: "message" }] },
      ],
    });
    expect(groupCount(html)).toBe(2);
    expect(html).toContain('aria-label="Operator"');
    expect(html).toContain('aria-label="Remove condition"');
    expect(html).toContain('aria-label="Remove group"');
  });

  it("nests a group inside a group inside a group", () => {
    const html = render({
      op: "bool",
      conditions: [
        { op: "bool", conditions: [{ op: "bool", conditions: [] }] },
      ],
    });
    expect(groupCount(html)).toBe(3);
  });

  it("gives a non-root group both a clause and a group kind", () => {
    const html = render({ op: "bool", occur: "should", conditions: [] }, false);
    expect(html).toContain('aria-label="Clause"');
    expect(html).toContain('aria-label="Group type"');
    expect(html).toContain('aria-label="Remove group"');
  });
});

describe("minimum should match", () => {
  // Without a should child the setting has nothing to act on, so offering it
  // would only invite a value the compiler ignores.
  it("stays hidden until a child contributes to the should clause", () => {
    expect(
      render({ op: "bool", conditions: [{ op: "term", field: "level" }] }),
    ).not.toContain('aria-label="Minimum should match"');
  });

  it("appears once a child is an or clause", () => {
    expect(
      render({
        op: "bool",
        conditions: [{ op: "term", field: "level", occur: "should" }],
      }),
    ).toContain('aria-label="Minimum should match"');
  });
});

describe("nested groups", () => {
  it("asks for the path a nested group descends into", () => {
    const html = render({ op: "nested", path: "spans", conditions: [] }, false);
    expect(html).toContain('aria-label="Nested path"');
    expect(html).toContain('value="spans"');
  });

  it("does not ask a plain bool group for a path", () => {
    expect(render({ op: "bool", conditions: [] }, false)).not.toContain(
      'aria-label="Nested path"',
    );
  });
});
