import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { applyPickedSource, jsonPathFormExtensions, pickedSource } from "./jsonPathPicker";

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

// A path picked inside a column holding JSON as text restarts at `$`, so it only
// resolves once that column is named as the column's source. Picking it is the
// whole edit; the author is never asked to pair the two by hand.
describe("applyPickedSource", () => {
  const draft = {
    profile: "orders",
    columns: [
      { name: "id", jsonpath: "$.id" },
      { name: "email", jsonpath: "$.user.email", source: "payload" },
    ],
  };

  it("names the encoded column as the source of the column that was picked in", () => {
    expect(applyPickedSource(draft, "/columns/0/jsonpath", "payload")).toEqual({
      profile: "orders",
      columns: [
        { name: "id", jsonpath: "$.id", source: "payload" },
        { name: "email", jsonpath: "$.user.email", source: "payload" },
      ],
    });
  });

  it("clears a stale source when the new path is picked outside any encoded column", () => {
    const next = applyPickedSource(draft, "/columns/1/jsonpath", undefined);

    expect(next.columns).toEqual([
      { name: "id", jsonpath: "$.id" },
      { name: "email", jsonpath: "$.user.email" },
    ]);
  });

  it("leaves every sibling column untouched", () => {
    const next = applyPickedSource(draft, "/columns/1/jsonpath", "payload");

    // Identity, not equality: the form re-renders what it sees change, so an
    // edit to one column must not rewrite the branch of another.
    expect((next.columns as unknown[])[0]).toBe(draft.columns[0]);
    expect(next.profile).toBe(draft.profile);
  });

  it("returns the draft unchanged when the pointer names no field", () => {
    expect(applyPickedSource(draft, "", "payload")).toBe(draft);
  });

  it("reads the escapes a JSON pointer uses for keys containing / and ~", () => {
    const odd = { columns: { "a/b~c": { jsonpath: "$.x" } } };

    expect(applyPickedSource(odd, "/columns/a~1b~0c/jsonpath", "payload")).toEqual({
      columns: { "a/b~c": { jsonpath: "$.x", source: "payload" } },
    });
  });
});

// The same sibling read back. A column that already pairs a source with its path
// has that path written against the decoded column, so the playground has to
// start there — browsing the row instead reports no matches for a column that
// works, and invites the author to break it.
describe("pickedSource", () => {
  const draft = {
    profile: "orders",
    columns: [
      { name: "id", jsonpath: "$.id" },
      { name: "email", jsonpath: "$.user.email", source: "payload" },
      { name: "blank", jsonpath: "$.x", source: "" },
    ],
  };
  const ctx = (instancePath: string) => ({ rootValue: draft, instancePath });

  it("reports the source declared beside the field", () => {
    expect(pickedSource(ctx("/columns/1/jsonpath"))).toBe("payload");
  });

  it("reports nothing for a column that declares none", () => {
    expect(pickedSource(ctx("/columns/0/jsonpath"))).toBeUndefined();
    expect(pickedSource(ctx("/columns/2/jsonpath"))).toBeUndefined();
  });

  it("reports nothing without a pointer, a root, or a column to read", () => {
    expect(pickedSource(undefined)).toBeUndefined();
    expect(pickedSource(ctx(""))).toBeUndefined();
    expect(pickedSource(ctx("/columns/9/jsonpath"))).toBeUndefined();
  });
});
