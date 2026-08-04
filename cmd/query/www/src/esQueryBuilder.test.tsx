import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import {
  defaultParamValues,
  EsQueryBuilder,
  esQueryBuilderFormExtensions,
  esQueryFields,
  paramNames,
  paramRoles,
} from "./esQueryBuilder";
import { compileRequestBody, type EsCompileRequest } from "./esQueryPreview";
import type { EsSearch } from "./esQueryBuilderModel";

// useCompiledSearch only issues its request once effects run, which server
// rendering never does — so the wiring is asserted on what the hook was handed.
const compileInputs = vi.hoisted(() => [] as EsCompileRequest[]);
vi.mock("./esQueryPreview", async (importOriginal) => {
  const original = await importOriginal<typeof import("./esQueryPreview")>();
  return {
    ...original,
    useCompiledSearch: (input: EsCompileRequest) => {
      compileInputs.push(input);
      return original.useCompiledSearch(input);
    },
  };
});
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

  // Raw DSL is the other tab, not a button inside this one: the builder edits a
  // specification and knows nothing about where the host stores the alternative.
  it("keeps the mode switch out of the builder", () => {
    expect(render({})).not.toContain("Edit raw DSL");
  });
});

describe("EsQueryBuilder value lookups", () => {
  // An analyzed text field has no doc values of its own, so its keyword sibling
  // is what a value list can be aggregated from.
  const lookupFields: EsFieldMapping[] = [
    ...fields,
    { name: "message.keyword", dataType: "keyword", aggregatable: true },
  ];

  const twoConditions: EsSearch = {
    query: {
      op: "bool",
      conditions: [
        { op: "term", field: "service.name", value: "pay" },
        { op: "term", field: "message", value: "timeout" },
      ],
    },
  };

  const renderWithLookup = (search: EsSearch) => {
    const asked: { field: string; search?: EsSearch }[] = [];
    const values = (request: { field: string; search?: EsSearch }) => {
      asked.push(request);
      return { key: request.field, fetch: async () => ({ values: [], total: 0, scoped: true }) };
    };
    const html = renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <EsQueryBuilder
          search={search}
          onChange={() => {}}
          fields={lookupFields}
          vocabulary={vocabulary}
          values={values}
        />
      </QueryClientProvider>,
    );
    return { asked, html };
  };

  // The row being edited holds the value the list is meant to complete, so
  // scoping by it would filter the suggestions down to what was already typed.
  it("scopes a row's lookup to the query without that row", () => {
    const { asked } = renderWithLookup(twoConditions);
    expect(asked.map((entry) => entry.field)).toEqual([
      "service.name",
      "message.keyword",
    ]);
    expect(asked[0]?.search?.query?.conditions).toEqual([
      { op: "term", field: "message", value: "timeout" },
    ]);
    expect(asked[1]?.search?.query?.conditions).toEqual([
      { op: "term", field: "service.name", value: "pay" },
    ]);
  });

  it("asks for no lookup on a field that cannot be aggregated", () => {
    const { asked } = renderWithLookup({
      query: { op: "bool", conditions: [{ op: "term", field: "@timestamp" }] },
    });
    expect(asked).toEqual([]);
  });

  it("picks the operand from the field's values when a lookup exists", () => {
    const { html } = renderWithLookup({
      query: { op: "bool", conditions: [{ op: "term", field: "service.name" }] },
    });
    expect(html).toMatch(/role="combobox"[^>]*aria-label="Value"/);
  });

  it("leaves the operand a plain input when the host has no connection", () => {
    const html = render({
      query: { op: "bool", conditions: [{ op: "term", field: "service.name" }] },
    });
    expect(html).toContain('aria-label="Value"');
    expect(html).not.toMatch(/role="combobox"[^>]*aria-label="Value"/);
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

  it("takes the default of every named parameter that declares one", () => {
    expect(
      defaultParamValues([
        { name: "country", default: "kenya" },
        { name: "env" },
        { name: "rows", default: 500 },
        { default: "orphan" },
      ]),
    ).toEqual({ country: "kenya", rows: 500 });
  });

  it("returns empty plumbing when the profile declares no parameters", () => {
    expect(paramNames(undefined)).toEqual([]);
    expect(paramRoles(undefined)).toEqual({});
    expect(defaultParamValues(undefined)).toEqual({});
  });
});

// The preview compiles server-side against the parameter values a run would
// start with, so an operand that binds {param:…} or interpolates {{.params.…}}
// resolves in the panel instead of showing template text.
describe("EsQueryBuilderField compilation", () => {
  const [post] = esQueryBuilderFormExtensions.post;

  it("sends the declared parameter defaults and roles to /compile", () => {
    compileInputs.length = 0;
    renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        {
          post(
            {
              key: "search",
              kind: "object" as const,
              label: "Search",
              required: false,
              schema: { "x-clicky-component": "es-query-builder" },
              value: {
                query: {
                  op: "term",
                  field: "process.serviceName",
                  value: "{{.params.country}}-api",
                },
              },
              onChange: () => {},
            },
            { label: "label", value: "input" },
            {
              rootValue: {
                provider: {
                  connection: "connection://11111111-2222-3333-4444-555555555555",
                  options: { index: "jaeger-span*" },
                },
                params: [
                  { name: "country", default: "kenya" },
                  { name: "since", role: "time-from", default: "now-1h" },
                  { name: "env" },
                ],
              },
            },
          ).value as React.ReactNode
        }
      </QueryClientProvider>,
    );

    expect(compileInputs).toHaveLength(1);
    expect(compileInputs[0]?.params).toEqual({
      country: "kenya",
      since: "now-1h",
    });
    expect(compileInputs[0]?.roles).toEqual({ since: "time-from" });
    expect(JSON.parse(compileRequestBody(compileInputs[0]!)).params).toEqual({
      country: "kenya",
      since: "now-1h",
    });
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
