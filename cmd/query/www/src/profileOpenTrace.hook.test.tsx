/** @vitest-environment jsdom */

import type { OtelOpenTrace } from "@flanksource/clicky-ui/data";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { openTraceRequests, TRACE_AUTO_LOAD, TRACE_PAGE_SIZE, useOpenTrace } from "./profileOpenTrace";

const traceId = "selected-trace";
const listing = "/api/v1/profile-traces";

function deferredResponse() {
  let resolve!: (response: Response) => void;
  const promise = new Promise<Response>((done) => { resolve = done; });
  return { promise, resolve };
}

function tracePage(ids: string[], next?: string) {
  const rows = ids.map((spanId) => ({
    cells: { _trace: { kind: "code", language: "json", source: JSON.stringify({
      traceId, spanId, startedAt: "2026-01-01T00:00:00Z", service: "api",
      operation: "request", durationMs: 10, status: "ok",
    }) } },
  }));
  return Response.json({ kind: "table", rows }, {
    headers: next ? { "X-Has-More": "true", "X-Next-Cursor": next } : {},
  });
}

describe("useOpenTrace", () => {
  let root: Root;
  let client: QueryClient;
  let trace: OtelOpenTrace | undefined;

  function Probe({ url }: { url: string }) {
    trace = useOpenTrace(url, traceId);
    return null;
  }

  async function render(url: string) {
    await act(async () => {
      root.render(<QueryClientProvider client={client}><Probe url={url} /></QueryClientProvider>);
    });
  }

  function until(assert: () => void) {
    return vi.waitFor(async () => {
      await act(async () => {});
      assert();
    });
  }

  beforeEach(() => {
    vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
    root = createRoot(document.createElement("div"));
    client = new QueryClient();
    trace = undefined;
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    client.clear();
    vi.unstubAllGlobals();
  });

  it("does not apply a late response from a previous filter", async () => {
    const oldListing = `${listing}?filter.service=old`;
    const newListing = `${listing}?filter.service=new`;
    const oldMatches = openTraceRequests(oldListing, traceId).matches;
    const newMatches = openTraceRequests(newListing, traceId).matches;
    const oldResponse = deferredResponse();
    const fetchPage = vi.fn((url: string, _options: RequestInit) => {
      if (url === oldMatches) return oldResponse.promise;
      return Promise.resolve(tracePage(url === newMatches ? ["new-span"] : ["old-span", "new-span"]));
    });
    vi.stubGlobal("fetch", fetchPage);

    await render(oldListing);
    const request = fetchPage.mock.calls.find(([url]) => url === oldMatches);
    expect(request?.[1].signal?.aborted).toBe(false);

    await render(newListing);
    await until(() => expect(trace?.matchingSpanIds).toEqual(new Set(["new-span"])));
    expect(request?.[1].signal?.aborted).toBe(true);
    await act(async () => oldResponse.resolve(tracePage(["old-span"])));
    expect(trace?.matchingSpanIds).toEqual(new Set(["new-span"]));
  });

  it("shows pages as they arrive and resumes from the cursor at the batch limit", async () => {
    const secondPage = deferredResponse();
    const pagesPerBatch = TRACE_AUTO_LOAD / TRACE_PAGE_SIZE;
    const page = (index: number) => tracePage(
      Array.from({ length: TRACE_PAGE_SIZE }, (_, i) => `span-${index * TRACE_PAGE_SIZE + i}`),
      String(index + 1),
    );
    const fetchPage = vi.fn((url: string) => {
      const cursor = Number(new URL(url, "https://example.com").searchParams.get("cursor"));
      if (cursor === 1) return secondPage.promise;
      return Promise.resolve(cursor < pagesPerBatch ? page(cursor) : tracePage(["last-span"]));
    });
    vi.stubGlobal("fetch", fetchPage);

    await render(listing);
    await until(() => {
      expect(trace?.spans).toHaveLength(TRACE_PAGE_SIZE);
      expect(trace?.loading).toBe(true);
    });
    await act(async () => secondPage.resolve(page(1)));
    await until(() => expect(trace?.onLoadMore).toBeTypeOf("function"));
    expect(trace?.spans).toHaveLength(TRACE_AUTO_LOAD);
    expect(fetchPage).toHaveBeenCalledTimes(pagesPerBatch);

    await act(async () => trace?.onLoadMore?.());
    await until(() => expect(trace?.spans.at(-1)?.spanId).toBe("last-span"));
    expect(trace?.spans).toHaveLength(TRACE_AUTO_LOAD + 1);
    expect(trace?.onLoadMore).toBeUndefined();
  });

  it("keeps the spans already loaded when a later page fails", async () => {
    vi.stubGlobal("fetch", vi.fn((url: string) => Promise.resolve(
      new URL(url, "https://example.com").searchParams.has("cursor")
        ? new Response("unavailable", { status: 503 })
        : tracePage(["first-span"], "next"),
    )));
    await render(listing);
    await until(() => expect(trace?.warning).toBe("Stopped loading spans after 1: 503: unavailable"));
    expect(trace?.spans.map((span) => span.spanId)).toEqual(["first-span"]);
    expect(trace?.error).toBeUndefined();
    expect(trace?.onLoadMore).toBeTypeOf("function");
  });
});
