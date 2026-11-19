import { useEffect, useState } from "react";
import { keepPreviousData, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, useRouter } from "@flanksource/clicky-ui";
import { OtelTraceSearch, OtelTraceDetail, otelSearchParams, readOtelSearchValues, type OtelTrace, type OtelSearchResult, type OtelSearchValues } from "@flanksource/clicky-ui/data";
import { mountedRoute } from "./mount";

export function ConnectionOtel({ id, name }: { id: string; name: string }) {
  const { navigate, pathname } = useRouter();
  const client = useQueryClient();
  const params = new URLSearchParams(window.location.search);
  const trace = params.get("trace");
  for (const key of ["tab", "trace", "span"]) params.delete(key);
  const [value, setValue] = useState<OtelSearchValues>(() => ({ lookback: "1h", minSpans: 2, limit: 100, ...readOtelSearchValues(params), source: id }));
  useEffect(() => {
    const restore = () => {
      const params = new URLSearchParams(window.location.search);
      for (const key of ["tab", "trace", "span"]) params.delete(key);
      setValue({ lookback: "1h", minSpans: 2, limit: 100, ...readOtelSearchValues(params), source: id });
    };
    window.addEventListener("popstate", restore);
    return () => window.removeEventListener("popstate", restore);
  }, [id]);
  const base = mountedRoute(`/api/v1/connection/${encodeURIComponent(id)}/browser/traces`);
  const read = async <T,>(url: string, signal: AbortSignal): Promise<T> => {
    const response = await fetch(url, { signal });
    if (!response.ok) throw new Error(await response.text());
    return response.json() as Promise<T>;
  };
  const search = useQuery({ queryKey: ["connection-traces", id, value], queryFn: ({ signal }) => read<OtelSearchResult>(`${base}?${otelSearchParams(value)}`, signal), enabled: !trace, placeholderData: keepPreviousData, retry: false });
  const detail = useQuery({ queryKey: ["connection-trace", id, value.index, value.format, trace], queryFn: ({ signal }) => read<OtelTrace>(`${base}/${encodeURIComponent(trace!)}?${otelSearchParams({ index: value.index, format: value.format })}`, signal), enabled: !!trace, retry: false });
  const href = (trace?: string, span?: string) => {
    const query = otelSearchParams(value); query.set("tab", "traces");
    if (trace) query.set("trace", trace);
    if (span) query.set("span", span);
    return `${pathname}?${query}`;
  };
  if (trace) return <div className="min-w-0 space-y-3">
    <Button variant="link" onClick={() => navigate(href())}>Back to trace search</Button>
    {detail.isPending && <p role="status">Loading trace…</p>}
    {detail.error && <p role="alert">{detail.error.message}</p>}
    {detail.data && <OtelTraceDetail trace={detail.data} {...(new URLSearchParams(window.location.search).get("span") ? { selectedSpanId: new URLSearchParams(window.location.search).get("span")! } : {})} onSelectSpan={span => navigate(href(trace, span), { replace: true })} />}
  </div>;
  return <OtelTraceSearch value={value} sources={[{ id, name, type: "opensearch" }]} allowRecords={false} {...(search.data ? { result: search.data } : {})} loading={search.isFetching} {...(search.error ? { error: search.error.message } : {})} onChange={next => {
    const merged = { ...value, ...next, source: id }; setValue(merged);
    const query = otelSearchParams(merged); query.set("tab", "traces"); navigate(`${pathname}?${query}`, { replace: true });
  }} onOpen={trace => navigate(href(trace))} href={href} onCancel={() => void client.cancelQueries({ queryKey: ["connection-traces", id] })} onRetry={() => void search.refetch()} />;
}
