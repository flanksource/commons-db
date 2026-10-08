/** @vitest-environment jsdom */
// Verifies ConnectionTrace stops its trace session through the session API's
// stop route, from the Stop button and when the page unmounts.

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConnectionTrace } from "./connectionTrace";

vi.mock("@flanksource/clicky-ui/profiles", () => ({
  browserBaseUrl: (id: string) => `/api/v1/connection/${id}/browser`,
  useInspection: () => ({ databases: [], loading: false, error: undefined }),
}));

const STOP_URL = "/api/v1/sessions/s1/stop";

type Deferred = { resolve: (response: Response) => void };

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

let fetchMock: ReturnType<typeof vi.fn>;
let pendingStart: Deferred | undefined;
let container: HTMLDivElement;
let root: Root;

function serveTraceAPI(deferStart: boolean) {
  fetchMock = vi.fn((url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    if (method === "POST" && url === "/api/v1/connection/c1/trace/sessions") {
      if (!deferStart) return Promise.resolve(json({ id: "s1", state: "running" }));
      return new Promise<Response>((resolve) => {
        pendingStart = { resolve };
      });
    }
    if (method === "POST" && url === STOP_URL)
      return Promise.resolve(json({ id: "s1", state: "stopped" }));
    if (method === "GET" && url === "/api/v1/sessions/s1")
      return Promise.resolve(json({ id: "s1", state: "running" }));
    if (method === "GET" && url === "/api/v1/sessions/s1/result")
      return Promise.resolve(json([]));
    return Promise.resolve(new Response("404 page not found", { status: 404 }));
  });
  vi.stubGlobal("fetch", fetchMock);
}

function button(label: string): HTMLButtonElement {
  const found = Array.from(container.querySelectorAll("button")).find(
    (candidate) => candidate.textContent === label,
  );
  if (!found) throw new Error(`no ${label} button`);
  return found;
}

async function click(label: string) {
  await act(async () => {
    button(label).click();
  });
}

async function resolveStart() {
  await act(async () => {
    pendingStart?.resolve(json({ id: "s1", state: "running" }));
  });
}

async function unmount() {
  await act(async () => {
    root.unmount();
  });
}

function expectStopSent() {
  expect(fetchMock).toHaveBeenCalledWith(STOP_URL, { method: "POST" });
  const methods = fetchMock.mock.calls.map(
    ([, init]) => (init as RequestInit | undefined)?.method,
  );
  expect(methods).not.toContain("DELETE");
}

describe("ConnectionTrace stop", () => {
  beforeEach(() => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    pendingStart = undefined;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    container.remove();
    vi.unstubAllGlobals();
  });

  async function mount() {
    await act(async () => {
      root.render(<ConnectionTrace id="c1" />);
    });
  }

  it("stops a running trace from the Stop button", async () => {
    serveTraceAPI(false);
    await mount();
    await click("Start");
    await click("Stop");

    expectStopSent();
    expect(container.querySelector('[role="alert"]')).toBeNull();
    await unmount();
  });

  it("stops a running trace when the page unmounts", async () => {
    serveTraceAPI(false);
    await mount();
    await click("Start");
    await unmount();

    expectStopSent();
  });

  it("stops a trace whose start returns after Stop was pressed", async () => {
    serveTraceAPI(true);
    await mount();
    await click("Start");
    await click("Stop");
    await resolveStart();

    expectStopSent();
    expect(container.querySelector('[role="alert"]')).toBeNull();
    await unmount();
  });

  it("stops a trace whose start returns after the page unmounted", async () => {
    serveTraceAPI(true);
    await mount();
    await click("Start");
    await unmount();
    await resolveStart();

    expectStopSent();
  });
});
