import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import {
  EsQueryBuilder,
  esQueryBuilderFormExtensions,
  esQueryFields,
  paramNames,
  paramRoles,
} from "./esQueryBuilder";
import type { EsSearch } from "./esQueryBuilderModel";
import type {
  EsBuilderVocabulary,
  EsFieldMapping,
  EsOperatorInfo,
} from "./esQueryOperators";

const operator = (
  op: string,
  fieldTypes: string[],
  extra: Partial<EsOperatorInfo> = {},
): EsOperatorInfo => ({
  op,
  label: op,
  arity: "single",
  needsField: true,
  fieldTypes,
  ...extra,
});

const vocabulary: EsBuilderVocabulary = {
  catalog: [
    operator("term", ["keyword", "number", "boolean", "ip", "date"]),
    operator("range", ["date", "number", "ip"], { arity: "range" }),
    operator("exists", ["any"], { arity: "none" }),
    operator("bool", [], {
      op: "bool",
      label: "group",
      arity: "group",
      group: true,
      needsField: false,
    }),
  ],
  occurs: ["filter", "must", "should", "must_not"],
  qualifierNames: ["boost"],
  qualifiers: { boost: { type: "number", title: "Boost" } },
  sortOrders: ["asc", "desc"],
};

const fields: EsFieldMapping[] = [
  { name: "@timestamp", dataType: "date", aggregatable: true },
  { name: "service.name", dataType: "keyword", aggregatable: true },
  { name: "message", dataType: "text", aggregatable: false },
];

const render = (search: EsSearch, extra: Record<string, unknown> = {}) =>
  renderToStaticMarkup(
    <EsQueryBuilder
      search={search}
      onChange={() => {}}
      fields={fields}
      vocabulary={vocabulary}
      {...extra}
    />,
  );

describe("EsQueryBuilder", () => {
  it("renders the root group without a clause or remove control", () => {
    const html = render({});
    expect(html).toContain('data-es-group="bool"');
    expect(html).not.toContain('aria-label="Clause"');
    expect(html).not.toContain('aria-label="Remove group"');
  });

  it("renders one condition row per child of the root group", () => {
    const html = render({
      query: {
        op: "bool",
        conditions: [
          { op: "term", field: "service.name" },
          { op: "exists", field: "message" },
        ],
      },
    });
    expect(html.match(/aria-label="Operator"/g)).toHaveLength(2);
  });

  it("offers only date fields as the time field", () => {
    const html = render({ timeField: "@timestamp" });
    const at = html.indexOf('aria-label="Time field"');
    expect(at).toBeGreaterThan(-1);
    // Combobox keeps its options closed, so the selected value is what SSR shows.
    expect(html.slice(at, html.indexOf(">", at))).toContain('value="@timestamp"');
  });

  it("renders the sort and output editors", () => {
    const html = render({ sort: [{ field: "@timestamp", order: "desc" }] });
    expect(html).toContain('aria-label="Sort field"');
    expect(html).toContain('aria-label="Size"');
    expect(html).toContain('aria-label="From"');
  });

  it("shows the compiled preview only when a compilation is supplied", () => {
    expect(render({})).not.toContain("Compiled DSL");
    expect(
      render({}, {
        compilation: { query: '{"query":{"match_all":{}}}', loading: false },
      }),
    ).toContain("Compiled DSL");
  });

  it("offers the raw-DSL escape hatch only when the host accepts one", () => {
    expect(render({})).not.toContain("Edit raw DSL");
    expect(render({}, { onEditRawDsl: () => {} })).toContain("Edit raw DSL");
  });
});

describe("esQueryBuilderFormExtensions", () => {
  const [post] = esQueryBuilderFormExtensions.post;
  const nodes = { label: "label", value: "input" };
  const field = (component: string | undefined) => ({
    key: "search",
    kind: "object" as const,
    label: "Search",
    required: false,
    schema: component ? { "x-clicky-component": component } : {},
    value: {},
    onChange: () => {},
  });

  it("passes the rendered nodes through for any other component", () => {
    expect(post(field("profile-query-builder"), nodes)).toBe(nodes);
    expect(post(field(undefined), nodes)).toBe(nodes);
  });

  it("replaces the value node for the es-query-builder component", () => {
    const replaced = post(field("es-query-builder"), nodes);
    expect(replaced.label).toBe(nodes.label);
    expect(replaced.value).not.toBe(nodes.value);
  });
});

describe("param plumbing", () => {
  it("lists declared parameter names in order, skipping unnamed drafts", () => {
    expect(
      paramNames([{ name: "env" }, {}, { name: "" }, { name: "since" }]),
    ).toEqual(["env", "since"]);
  });

  it("maps only the parameters that carry a role", () => {
    expect(
      paramRoles([
        { name: "since", role: "time-from" },
        { name: "env" },
        { name: "rows", role: "limit" },
      ]),
    ).toEqual({ since: "time-from", rows: "limit" });
  });

  it("returns empty plumbing when the profile declares no parameters", () => {
    expect(paramNames(undefined)).toEqual([]);
    expect(paramRoles(undefined)).toEqual({});
  });
});

describe("esQueryFields", () => {
  it("reads the mappings off an OpenSearch field completion", () => {
    expect(
      esQueryFields({ kind: "json-fields", vocabulary: "opensearch", fields }),
    ).toEqual(fields);
  });

  it("builds against free text for a completion of any other kind", () => {
    expect(esQueryFields({ kind: "sql", dialect: "postgresql" })).toEqual([]);
    expect(esQueryFields(undefined)).toEqual([]);
  });
});
