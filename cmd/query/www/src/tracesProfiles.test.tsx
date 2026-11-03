// @vitest-environment jsdom
import { act, type ComponentProps } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { OtelTraceTable } from "@flanksource/clicky-ui/data";
import type { ResultRenderContext } from "@flanksource/clicky-ui";
import { afterEach, expect, it, vi } from "vitest";
import { profileRenderSurfaces } from "./logsProfiles";
import { tracesResultRenderer } from "./tracesProfiles";

const observed = vi.hoisted(() => ({
  props: undefined as ComponentProps<typeof OtelTraceTable> | undefined,
}));
vi.mock("@flanksource/clicky-ui/data", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@flanksource/clicky-ui/data")>()),
  OtelTraceTable: (props: ComponentProps<typeof OtelTraceTable>) => {
    observed.props = props;
    return (
      <div>
        <button onClick={() => props.onSelectTrace("trace-a")}>
          Open trace
        </button>
        <button onClick={() => props.onSelectSpan("child")}>Select span</button>
        <button onClick={() => props.onSelectTrace()}>Back</button>
      </div>
    );
  },
}));

let root: Root;
let container: HTMLDivElement;
afterEach(async () => {
  await act(async () => root?.unmount());
  container?.remove();
  vi.unstubAllGlobals();
  window.history.replaceState(null, "", "/");
  observed.props = undefined;
});
const cell = (plain: string) => ({ kind: "text", plain });
const page = (id: string) => ({
  success: true,
  exit_code: 0,
  stdout: "",
  requestUrl:
    "/api/v1/profile/profile-service-traces?limit=100&service=checkout",
  parsed: {
    node: {
      kind: "table",
      columns: [{ name: "operation", label: "Operation" }],
      rows: [
        {
          cells: {
            _trace: {
              kind: "code",
              language: "json",
              source: JSON.stringify({
                traceId: "trace-a",
                spanId: id,
                startedAt: "2026-11-02T10:00:00Z",
                service: "checkout",
                operation: "POST /orders",
                status: "unset",
                durationMs: 0.25,
                attributes: { cached: false, "db.rows": 3 },
              }),
            },
            trace_id: cell("trace-a"),
            span_id: cell(id),
            timestamp: cell("2026-11-02T10:00:00Z"),
            service: cell("checkout"),
            operation: cell("POST /orders"),
            duration_ms: {
              kind: "text",
              plain: "250…",
              style: { maxWidth: 4 },
              children: [cell("250 µs")],
              filterValue: 0.25,
            },
            _span_attributes: {
              kind: "code",
              language: "json",
              source: '{"cached":false,"db.rows":3}',
            },
          },
        },
      ],
    },
  },
});
const surfaces = profileRenderSurfaces(
  [
    { profile: "Service traces", render: "traces" },
    { profile: "Live stream", render: "trace" },
  ],
  "traces",
);

it("uses only the traces render mode and leaves other results alone", () => {
  expect([...surfaces.keys()]).toEqual(["profile-service-traces"]);
  const context = {
    response: page("root"),
    loading: false,
    defaultView: "table",
  };
  expect(tracesResultRenderer(new Map())(context)).toBe("table");
});

it("keeps all cursor pages, typed fields and server controls while preserving route filters", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  window.history.replaceState(
    null,
    "",
    "/profile-service-traces?service=checkout&limit=100",
  );
  const pagination = {
    page: 1,
    pageSize: 100,
    onPageChange: vi.fn(),
    onPageSizeChange: vi.fn(),
  };
  const filterConfig = { search: { value: "orders", onChange: vi.fn() } };
  const context: ResultRenderContext = {
    surfaceKey: "profile-service-traces",
    response: page("child"),
    pages: [page("root"), page("child")],
    loading: true,
    defaultView: "table",
    pagination,
    filterConfig,
  };
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => root.render(tracesResultRenderer(surfaces)(context)));
  expect(
    observed.props?.spans.map((span) => ({
      id: span.spanId,
      duration: span.durationMs,
      attributes: span.attributes,
    })),
  ).toEqual([
    { id: "root", duration: 0.25, attributes: { cached: false, "db.rows": 3 } },
    {
      id: "child",
      duration: 0.25,
      attributes: { cached: false, "db.rows": 3 },
    },
  ]);
  expect(observed.props?.pagination).toBe(pagination);
  expect(observed.props?.externalSearch).toBe(filterConfig.search);
  expect(observed.props?.loading).toBe(true);
  expect(
    observed.props?.listing?.columns.map((column) => ({
      key: column.key,
      label: column.label,
    })),
  ).toEqual([{ key: "operation", label: "Operation" }]);
  await act(async () => container.querySelectorAll("button")[0]!.click());
  expect(new URLSearchParams(window.location.search).get("trace")).toBe(
    "trace-a",
  );
  await act(async () => root.render(tracesResultRenderer(surfaces)(context)));
  await act(async () => container.querySelectorAll("button")[1]!.click());
  expect(new URLSearchParams(window.location.search).get("span")).toBe("child");
  await act(async () => container.querySelectorAll("button")[2]!.click());
  expect(window.location.search).toBe("?service=checkout&limit=100");
});
