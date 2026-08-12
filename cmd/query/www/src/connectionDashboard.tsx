import {
  Button,
  Icon,
  cn,
  useRouter,
} from "@flanksource/clicky-ui";
import {
  UiKey,
  UiLink,
  UiLock,
  UiWarningTriangle,
} from "@flanksource/clicky-ui/icons";
import { useQuery } from "@tanstack/react-query";
import { ConnectionTypeIcon } from "./iconProvider";
import {
  groupConnectionDashboardLanes,
  type ConnectionDashboardHealthState,
  type ConnectionDashboardItem,
  type ConnectionDashboardResponse,
} from "./connectionDashboardModel";

const HEALTH_LEGEND_LABELS: Record<ConnectionDashboardHealthState, string> = {
  healthy: "reachable",
  credentials: "credentials failed",
  unreachable: "unreachable",
  unverifiable: "no discovery for this type",
};

const HEALTH_LABELS: Record<ConnectionDashboardHealthState, string> = {
  healthy: "Reachable",
  credentials: "Credentials failed",
  unreachable: "Unreachable",
  unverifiable: "Not verifiable",
};

const HEALTH_STYLES: Record<
  ConnectionDashboardHealthState,
  { dot: string; edge: string }
> = {
  healthy: { dot: "bg-emerald-400", edge: "border-l-emerald-400" },
  credentials: { dot: "bg-amber-400", edge: "border-l-amber-400" },
  unreachable: { dot: "bg-rose-400", edge: "border-l-rose-400" },
  unverifiable: { dot: "bg-muted-foreground", edge: "border-l-border" },
};

export function ConnectionDashboardSurface({
  requestUrl,
  refreshKey,
}: {
  requestUrl: string;
  refreshKey: string;
}) {
  const dashboard = useQuery({
    queryKey: ["connection-dashboard", requestUrl, refreshKey],
    queryFn: async () => {
      const response = await fetch(requestUrl, {
        headers: { Accept: "application/json" },
      });
      if (!response.ok) {
        const message = (await response.text()).trim();
        throw new Error(message || `Connection dashboard failed: ${response.status}`);
      }
      const payload = (await response.json()) as Partial<ConnectionDashboardResponse>;
      if (!Array.isArray(payload.connections)) {
        throw new Error("Connection dashboard response is missing connections");
      }
      return payload.connections;
    },
    staleTime: 30_000,
    retry: 0,
  });

  if (dashboard.isLoading) {
    return (
      <div className="rounded-lg border bg-card p-6 text-sm text-muted-foreground">
        Loading connection health…
      </div>
    );
  }
  if (dashboard.isError) {
    return (
      <div className="flex items-center justify-between gap-4 rounded-lg border border-destructive/40 bg-destructive/5 p-4 text-sm text-destructive">
        <span>
          {dashboard.error instanceof Error
            ? dashboard.error.message
            : "Unable to load connection health"}
        </span>
        <Button type="button" variant="outline" size="sm" onClick={() => void dashboard.refetch()}>
          Retry
        </Button>
      </div>
    );
  }
  if (!dashboard.data) {
    return (
      <div className="rounded-lg border border-destructive/40 bg-destructive/5 p-4 text-sm text-destructive">
        Connection dashboard returned no data.
      </div>
    );
  }
  if (dashboard.data.length === 0) {
    return (
      <div className="rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">
        No connections match the current filters.
      </div>
    );
  }
  return <ConnectionDashboardLanes connections={dashboard.data} />;
}

export function ConnectionDashboardLanes({
  connections,
}: {
  connections: ConnectionDashboardItem[];
}) {
  return (
    <div className="space-y-4">
      {groupConnectionDashboardLanes(connections).map((lane) => {
        const failing = lane.connections.filter(
          (connection) =>
            connection.health.state === "credentials" ||
            connection.health.state === "unreachable",
        ).length;
        const unused = lane.connections.filter(
          (connection) => connection.profileCount === 0,
        ).length;
        const unnamespaced = lane.namespace === "";
        return (
          <section
            key={lane.namespace || "__none__"}
            className={cn(
              "overflow-hidden rounded-lg border",
              unnamespaced ? "border-dashed border-border" : "border-border",
            )}
          >
            <header className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-border bg-muted/40 px-3 py-2">
              <h2
                className={cn(
                  "text-sm font-semibold",
                  unnamespaced && "italic text-muted-foreground",
                )}
              >
                {unnamespaced ? "No namespace" : lane.namespace}
              </h2>
              <span className="rounded bg-background px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">
                {lane.connections.length}
              </span>
              {failing > 0 ? (
                <span className="inline-flex items-center gap-1 rounded bg-rose-100 px-1.5 py-0.5 text-[11px] text-rose-700 ring-1 ring-inset ring-rose-300/60 dark:bg-rose-400/10 dark:text-rose-300 dark:ring-rose-400/25">
                  {failing} failing
                </span>
              ) : null}
              {unused > 0 ? (
                <span className="text-[11px] text-muted-foreground">{unused} unused</span>
              ) : null}
              {unnamespaced ? (
                <span className="text-xs text-muted-foreground">
                  `namespace` is optional on the model, so these are unplaceable rather than global.
                </span>
              ) : null}
            </header>
            <div className="divide-y divide-border">
              {lane.connections.map((connection) => (
                <ConnectionDashboardRow key={connection.id} connection={connection} />
              ))}
            </div>
          </section>
        );
      })}
      <ConnectionHealthLegend />
    </div>
  );
}

function ConnectionDashboardRow({
  connection,
}: {
  connection: ConnectionDashboardItem;
}) {
  const { renderLink } = useRouter();
  return renderLink({
    to: `/connection/${encodeURIComponent(connection.id)}`,
    title: connection.health.detail,
    className: cn(
      "flex min-w-0 items-center gap-3 border-l-2 py-2 pl-3 pr-2 hover:bg-muted/30 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset",
      HEALTH_STYLES[connection.health.state].edge,
    ),
    children: (
      <>
        <span className="shrink-0" aria-hidden>
          <ConnectionTypeIcon type={connection.type} className="size-4" />
        </span>
        <div className="flex w-36 shrink-0 items-center gap-1.5 md:w-44">
          <HealthDot state={connection.health.state} detail={connection.health.detail} />
          <span className="truncate text-sm font-medium" title={connection.name}>
            {connection.name}
          </span>
        </div>
        <div className="min-w-0 flex-1">
          <EndpointLine endpoint={connection.endpoint} />
        </div>
        <div className="hidden shrink-0 items-center gap-1.5 lg:flex">
          <SecretChip count={connection.secretCount} />
          <RiskChips connection={connection} />
        </div>
        <span className="w-24 shrink-0 text-right text-xs text-muted-foreground">
          {connection.profileCount === 0 ? (
            <span className="text-muted-foreground/60">unused</span>
          ) : (
            `${connection.profileCount} profile${connection.profileCount === 1 ? "" : "s"}`
          )}
        </span>
        <span className="hidden w-16 shrink-0 text-right text-xs text-muted-foreground/70 xl:inline">
          {ageLabel(connection.updatedAt)}
        </span>
      </>
    ),
  });
}

function HealthDot({
  state,
  detail,
}: {
  state: ConnectionDashboardHealthState;
  detail: string;
}) {
  return (
    <span
      role="img"
      className={cn("inline-block size-2 shrink-0 rounded-full", HEALTH_STYLES[state].dot)}
      title={detail}
      aria-label={HEALTH_LABELS[state]}
    />
  );
}

function EndpointLine({
  endpoint,
}: {
  endpoint: ConnectionDashboardItem["endpoint"];
}) {
  if (!endpoint) {
    return (
      <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
        <Icon icon={UiLink} className="text-xs" />
        <span className="italic">Resolved from ambient config</span>
      </span>
    );
  }
  const indirect = ["connection", "host", "portforward"].includes(endpoint.scheme);
  return (
    <span className="flex min-w-0 items-center gap-1.5 font-mono text-xs">
      <span
        className={cn(
          "shrink-0 rounded px-1 py-0.5 text-[10px] ring-1 ring-inset",
          indirect
            ? "bg-violet-100 text-violet-700 ring-violet-300/60 dark:bg-violet-400/10 dark:text-violet-300 dark:ring-violet-400/25"
            : "bg-muted text-muted-foreground ring-border",
        )}
      >
        {endpoint.scheme}
      </span>
      <span
        className="truncate text-foreground"
        title={`${endpoint.host}${endpoint.path ?? ""}`}
      >
        {endpoint.host}
        {endpoint.path ? (
          <span className="text-muted-foreground">{endpoint.path}</span>
        ) : null}
      </span>
    </span>
  );
}

function SecretChip({ count }: { count: number }) {
  if (count === 0) return null;
  return (
    <span className="inline-flex items-center gap-1 rounded bg-teal-100 px-1.5 py-0.5 text-[11px] text-teal-700 ring-1 ring-inset ring-teal-300/60 dark:bg-teal-400/10 dark:text-teal-300 dark:ring-teal-400/25">
      <Icon icon={UiLock} className="text-[11px]" />
      {count} secret{count === 1 ? "" : "s"}
    </span>
  );
}

function RiskChips({ connection }: { connection: ConnectionDashboardItem }) {
  return (
    <>
      {connection.inlineCredential ? (
        <span className="inline-flex items-center gap-1 rounded bg-rose-100 px-1.5 py-0.5 text-[11px] text-rose-700 ring-1 ring-inset ring-rose-300/60 dark:bg-rose-400/10 dark:text-rose-300 dark:ring-rose-400/25">
          <Icon icon={UiKey} className="text-[11px]" />
          Password in URL
        </span>
      ) : null}
      {connection.insecureTLS ? (
        <span className="inline-flex items-center gap-1 rounded bg-amber-100 px-1.5 py-0.5 text-[11px] text-amber-800 ring-1 ring-inset ring-amber-300/60 dark:bg-amber-400/10 dark:text-amber-300 dark:ring-amber-400/25">
          <Icon icon={UiWarningTriangle} className="text-[11px]" />
          TLS verification off
        </span>
      ) : null}
    </>
  );
}

function ConnectionHealthLegend() {
  return (
    <p className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
      {(
        ["healthy", "credentials", "unreachable", "unverifiable"] as const
      ).map((state) => (
        <span key={state} className="inline-flex items-center gap-1.5">
          <span className={cn("inline-block h-3 w-0.5", HEALTH_STYLES[state].dot)} />
          {HEALTH_LEGEND_LABELS[state]}
        </span>
      ))}
    </p>
  );
}

function ageLabel(updatedAt: string): string {
  const timestamp = Date.parse(updatedAt);
  if (!Number.isFinite(timestamp)) return "—";
  return `${Math.max(0, Math.floor((Date.now() - timestamp) / 86_400_000))}d`;
}
