import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import type { BrowserDescriptor, Inspection } from "./connectionBrowserModel";
import {
  ConnectionQueryWorkspace,
  initialNavigatorTab,
  navigatorTabs,
  supportsQueryBuilder,
} from "./connectionQueryWorkspace";
import type { EsCompileRequest } from "./esQueryPreview";

// The compile request only leaves the browser once effects run, which server
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

const searchSchema = {
  type: "object" as const,
  properties: {
    search: {
      type: "object" as const,
      "x-clicky-component": "es-query-builder",
      "x-es-operators": [
        { op: "term", label: "term", arity: "single", fieldTypes: ["keyword"] },
      ],
    },
  },
};

const openSearch: BrowserDescriptor = {
  kind: "query",
  provider: "opensearch",
  language: "json",
  catalog: true,
  targetLabel: "Index",
  optionsSchema: searchSchema,
};

const sql: BrowserDescriptor = {
  kind: "query",
  provider: "sql",
  language: "sql",
  catalog: true,
};

describe("supportsQueryBuilder", () => {
  it("accepts a source whose options schema carries an operator catalog", () => {
    expect(supportsQueryBuilder(openSearch)).toBe(true);
  });

  it("rejects a source with no options schema", () => {
    expect(supportsQueryBuilder(sql)).toBe(false);
  });

  it("rejects an options schema that describes no structured search", () => {
    expect(
      supportsQueryBuilder({
        ...openSearch,
        optionsSchema: { type: "object", properties: { index: { type: "string" } } },
      }),
    ).toBe(false);
  });
});

describe("navigatorTabs", () => {
  it("offers the two authoring modes as tabs, form first", () => {
    expect(navigatorTabs({ descriptor: openSearch, builder: true })).toEqual([
      { id: "form", label: "Form" },
      { id: "json", label: "JSON" },
    ]);
  });

  it("keeps the catalog tab for a hierarchical source with a builder", () => {
    expect(
      navigatorTabs({
        descriptor: { ...openSearch, targetLabel: undefined },
        builder: true,
      }),
    ).toEqual([
      { id: "catalog", label: "Catalog" },
      { id: "form", label: "Form" },
      { id: "json", label: "JSON" },
    ]);
  });

  it("offers Catalog alone for a source with no structured search", () => {
    expect(navigatorTabs({ descriptor: sql, builder: false })).toEqual([
      { id: "catalog", label: "Catalog" },
    ]);
  });

  it("offers no tabs when the target picker is the whole navigator", () => {
    expect(navigatorTabs({ descriptor: openSearch, builder: false })).toEqual([]);
  });

  it("offers no navigator when there is neither a catalog nor a builder", () => {
    expect(
      navigatorTabs({ descriptor: { ...sql, catalog: false }, builder: false }),
    ).toEqual([]);
  });
});

describe("initialNavigatorTab", () => {
  it("starts in the form so filters are always built, not opted into", () => {
    expect(
      initialNavigatorTab({
        tabs: navigatorTabs({ descriptor: openSearch, builder: true }),
        search: undefined,
        query: "",
      }),
    ).toBe("form");
  });

  it("opens a stored specification in the form", () => {
    expect(
      initialNavigatorTab({
        tabs: navigatorTabs({ descriptor: openSearch, builder: true }),
        search: {},
        query: "",
      }),
    ).toBe("form");
  });

  it("opens a stored raw query in JSON rather than discarding it", () => {
    expect(
      initialNavigatorTab({
        tabs: navigatorTabs({ descriptor: openSearch, builder: true }),
        search: undefined,
        query: '{"query":{"term":{"level":"error"}}}',
      }),
    ).toBe("json");
  });

  it("treats the descriptor's own starter query as nothing to preserve", () => {
    expect(
      initialNavigatorTab({
        tabs: navigatorTabs({ descriptor: openSearch, builder: true }),
        search: undefined,
        query: '{"query":{"match_all":{}}}',
        defaultQuery: '{"query":{"match_all":{}}}',
      }),
    ).toBe("form");
  });

  it("falls back to the only tab a builder-less source has", () => {
    expect(
      initialNavigatorTab({
        tabs: navigatorTabs({ descriptor: sql, builder: false }),
        search: undefined,
        query: "SELECT 1",
      }),
    ).toBe("catalog");
  });
});

// The preview is compiled server-side, so an operand that interpolates
// {{.params.…}} resolves only if the host's parameter values travel with the
// specification. Without them the panel shows the compiler's refusal to guess.
describe("ConnectionQueryWorkspace compilation", () => {
  const inspection: Inspection = {
    nodes: [],
    databases: [],
    activeDatabase: "",
    sqlDatabase: "",
    targetKind: "index",
    loading: false,
    error: undefined,
  };

  const renderWorkspace = (extra: Record<string, unknown>) => {
    compileInputs.length = 0;
    renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <ConnectionQueryWorkspace
          id="test"
          title="Profile query"
          descriptor={openSearch}
          inspection={inspection}
          onDatabaseChange={() => {}}
          query=""
          onQueryChange={() => {}}
          options={{ index: "logs-*" }}
          onOptionsChange={() => {}}
          onCatalogSelect={() => {}}
          search={{
            query: {
              op: "term",
              field: "service.name",
              value: "{{.params.service}}",
            },
          }}
          onSearchChange={() => {}}
          compileBaseUrl="/api/v1/connection/abc/browser"
          execute={async () => ({ rows: [] })}
          {...extra}
        />
      </QueryClientProvider>,
    );
    return compileInputs;
  };

  it("compiles the specification against the host's parameter values", () => {
    const inputs = renderWorkspace({
      params: [{ name: "service" }, { name: "since", role: "time-from" }],
      paramValues: { service: "payments", since: "now-1h" },
      paramRoles: { since: "time-from" },
    });
    expect(inputs.length).toBeGreaterThan(0);
    expect(inputs[0]?.params).toEqual({ service: "payments", since: "now-1h" });
    expect(inputs[0]?.roles).toEqual({ since: "time-from" });
  });

  it("sends no parameter values when the host declares none", () => {
    const inputs = renderWorkspace({});
    expect(inputs.length).toBeGreaterThan(0);
    expect(inputs[0]?.params).toBeUndefined();
  });
});
