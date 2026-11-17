import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { OtelOpenTrace } from "@flanksource/clicky-ui/data";
import { profileSpans } from "@flanksource/clicky-ui/data";
import { clickyLogsRows } from "./logsProfiles";

type Span = OtelOpenTrace["spans"][number];

/** Spans per request: the server's largest page. */
export const TRACE_PAGE_SIZE = 1000;
/** Spans loaded before the reader is asked whether to load more. */
export const TRACE_AUTO_LOAD = 10_000;
/** Matching spans past which the filters are too broad to mark inside one trace. */
export const TRACE_MATCH_LIMIT = 10_000;

const TRACE_SCOPE = "filter.trace_id";
const LISTING_ONLY = ["limit", "offset", "cursor", "sort", "order", "trace", "span"];

/**
 * The requests that open one trace of a traces profile, derived from the
 * listing's own request so its profile params (source scope, time params) carry
 * over. The spans request drops the listing's span filters: they pick which
 * traces the listing shows, never which spans the open trace has. The matches
 * request keeps them, so the server decides which spans they select with the
 * exact semantics it applied to the listing.
 */
export function openTraceRequests(listingUrl: string, traceId: string): { spans: string; matches?: string } {
  const [path, search = ""] = listingUrl.split("?");
  const listing = new URLSearchParams(search);
  for (const key of LISTING_ONLY) listing.delete(key);
  // No sort: the waterfall orders spans by start itself, and the profile's own
  // order is the one its index serves fast (on a Jaeger index an ascending
  // timestamp walk measured ~40 s a page against ~0.15 s descending). A
  // profile whose own size is smaller than the page serves it cap-sized pages
  // and keeps the walk going.
  const scoped = (params: URLSearchParams) => {
    params.set(TRACE_SCOPE, traceId);
    params.set("limit", String(TRACE_PAGE_SIZE));
    return `${path}?${params}`;
  };
  const filters = [...listing.keys()].filter((key) => key.startsWith("filter.") && key !== TRACE_SCOPE);
  const spans = new URLSearchParams(listing);
  for (const key of filters) spans.delete(key);
  return {
    spans: scoped(spans),
    ...(filters.length ? { matches: scoped(new URLSearchParams(listing)) } : {}),
  };
}

export function pageUrl(url: string, cursor?: string): string {
  if (!cursor) return url;
  const [path, search = ""] = url.split("?");
  const params = new URLSearchParams(search);
  params.set("cursor", cursor);
  return `${path}?${params}`;
}

/** `truncated`: the source cut this page and offers nothing after it, so the walk ends short. */
type TracePage = { spans: Span[]; next?: string; total?: number; truncated: boolean };

async function fetchTracePage(url: string, signal: AbortSignal): Promise<TracePage> {
  const response = await fetch(url, { headers: { Accept: "application/json+clicky" }, signal });
  if (!response.ok) {
    const body = await response.text();
    let message = body;
    try {
      message = (JSON.parse(body) as { message?: string }).message ?? body;
    } catch {
      // A non-JSON error body is reported as it was sent.
    }
    throw new Error(`${response.status}: ${message}`);
  }
  const rows = clickyLogsRows(await response.json(), { rawValues: true });
  if (!rows) throw new Error("Trace spans response is not a table of span rows");
  const next = response.headers.get("X-Has-More") === "true" ? response.headers.get("X-Next-Cursor") : null;
  const count = response.headers.get("X-Total-Count");
  return {
    spans: profileSpans(rows),
    ...(next ? { next } : {}),
    ...(count && response.headers.get("X-Total-Relation") === "eq" ? { total: Number(count) } : {}),
    truncated: !next && response.headers.get("X-Truncated") === "true",
  };
}

type SpansState = { spans: Span[]; next?: string; total?: number; loading: boolean; error?: string; warning?: string };
type MatchesState = { ids?: ReadonlySet<string>; loading: boolean; warning?: string };

const failure = (error: unknown) => (error instanceof Error ? error.message : String(error));
const aborted = (error: unknown) => error instanceof DOMException && error.name === "AbortError";

/**
 * Loads the open trace on its own, a cursor page at a time: TRACE_AUTO_LOAD
 * spans at once, then a page batch per onLoadMore. With filters active it walks
 * the matching span ids too, which the waterfall narrows to.
 */
export function useOpenTrace(listingUrl: string | undefined, traceId: string | undefined): OtelOpenTrace | undefined {
  const derived = listingUrl && traceId ? openTraceRequests(listingUrl, traceId) : undefined;
  const spansUrl = derived?.spans;
  const matchesUrl = derived?.matches;
  // Keyed by the derived URLs, not the listing's: paging or re-sorting the
  // listing leaves the open trace's requests, and its loaded spans, alone.
  const requests = useMemo(
    () => (spansUrl ? { spans: spansUrl, ...(matchesUrl ? { matches: matchesUrl } : {}) } : undefined),
    [spansUrl, matchesUrl],
  );
  const [spans, setSpans] = useState<SpansState>({ spans: [], loading: false });
  const [matches, setMatches] = useState<MatchesState>({ loading: false });
  const controller = useRef<AbortController | undefined>(undefined);

  const load = useCallback(
    async (url: string, from: SpansState) => {
      controller.current?.abort();
      const abort = new AbortController();
      controller.current = abort;
      let state: SpansState = { ...from, loading: true };
      setSpans(state);
      try {
        const budget = from.spans.length + TRACE_AUTO_LOAD;
        let cursor = from.next;
        do {
          const page = await fetchTracePage(pageUrl(url, cursor), abort.signal);
          cursor = page.next;
          const loaded = [...state.spans, ...page.spans];
          const total = page.total ?? state.total;
          state = {
            spans: loaded,
            ...(cursor ? { next: cursor } : {}),
            ...(total !== undefined ? { total } : {}),
            loading: Boolean(cursor) && loaded.length < budget,
            ...(page.truncated
              ? { warning: `The profile stopped returning spans after ${loaded.length}${total !== undefined ? ` of ${total}` : ""}; this trace is incomplete.` }
              : {}),
          };
          setSpans(state);
        } while (cursor && state.spans.length < budget);
      } catch (error) {
        if (aborted(error)) return;
        // Spans already loaded are still the trace: a later page failing
        // leaves it incomplete, which is a warning, not a lost trace.
        setSpans(
          state.spans.length > 0
            ? { ...state, loading: false, warning: `Stopped loading spans after ${state.spans.length}: ${failure(error)}` }
            : { ...state, loading: false, error: failure(error) },
        );
      }
    },
    [],
  );

  useEffect(() => {
    if (!requests) return;
    void load(requests.spans, { spans: [], loading: true });
    return () => controller.current?.abort();
  }, [requests, load]);

  useEffect(() => {
    if (!requests?.matches) {
      setMatches({ loading: false });
      return;
    }
    const abort = new AbortController();
    setMatches({ loading: true });
    void (async () => {
      const ids = new Set<string>();
      let cursor: string | undefined;
      let truncated = false;
      try {
        do {
          const page = await fetchTracePage(pageUrl(requests.matches!, cursor), abort.signal);
          for (const span of page.spans) ids.add(span.spanId);
          if (ids.size > TRACE_MATCH_LIMIT)
            throw new Error(`More than ${TRACE_MATCH_LIMIT} spans of this trace match the filters; narrow them`);
          truncated = page.truncated;
          cursor = page.next;
        } while (cursor);
        setMatches({
          ids,
          loading: false,
          ...(truncated ? { warning: "Filter matches are incomplete; some matching spans may be hidden." } : {}),
        });
      } catch (error) {
        // Without the matches the trace still renders, unfiltered.
        if (!aborted(error)) setMatches({ loading: false, warning: `Filters not applied inside the trace: ${failure(error)}` });
      }
    })();
    return () => abort.abort();
  }, [requests]);

  if (!requests) return undefined;
  const warning = [spans.warning, matches.warning].filter(Boolean).join("\n");
  return {
    spans: spans.spans,
    ...(spans.total !== undefined ? { total: spans.total } : {}),
    loading: spans.loading || matches.loading,
    ...(spans.error ? { error: spans.error } : {}),
    ...(warning ? { warning } : {}),
    ...(spans.next && !spans.loading ? { onLoadMore: () => void load(requests.spans, spans) } : {}),
    ...(matches.ids ? { matchingSpanIds: matches.ids } : {}),
  };
}
