import { fetchJSON } from "@flanksource/clicky-ui/profiles";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import type { ConnectionInfo } from "./connectionInfoTypes";
import { HeaderDot } from "./headerDot";
import { ServerStatus } from "./serverStatus";

// ConnectionInfoHeader renders the connection's identity and resolved server on
// a single line for the explorer heading: [icon] name · endpoint · product ·
// status. It shares the ["connection-info", id] query cache with the browser.
export function ConnectionInfoHeader({
  id,
  icon,
  fallbackName,
}: {
  id: string;
  icon?: ReactNode;
  fallbackName: string;
}) {
  const info = useQuery({
    queryKey: ["connection-info", id],
    queryFn: () =>
      fetchJSON<ConnectionInfo>(
        `/api/v1/connection/${encodeURIComponent(id)}/info`,
      ),
    retry: 0,
    staleTime: 30_000,
  });
  const data = info.data;
  const name = data?.connection.name ?? fallbackName;
  const endpoint =
    data?.connection.resolvedEndpoint ?? data?.connection.configuredEndpoint;
  const product = data
    ? [data.server.product, data.server.version].filter(Boolean).join(" ")
    : "";
  return (
    <h1 className="flex min-w-0 items-center gap-2 text-2xl font-semibold tracking-tight">
      {icon}
      <span className="shrink-0">{name}</span>
      {info.isLoading ? (
        <span className="text-sm font-normal text-muted-foreground">
          resolving…
        </span>
      ) : info.isError ? (
        <span
          className="truncate text-sm font-normal text-destructive"
          title={info.error instanceof Error ? info.error.message : undefined}
        >
          {info.error instanceof Error ? info.error.message : "unresolved"}
        </span>
      ) : data ? (
        <span className="flex min-w-0 items-center gap-2 text-sm font-normal text-muted-foreground">
          {endpoint ? (
            <>
              <HeaderDot />
              <code className="min-w-0 truncate">{endpoint}</code>
            </>
          ) : null}
          {product ? (
            <>
              <HeaderDot />
              <span className="shrink-0">{product}</span>
            </>
          ) : null}
          <HeaderDot />
          <ServerStatus server={data.server} />
        </span>
      ) : null}
    </h1>
  );
}
