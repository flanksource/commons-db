/**
 * What a filterable column of a browsed query holds. The console asks the
 * source rather than the rows it has: a value list built from one page offers
 * only the values that page happened to contain, which is the opposite of what
 * a filter is for.
 */

import type {
  QueryBrowserFilterLookup,
  QueryBrowserFilterLookupRequest,
} from "@flanksource/clicky-ui";
import { fetchJSON } from "./connectionBrowserModel";

type FilterValuesResponse = {
  options: { value: string; label?: string; count?: number }[];
  total?: number;
  /**
   * How to read `total`, in the same vocabulary the profile export headers use.
   * OpenSearch counts distinct values with a cardinality aggregation, which is
   * an estimate past its precision threshold — so "gte" means the number is a
   * bound and not a count.
   */
  totalRelation?: "eq" | "gte" | "unknown";
  truncated?: boolean;
};

/**
 * makeBrowserFilterLookup binds a lookup to a connection's browser. The query,
 * its options and the rest of the selection all travel with the request, so the
 * suggestions are scoped to the rows the run would return — a value that would
 * leave the table empty is not offered.
 */
export function makeBrowserFilterLookup(
  baseUrl: string,
): QueryBrowserFilterLookup | undefined {
  if (!baseUrl) return undefined;
  return async (request: QueryBrowserFilterLookupRequest) => {
    const response = await fetchJSON<FilterValuesResponse>(
      `${baseUrl}/filters/values`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          query: request.query,
          options: request.options,
          filters: request.filters,
          ...(request.columns ? { columns: request.columns } : {}),
          filterKey: request.filterKey,
          search: request.search,
          limit: request.limit,
        }),
      },
    );
    return { options: response.options ?? [] };
  };
}
