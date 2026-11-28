/** @vitest-environment jsdom */

import type { JsonSchemaObject } from "@flanksource/clicky-ui";
import { useConnectionSchema } from "@flanksource/clicky-ui/connections";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { connectionsClient } from "./connectionsApi";

const servedSchema: JsonSchemaObject = {
  type: "object",
  properties: { url: { type: "string", "x-clicky-component": "k8s-url-selector" } },
};

// connectionsClient binds globalThis.fetch when its module loads, so the stub
// has to be in place before the imports above run.
const requests = vi.hoisted(() => {
  const seen: Array<{ path: string; accept: string | null }> = [];
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), document.baseURI).pathname;
    const accept = new Headers(init?.headers).get("Accept");
    seen.push({ path, accept });
    if (path !== "/api/v1/connection" || accept !== "application/schema+json") {
      return new Response("404 page not found", { status: 404 });
    }
    return Response.json(
      { type: "object", properties: { url: { type: "string", "x-clicky-component": "k8s-url-selector" } } },
      { headers: { "Content-Type": "application/schema+json" } },
    );
  }) as typeof fetch;
  return seen;
});

describe("connectionsClient", () => {
  let root: Root;
  let schema: JsonSchemaObject | null | undefined;

  beforeEach(() => {
    requests.length = 0;
    document.body.innerHTML = '<div id="root"></div>';
    root = createRoot(document.getElementById("root")!);
  });

  afterEach(() => {
    act(() => root.unmount());
  });

  it("serves useConnectionSchema the connection schema from the /api/v1 connections service", async () => {
    function Probe() {
      schema = useConnectionSchema(connectionsClient).data;
      return null;
    }
    await act(async () => {
      root.render(<QueryClientProvider client={new QueryClient()}><Probe /></QueryClientProvider>);
    });
    await vi.waitFor(async () => {
      await act(async () => {});
      expect(schema).toEqual(servedSchema);
    });
    expect(requests).toEqual([{ path: "/api/v1/connection", accept: "application/schema+json" }]);
  });
});
