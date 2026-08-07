import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { jsonPathFormExtensions } from "./jsonPathPicker";

const [post] = jsonPathFormExtensions.post;
const nodes = { label: "label", value: "plain input" };

const control = (component: string, value = "") => ({
  key: "jsonpath",
  kind: "string" as const,
  label: "JSONPath",
  required: false,
  schema: { "x-clicky-component": component },
  value,
  onChange: () => {},
});

const profile = {
  profile: "orders",
  provider: { type: "sql", options: { url: "postgres://localhost/orders" } },
  query: "SELECT payload FROM orders",
};

const markup = (node: React.ReactNode) =>
  renderToStaticMarkup(
    <QueryClientProvider client={new QueryClient()}>{node}</QueryClientProvider>,
  );

describe("jsonPathFormExtensions", () => {
  it("leaves a field without the jsonpath-picker hint untouched", () => {
    expect(post(control("es-param-field"), nodes, { rootValue: profile })).toBe(nodes);
    expect(post(control(""), nodes, { rootValue: profile })).toBe(nodes);
  });

  it("replaces the value node with a JSONPath input carrying the current path", () => {
    const replaced = post(control("jsonpath-picker", "$.payload.user.email"), nodes, {
      rootValue: profile,
    });

    expect(replaced.label).toBe(nodes.label);
    expect(markup(replaced.value)).toContain('value="$.payload.user.email"');
  });

  it("leaves the browse button disabled until a sample has resolved", () => {
    // The sample is fetched, so the first paint has nothing to browse yet and the
    // field stays usable as a plain text input rather than opening an empty tree.
    const html = markup(post(control("jsonpath-picker"), nodes, { rootValue: profile }).value);

    expect(html).toContain("disabled");
  });
});
