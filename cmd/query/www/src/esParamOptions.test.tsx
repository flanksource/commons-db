import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { esParamOptionsFormExtensions } from "./esParamOptions";
import type { ParamDraft } from "@flanksource/clicky-ui/profiles";

const [post] = esParamOptionsFormExtensions.post;
const [pre] = esParamOptionsFormExtensions.pre;
const nodes = { label: "label", value: "fields" };

const control = (param: ParamDraft, component = "es-param") => ({
  key: "params",
  kind: "object" as const,
  label: "Parameter",
  required: false,
  schema: { "x-clicky-component": component },
  value: param,
  onChange: () => {},
});

// The specification is what ties a parameter to a field: the parameter itself
// only carries a name.
const rootValue = {
  params: [{ name: "service", type: "list" } satisfies ParamDraft],
  provider: {
    connection: "connection://8f1c0b9e-0000-4000-8000-000000000000",
    options: {
      index: "logs-*",
      search: {
        query: {
          op: "terms",
          field: "service.name",
          values: [{ param: "service" }],
        },
      },
    },
  },
};

const connectionID = "8f1c0b9e-0000-4000-8000-000000000000";
const targets = {
  kind: "opensearch",
  targets: [{ name: "logs-*", kind: "pattern" }],
};
const mappings = {
  kind: "opensearch",
  selected: {
    target: { name: "logs-*", kind: "pattern" },
    fields: [{ name: "service.name", dataType: "keyword", aggregatable: true }],
  },
};

const renderExtension = (param: ParamDraft, root: unknown = rootValue) => {
  const client = new QueryClient();
  // The inspection is fetched, so seed it rather than reaching the network.
  client.setQueryData(["es-param-options", connectionID], targets);
  client.setQueryData(
    ["es-param-options", connectionID, "pattern", "logs-*"],
    mappings,
  );
  const result = post(control(param), nodes, {
    rootValue: root as Record<string, unknown>,
  });
  return renderToStaticMarkup(
    <QueryClientProvider client={client}>{result.value}</QueryClientProvider>,
  );
};

describe("esParamOptionsFormExtensions", () => {
  it("passes the rendered nodes through for any other component", () => {
    expect(post(control({ name: "service", type: "enum" }, "es-query-builder"), nodes)).toBe(
      nodes,
    );
  });

  it("adds the mapping editor to a scalar parameter", () => {
    const html = renderExtension({ name: "service", type: "string" });
    expect(html).toMatch(
      /role="combobox"[^>]*aria-label="Map service to a field"/,
    );
    expect(html).not.toContain("Load from file");
  });

  it("keeps the parameter's own fields when it offers options", () => {
    const result = post(control({ name: "service", type: "enum" }), nodes, {
      rootValue: rootValue as unknown as Record<string, unknown>,
    });
    expect(result.label).toBe(nodes.label);
    expect(result.value).not.toBe(nodes.value);
  });

  it("offers the values of the field the parameter is bound to", () => {
    const html = renderExtension({ name: "service", type: "enum" });
    expect(html).toContain("Options from service.name");
    expect(html).toMatch(/role="combobox"[^>]*aria-label="Options"/);
  });

  it("also offers the option list for a list parameter, not only an enum", () => {
    const html = renderExtension({ name: "service", type: "list" });
    expect(html).toContain("Options from service.name");
  });

  // Reading options off the index needs something to ask; loading them from a
  // file does not, so it stays available either way.
  it("offers no index picker for a parameter no condition binds", () => {
    const html = renderExtension({ name: "unbound", type: "enum" });
    expect(html).not.toContain("Options from");
    expect(html).toContain("Load from file");
  });

  it("offers no index picker without a saved connection to ask", () => {
    const html = renderExtension({ name: "service", type: "enum" }, {
      provider: { options: rootValue.provider.options },
    });
    expect(html).not.toContain("Options from");
    expect(html).toContain("Load from file");
  });

  it("asks the author to switch to Form before mapping a raw query", () => {
    const html = renderExtension(
      { name: "service", type: "string" },
      {
        params: [{ name: "service", type: "string" }],
        provider: { options: { index: "logs-*" } },
      },
    );
    expect(html).toContain("Switch Source &amp; Query to Form");
  });

  it("renames condition references through one root update", () => {
    const onRootChange = vi.fn();
    const field = {
      ...control(rootValue.params[0]),
      kind: "array" as const,
      schema: { "x-clicky-component": "es-params" },
      value: rootValue.params,
    };
    const extended = pre(field, {
      key: "params",
      prop: field.schema,
      value: field.value,
      rootValue: rootValue as unknown as Record<string, unknown>,
      onRootChange,
    });

    extended?.onChange([{ name: "application", type: "list" }]);

    expect(onRootChange).toHaveBeenCalledWith(
      expect.objectContaining({
        params: [{ name: "application", type: "list", field: "service.name" }],
        provider: expect.objectContaining({
          options: expect.objectContaining({
            search: expect.objectContaining({
              query: expect.objectContaining({
                values: [{ param: "application" }],
              }),
            }),
          }),
        }),
      }),
    );
  });

  it("rejects a structured parameter edit without an atomic root updater", () => {
    const field = {
      ...control(rootValue.params[0]),
      kind: "array" as const,
      schema: { "x-clicky-component": "es-params" },
      value: rootValue.params,
    };
    const extended = pre(field, {
      key: "params",
      prop: field.schema,
      value: field.value,
      rootValue: rootValue as unknown as Record<string, unknown>,
    });

    expect(() =>
      extended?.onChange([{ name: "application", type: "list" }]),
    ).toThrow("structured parameter edits require an atomic root form update");
  });

  it("hides the raw field input owned by the mapping extension", () => {
    const field = {
      ...control({ name: "service", type: "list" }),
      schema: { "x-clicky-component": "es-param-field" },
    };
    expect(
      pre(field, {
        key: "field",
        prop: field.schema,
        value: "service.name",
      }),
    ).toBeNull();
  });
});
