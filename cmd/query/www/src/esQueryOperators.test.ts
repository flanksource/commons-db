import { describe, expect, it } from "vitest";
import {
  changeConditionOperator,
  esBuilderVocabulary,
  fieldFamily,
  fieldWarning,
  operatorCatalogFromSchema,
  operatorsForField,
  qualifiersForOperator,
  sortableFields,
  type EsFieldMapping,
  type EsOperatorInfo,
} from "./esQueryOperators";

// A trimmed catalog in the exact shape x-es-operators carries. Using the real
// keys keeps the test honest about what the schema actually hands over.
const catalog: EsOperatorInfo[] = [
  { op: "term", label: "is", arity: "single", needsField: true,
    fieldTypes: ["keyword", "text", "date", "number", "boolean", "ip"] },
  { op: "terms", label: "is one of", arity: "multiple", needsField: true,
    fieldTypes: ["keyword", "text", "date", "number", "boolean", "ip"] },
  { op: "match", label: "matches", arity: "single", needsField: true,
    fieldTypes: ["text", "keyword"], analyzed: true },
  { op: "prefix", label: "starts with", arity: "single", needsField: true,
    fieldTypes: ["keyword", "text"] },
  { op: "range", label: "is between", arity: "range", needsField: true,
    fieldTypes: ["date", "number", "ip", "keyword"] },
  { op: "exists", label: "exists", arity: "none", needsField: true, fieldTypes: ["any"] },
  { op: "query_string", label: "query string", arity: "single", acceptsFields: true,
    fieldTypes: ["any"], analyzed: true },
  { op: "nested", label: "nested", arity: "group", fieldTypes: ["nested"], group: true },
  { op: "bool", label: "group", arity: "group", fieldTypes: ["any"], group: true },
];

const field = (mapping: Partial<EsFieldMapping> & { name: string }): EsFieldMapping => ({
  searchable: true,
  aggregatable: true,
  ...mapping,
});

describe("condition operator transitions", () => {
  const operator = (op: string) => {
    const match = catalog.find((entry) => entry.op === op);
    if (!match) throw new Error(`missing test operator ${op}`);
    return match;
  };

  it("moves a singular operand into values when changing to terms", () => {
    expect(
      changeConditionOperator(
        {
          op: "term",
          occur: "filter",
          field: "tag.scheme@id",
          value: "scheme-1",
          boost: 2,
        },
        operator("terms"),
      ),
    ).toEqual({
      op: "terms",
      occur: "filter",
      field: "tag.scheme@id",
      values: ["scheme-1"],
      boost: 2,
    });
  });

  it("promotes one list operand when changing to a singular operator", () => {
    expect(
      changeConditionOperator(
        { op: "terms", field: "level", values: ["error"] },
        operator("term"),
      ),
    ).toEqual({ op: "term", field: "level", value: "error" });
  });

  it("clears a list that cannot be converted to one operand", () => {
    expect(
      changeConditionOperator(
        { op: "terms", field: "level", values: ["warn", "error"] },
        operator("term"),
      ),
    ).toEqual({ op: "term", field: "level" });
  });

  it("keeps only operands accepted by the next arity", () => {
    expect(
      changeConditionOperator(
        {
          op: "range",
          field: "@timestamp",
          value: "stale",
          values: ["also-stale"],
          gte: "now-1h",
          lte: "now",
          conditions: [{ op: "exists", field: "trace.id" }],
        },
        operator("range"),
      ),
    ).toEqual({
      op: "range",
      field: "@timestamp",
      gte: "now-1h",
      lte: "now",
    });

    expect(
      changeConditionOperator(
        { op: "term", field: "level", value: "error", gte: "stale" },
        operator("exists"),
      ),
    ).toEqual({ op: "exists", field: "level" });
  });
});

describe("field families", () => {
  const cases: Array<[string, string]> = [
    ["keyword", "keyword"],
    ["constant_keyword", "keyword"],
    ["wildcard", "keyword"],
    ["text", "text"],
    ["match_only_text", "text"],
    ["search_as_you_type", "text"],
    ["date", "date"],
    ["date_nanos", "date"],
    ["long", "number"],
    ["integer", "number"],
    ["short", "number"],
    ["byte", "number"],
    ["double", "number"],
    ["float", "number"],
    ["half_float", "number"],
    ["scaled_float", "number"],
    ["unsigned_long", "number"],
    ["boolean", "boolean"],
    ["ip", "ip"],
    ["ip_range", "ip"],
    ["nested", "nested"],
    ["object", "object"],
    ["flattened", "object"],
    ["geo_point", "any"],
    ["", "any"],
  ];
  it.each(cases)("reduces %s to the %s family", (dataType, family) => {
    expect(fieldFamily(field({ name: "f", dataType }))).toBe(family);
  });

  it("takes the first mapped type when a field carries several", () => {
    expect(fieldFamily(field({ name: "f", types: ["keyword", "text"] }))).toBe("keyword");
  });
});

describe("operators offered per field", () => {
  const operators = (mapping: Partial<EsFieldMapping> & { name: string }) =>
    operatorsForField(catalog, field(mapping)).map((entry) => entry.op);

  it("leads a keyword field with an exact match", () => {
    expect(operators({ name: "level", dataType: "keyword" })).toEqual([
      "term", "terms", "prefix", "range", "exists", "query_string", "match",
    ]);
  });

  // A text field is analyzed, so term/prefix rarely do what an author expects.
  // They stay available, but behind the advanced flag.
  it("leads a text field with match and demotes the exact operators", () => {
    const offered = operatorsForField(catalog, field({ name: "message", dataType: "text" }));
    expect(offered.map((entry) => entry.op)).toEqual([
      "match", "exists", "query_string", "term", "terms", "prefix",
    ]);
    expect(offered.find((entry) => entry.op === "term")?.advanced).toBe(true);
    expect(offered.find((entry) => entry.op === "match")?.advanced).toBeFalsy();
  });

  it("leads a date field with a range", () => {
    expect(operators({ name: "@timestamp", dataType: "date" })[0]).toBe("range");
  });

  it("leads a number field with a range and omits the analyzed operators", () => {
    const offered = operators({ name: "duration", dataType: "long" });
    expect(offered[0]).toBe("range");
    expect(offered).not.toContain("match");
    expect(offered).not.toContain("prefix");
  });

  it("offers a nested field a nested group rather than a leaf", () => {
    expect(operators({ name: "spans", dataType: "nested" })).toEqual(["nested", "exists"]);
  });

  // An unsearchable field can only be tested for presence; offering anything
  // else would build a query the backend silently returns nothing for.
  it("offers an unsearchable field only exists", () => {
    expect(operators({ name: "blob", dataType: "keyword", searchable: false })).toEqual([
      "exists",
    ]);
  });

  it("falls back to the type-independent operators for an unknown type", () => {
    expect(operators({ name: "point", dataType: "geo_point" })).toEqual([
      "exists", "query_string",
    ]);
  });

  it("offers every operator when no field is selected yet", () => {
    expect(operatorsForField(catalog, undefined).map((entry) => entry.op)).toEqual(
      catalog.map((entry) => entry.op),
    );
  });
});

describe("field warnings", () => {
  it("names every mapped type of a conflicting field", () => {
    expect(
      fieldWarning(field({ name: "code", conflicting: true, types: ["keyword", "long"] })),
    ).toBe("code is mapped as keyword and long across indexes");
  });

  it("explains why an unsearchable field is limited", () => {
    expect(fieldWarning(field({ name: "blob", dataType: "binary", searchable: false }))).toBe(
      "blob is not searchable, so only exists applies",
    );
  });

  it("stays quiet about an ordinary field", () => {
    expect(fieldWarning(field({ name: "level", dataType: "keyword" }))).toBeUndefined();
  });
});

describe("sortable fields", () => {
  it("keeps the aggregatable fields and always offers _score and _doc", () => {
    expect(
      sortableFields([
        field({ name: "@timestamp", dataType: "date" }),
        field({ name: "message", dataType: "text", aggregatable: false }),
        field({ name: "level", dataType: "keyword" }),
      ]),
    ).toEqual(["@timestamp", "level", "_score", "_doc"]);
  });
});

describe("qualifiers", () => {
  const names = ["analyzer", "caseInsensitive", "scoreMode", "boost"];
  const restrictions = {
    analyzer: ["match", "match_phrase"],
    caseInsensitive: ["term", "prefix"],
    scoreMode: ["nested"],
  };

  it("offers only the qualifiers the operator emits", () => {
    expect(qualifiersForOperator({ names, restrictions, op: "match" })).toEqual([
      "analyzer",
      "boost",
    ]);
    expect(qualifiersForOperator({ names, restrictions, op: "nested" })).toEqual([
      "scoreMode",
      "boost",
    ]);
  });

  // boost is absent from the table because the compiler accepts it everywhere,
  // so an unlisted qualifier has to stay offered rather than disappear.
  it("keeps an unrestricted qualifier for every operator", () => {
    for (const op of ["match", "term", "nested", "range"]) {
      expect(qualifiersForOperator({ names, restrictions, op })).toContain("boost");
    }
  });

  it("offers everything when the schema carried no table", () => {
    expect(qualifiersForOperator({ names, op: "match" })).toEqual(names);
  });
});

describe("catalog extraction", () => {
  it("reads the catalog off the search property of the options schema", () => {
    expect(
      operatorCatalogFromSchema({
        properties: { search: { "x-es-operators": catalog } },
      }),
    ).toEqual(catalog);
  });

  it("returns nothing when the schema carries no catalog", () => {
    expect(operatorCatalogFromSchema({ properties: { index: { type: "string" } } })).toEqual([]);
    expect(operatorCatalogFromSchema(undefined)).toEqual([]);
  });
});

describe("builder vocabulary", () => {
  // The shape query/schema/search_spec.go emits, trimmed to what is read here.
  const schema = {
    properties: {
      search: {
        "x-es-operators": catalog,
        "x-es-occurs": ["filter", "must", "should", "must_not"],
        "x-es-qualifiers": { analyzer: ["match"], scoreMode: ["nested"] },
        properties: {
          query: {
            properties: {
              op: { type: "string" },
              occur: { type: "string" },
              field: { type: "string" },
              conditions: { type: "array" },
              analyzer: { type: "string", title: "Analyzer" },
              boost: { type: "number", title: "Boost" },
              scoreMode: { type: "string", title: "Score mode", enum: ["avg", "none"] },
            },
          },
          sort: {
            items: { properties: { order: { type: "string", enum: ["asc", "desc"] } } },
          },
        },
      },
    },
  };

  // A qualifier is whatever the condition schema carries beyond the structural
  // keys, so a new one in Go reaches the advanced editor untouched here.
  it("separates the advanced qualifiers from the structural condition keys", () => {
    const vocabulary = esBuilderVocabulary(schema);
    expect(vocabulary.qualifierNames).toEqual(["analyzer", "boost", "scoreMode"]);
    expect(vocabulary.qualifiers.scoreMode).toEqual({
      type: "string",
      title: "Score mode",
      enum: ["avg", "none"],
    });
  });

  it("carries the clauses, the restriction table and the sort orders", () => {
    const vocabulary = esBuilderVocabulary(schema);
    expect(vocabulary.occurs).toEqual(["filter", "must", "should", "must_not"]);
    expect(vocabulary.qualifierRestrictions).toEqual({
      analyzer: ["match"],
      scoreMode: ["nested"],
    });
    expect(vocabulary.sortOrders).toEqual(["asc", "desc"]);
    expect(vocabulary.catalog).toEqual(catalog);
  });

  it("stays empty for a schema that describes no search", () => {
    expect(esBuilderVocabulary({ properties: { index: { type: "string" } } })).toEqual({
      catalog: [],
      occurs: [],
      qualifierNames: [],
      qualifiers: {},
      sortOrders: [],
    });
  });
});
