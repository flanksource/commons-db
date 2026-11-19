import { OtelTraceBrowser } from "@flanksource/clicky-ui/data";
import { mountedRoute } from "./mount";

export function ConnectionOtel({ id, name }: { id: string; name: string }) {
  return <OtelTraceBrowser baseUrl={mountedRoute(`/api/v1/connection/${encodeURIComponent(id)}/browser/traces`)} sources={[{ id, name, type: "opensearch" }]} defaults={{ lookback: "1h", minSpans: 2, limit: 100 }} allowRecords={false} />;
}
