import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { esParamOptionsFormExtensions } from "./esParamOptions";
import type { ParamDraft } from "./profileWizardModel";

const [post] = esParamOptionsFormExtensions.post;
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

  it("passes a parameter that is not an enum through untouched", () => {
    expect(post(control({ name: "service", type: "string" }), nodes)).toBe(nodes);
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

  it("offers no picker for a parameter no condition binds", () => {
    expect(renderExtension({ name: "unbound", type: "enum" })).toBe("fields");
  });

  it("offers no picker without a saved connection to ask", () => {
    expect(
      renderExtension({ name: "service", type: "enum" }, {
        provider: { options: rootValue.provider.options },
      }),
    ).toBe("fields");
  });
});
