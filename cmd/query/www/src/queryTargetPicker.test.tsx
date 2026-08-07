import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { Inspection } from "./connectionBrowserModel";
import { QueryTargetPicker } from "./queryTargetPicker";

const inspection = (overrides: Partial<Inspection> = {}): Inspection => ({
  data: {
    kind: "opensearch",
    targets: [
      { name: "jaeger-span-*", kind: "pattern", count: 53 },
      { name: "logs-current", kind: "alias" },
    ],
  },
  nodes: [],
  databases: [],
  activeDatabase: "",
  sqlDatabase: "",
  targetKind: "",
  loading: false,
  error: undefined,
  ...overrides,
});

const render = (props: Partial<Parameters<typeof QueryTargetPicker>[0]> = {}) =>
  renderToStaticMarkup(
    <QueryTargetPicker
      label="Index"
      inspection={inspection()}
      value=""
      onChange={() => {}}
      {...props}
    />,
  );

describe("QueryTargetPicker", () => {
  it("labels itself with the target the server named", () => {
    expect(render()).toContain("Index");
  });

  it("shows the selected target rather than its rotation count", () => {
    expect(render({ value: "jaeger-span-*" })).toContain("jaeger-span-*");
  });

  it("surfaces an inspection failure instead of an empty picker", () => {
    const html = render({
      inspection: inspection({
        data: undefined,
        error: new Error("connection refused"),
      }),
    });

    expect(html).toContain("connection refused");
    expect(html).toContain("Unavailable");
  });
});
