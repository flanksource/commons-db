import { describe, expect, it } from "vitest";
import { openTraceRequests, pageUrl, TRACE_PAGE_SIZE } from "./profileOpenTrace";

const traceId = "92e8ccd4edb5839406a04d963a1d7a14";
const listing =
  "/api/v1/profile-os?limit=100&offset=100&filter.service=oipa-oipa&filter.duration_ms=%3E%3D100ms&sort=duration_ms&order=desc&since=now-1h";

const query = (url: string) => Object.fromEntries(new URL(url, "http://host").searchParams);

describe("openTraceRequests", () => {
  // The listing's page size, offset and sort describe its own page, not the
  // trace: a profile that caps pages below TRACE_PAGE_SIZE still pages the
  // trace, a cap's-size page at a time.
  it("reads every span of the trace: the listing's span filters, page and sort are dropped, its params kept", () => {
    const { spans } = openTraceRequests(listing, traceId);
    expect(spans.startsWith("/api/v1/profile-os?")).toBe(true);
    expect(query(spans)).toEqual({
      since: "now-1h",
      "filter.trace_id": traceId,
      limit: String(TRACE_PAGE_SIZE),
    });
  });

  it("asks which of the trace's spans the active filters match, under the same filters", () => {
    const { matches } = openTraceRequests(listing, traceId);
    expect(query(matches!)).toEqual({
      since: "now-1h",
      "filter.service": "oipa-oipa",
      "filter.duration_ms": ">=100ms",
      "filter.trace_id": traceId,
      limit: String(TRACE_PAGE_SIZE),
    });
  });

  it("has no match query when no filter is active, so every span shows", () => {
    expect(openTraceRequests("/api/v1/profile-os?limit=100&since=now-1h", traceId).matches).toBeUndefined();
  });

  it("resumes a page by cursor on the same request", () => {
    const { spans } = openTraceRequests(listing, traceId);
    expect(query(pageUrl(spans, "next-page"))).toEqual({ ...query(spans), cursor: "next-page" });
  });
});
