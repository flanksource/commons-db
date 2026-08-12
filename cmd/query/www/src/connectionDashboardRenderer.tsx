import type { ResultRenderContext } from "@flanksource/clicky-ui";
import type { ReactNode } from "react";
import { ConnectionDashboardSurface } from "./connectionDashboard";
import { connectionDashboardUrl } from "./connectionDashboardModel";

export function connectionDashboardResultRenderer(
  context: ResultRenderContext,
): ReactNode {
  if (context.surfaceKey !== "connection") return context.defaultView;
  return (
    <ConnectionDashboardSurface
      requestUrl={connectionDashboardUrl(context.response?.requestUrl)}
      refreshKey={
        context.response?.output ?? JSON.stringify(context.response?.parsed ?? null)
      }
    />
  );
}
