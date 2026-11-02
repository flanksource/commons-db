// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, it, vi } from "vitest";
import { ConnectionOtel } from "./connectionOtel";

let root: Root;
let container: HTMLDivElement;
afterEach(async () => { await act(async () => root?.unmount()); container?.remove(); vi.unstubAllGlobals(); window.history.replaceState(null, "", "/"); });
it("keeps the connection and filters when opening a trace and restoring search", async () => {
  window.history.replaceState(null, "", "/connection/search?tab=traces&index=example-*&minSpans=2");
  const summary = { traceId: "0123456789abcdef0123456789abcdef", operation: "POST /orders", service: "checkout", startedAt: "2026-11-02T10:00:00Z", durationMs: 100, spanCount: 2, errors: 0, services: ["checkout"] };
  const fetchMock = vi.fn((url: string) => Promise.resolve(new Response(JSON.stringify(url.includes(`/traces/${summary.traceId}`) ? {
    summary, spans: [{ traceId: summary.traceId, spanId: "root", operation: summary.operation, service: "checkout", startedAt: summary.startedAt, durationMs: 100, status: "ok" }], missingParents: [],
  } : { traces: [summary], total: 1, services: ["checkout"], attributes: [], rootOperations: [{ operation: summary.operation, count: 1, p50Ms: 100, p95Ms: 100 }] }))));
  vi.stubGlobal("fetch", fetchMock);
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  container = document.createElement("div"); document.body.appendChild(container); root = createRoot(container);
  await act(async () => { root.render(<QueryClientProvider client={new QueryClient()}><ConnectionOtel id="search" name="Search" /></QueryClientProvider>); });
  await vi.waitFor(() => expect(container.querySelector("tbody a")).not.toBeNull());
  await act(async () => { container.querySelector<HTMLAnchorElement>("tbody a")!.click(); });
  await vi.waitFor(() => expect(container.textContent).toContain("Back to trace search"));
  expect(new URLSearchParams(window.location.search).get("index")).toBe("example-*");
  expect(new URLSearchParams(window.location.search).get("trace")).toBe(summary.traceId);
  await act(async () => { Array.from(container.querySelectorAll("button")).find(button => button.textContent === "Back to trace search")!.click(); });
  await vi.waitFor(() => expect(container.querySelector("h2")?.textContent).toBe("Traces"));
  expect(new URLSearchParams(window.location.search).get("minSpans")).toBe("2");
});
