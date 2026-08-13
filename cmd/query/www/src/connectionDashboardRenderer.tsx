import type { ResultRenderContext } from "@flanksource/clicky-ui";
import type { ReactNode } from "react";
import { ConnectionDashboardSurface } from "./connectionDashboard";
import { connectionDashboardUrl } from "./connectionDashboardModel";

// The dashboard's identity is its request URL alone. Keying it off the response
// body instead would mint a fresh query cache entry on nearly every render,
// which is what made the surface refetch constantly.
export function connectionDashboardResultRenderer(
  context: ResultRenderContext,
): ReactNode {
  if (context.surfaceKey !== "connection") return context.defaultView;
  return (
    <ConnectionDashboardSurface
      requestUrl={connectionDashboardUrl(context.response?.requestUrl)}
    />
  );
}
