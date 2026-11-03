import { useMemo, type ReactNode } from "react";
import { useRouter, type ResultRenderContext } from "@flanksource/clicky-ui";
import {
  OtelTraceTable,
  profileSpans,
  profileTraceListing,
} from "@flanksource/clicky-ui/data";
import {
  clickyLogsWalk,
  clickyTable,
  entitySegment,
  profileRenderSurfaces,
  type ProfileRenderSurface,
} from "./logsProfiles";
import { useProfiles } from "./profilesQuery";

export function useTraceSurfaces() {
  const { data } = useProfiles();
  return profileRenderSurfaces(data ?? [], "traces");
}

export function tracesResultRenderer(
  surfaces: Map<string, ProfileRenderSurface>,
): (context: ResultRenderContext) => ReactNode {
  return (context) => {
    if (
      !context.response?.requestUrl ||
      !surfaces.has(entitySegment(context.response.requestUrl))
    )
      return context.defaultView;
    const rows = clickyLogsWalk(context.pages, context.response, {
      rawValues: true,
    });
    if (!rows)
      return (
        <p role="alert">Traces render mode requires a table of span rows.</p>
      );
    return (
      <ProfileTraces key={context.surfaceKey} rows={rows} context={context} />
    );
  };
}

function ProfileTraces({
  rows,
  context,
}: {
  rows: Record<string, unknown>[];
  context: ResultRenderContext;
}) {
  const { navigate, pathname } = useRouter();
  const params = new URLSearchParams(window.location.search);
  const result = useMemo(() => {
    try {
      const table = clickyTable(context.response!.parsed);
      if (!table) throw new Error("Trace listing requires a rendered table");
      const pages =
        context.pages && context.pages.length > 1
          ? context.pages
          : [context.response!];
      return {
        spans: profileSpans(rows),
        listing: profileTraceListing(rows, {
          ...table,
          rows: pages.flatMap((page) => clickyTable(page.parsed)?.rows ?? []),
        }),
      };
    } catch (error) {
      return { error: error instanceof Error ? error.message : String(error) };
    }
  }, [rows, context.pages, context.response]);
  const href = (trace?: string, span?: string) => {
    const query = new URLSearchParams(window.location.search);
    query.delete("trace");
    query.delete("span");
    if (trace) query.set("trace", trace);
    if (span) query.set("span", span);
    return `${pathname}${query.size ? `?${query}` : ""}`;
  };
  if (result.error)
    return <p role="alert">Cannot render traces: {result.error}</p>;
  return (
    <OtelTraceTable
      spans={result.spans!}
      traceHref={href}
      onSelectTrace={(trace) => navigate(href(trace))}
      onSelectSpan={(span) =>
        navigate(href(params.get("trace") ?? undefined, span), {
          replace: true,
        })
      }
      listing={result.listing!}
      {...(params.get("trace")
        ? { selectedTraceId: params.get("trace")! }
        : {})}
      {...(params.get("span") ? { selectedSpanId: params.get("span")! } : {})}
      loading={context.loading}
      {...(context.filterConfig?.filters
        ? { externalFilters: context.filterConfig.filters }
        : {})}
      {...(context.filterConfig?.search
        ? { externalSearch: context.filterConfig.search }
        : {})}
      {...(context.filterConfig?.timeRange
        ? { externalTimeRange: context.filterConfig.timeRange }
        : {})}
      {...(context.pagination ? { pagination: context.pagination } : {})}
      {...(context.infinite ? { infinite: context.infinite } : {})}
    />
  );
}
