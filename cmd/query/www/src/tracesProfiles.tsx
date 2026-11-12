import { useMemo, type ReactNode } from "react";
import {
  ExplorerNavPortal,
  useClickyTableMenu,
  useRouter,
  type DataTableMenuAction,
  type ResultRenderContext,
} from "@flanksource/clicky-ui";
import {
  OtelTraceTable,
  parseServerTiming,
  profileListingSort,
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

// Trace listings page by offset: the reader steps through grouped traces with
// prev/next rather than a cursor walk that only grows on scroll.
export function tracePaginationMode(
  surfaces: Map<string, ProfileRenderSurface>,
): (surfaceKey: string) => "paged" | undefined {
  return (surfaceKey) => (surfaces.has(surfaceKey) ? "paged" : undefined);
}

export function tracesResultRenderer(
  surfaces: Map<string, ProfileRenderSurface>,
  menuActionsFor: (surfaceKey: string) => DataTableMenuAction[],
): (context: ResultRenderContext) => ReactNode {
  return (context) => {
    const response = context.response;
    const surfaceKey = response?.requestUrl
      ? entitySegment(response.requestUrl)
      : undefined;
    if (!response || !surfaceKey || !surfaces.has(surfaceKey))
      return context.defaultView;
    const rows = clickyLogsWalk(context.pages, response, {
      rawValues: true,
    });
    if (!rows)
      return (
        <p role="alert">Traces render mode requires a table of span rows.</p>
      );
    return (
      <ProfileTraces
        key={context.surfaceKey}
        rows={rows}
        context={context}
        hostMenuActions={menuActionsFor(surfaceKey)}
      />
    );
  };
}

function ProfileTraces({
  rows,
  context,
  hostMenuActions,
}: {
  rows: Record<string, unknown>[];
  context: ResultRenderContext;
  hostMenuActions: DataTableMenuAction[];
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
        sorting: context.sort ? profileListingSort(table, context.sort) : undefined,
      };
    } catch (error) {
      return { error: error instanceof Error ? error.message : String(error) };
    }
  }, [rows, context.pages, context.response, context.sort]);
  const menu = useClickyTableMenu({
    url: context.response?.requestUrl,
    download: context.download,
    pagination: context.pagination,
  });
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
    <>
    {menu.dialog}
    <OtelTraceTable
      menuActions={[...hostMenuActions, ...menu.actions]}
      {...(result.sorting ?? {})}
      {...(context.filterConfig?.cellFilters
        ? { cellFilters: context.filterConfig.cellFilters }
        : {})}
      {...(context.filterConfig?.onCellFilterChange
        ? { onCellFilterChange: context.filterConfig.onCellFilterChange }
        : {})}
      spans={result.spans!}
      serverTiming={parseServerTiming(context.response?.responseHeaders?.["server-timing"])}
      traceHref={href}
      renderTraceTitle={(title) => (
        <ExplorerNavPortal parentHref={href()}>{title}</ExplorerNavPortal>
      )}
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
    </>
  );
}
